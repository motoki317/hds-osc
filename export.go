package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hypebeast/go-osc/osc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/samber/lo"
)

type exporter interface {
	Send(heartRate int) error
}

type nonBlockingExporter struct {
	exporters []exporter
}

type httpServerExporter struct {
	upgrader websocket.Upgrader

	clients     []chan *httpSenderMessage
	clientsLock sync.Mutex

	heartRate    int
	receivedTime time.Time
}

func newHTTPServerExporter(port int) *httpServerExporter {
	h := &httpServerExporter{
		upgrader: websocket.Upgrader{},
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.HandlerFunc(h.getLatest))
	mux.Handle("GET /ws", http.HandlerFunc(h.connectWS))

	go func() {
		slog.Info("HTTP exporter listening...", "port", port)
		if err := http.ListenAndServe(":"+strconv.Itoa(port), mux); err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
	}()

	return h
}

type httpSenderMessage struct {
	HeartRate int    `json:"heartRate"`
	Time      string `json:"time"`
}

func (h *httpServerExporter) Send(rate int) error {
	// Update latest data
	h.heartRate = rate
	h.receivedTime = time.Now()

	// Send msg to all connected clients
	h.clientsLock.Lock()
	msg := &httpSenderMessage{
		HeartRate: h.heartRate,
		Time:      h.receivedTime.Format(time.RFC3339),
	}
	for _, ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
	h.clientsLock.Unlock()
	return nil
}

func (h *httpServerExporter) getLatest(w http.ResponseWriter, _ *http.Request) {
	type latest struct {
		HeartRate int    `json:"heartRate"`
		Time      string `json:"time"`
	}

	if h.receivedTime.IsZero() {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	l := latest{
		HeartRate: h.heartRate,
		Time:      h.receivedTime.Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(l); err != nil {
		slog.Error("Serving GET /latest", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (h *httpServerExporter) connectWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("Upgrading connection", "err", err)
		return
	}
	defer conn.Close()

	remoteAddr := conn.RemoteAddr()

	ch := make(chan *httpSenderMessage)
	h.clientsLock.Lock()
	h.clients = append(h.clients, ch)
	slog.Info("New WebSocket connection", "addr", remoteAddr, "current", len(h.clients))
	h.clientsLock.Unlock()
	defer func() {
		h.clientsLock.Lock()
		h.clients = lo.Without(h.clients, ch)
		slog.Info("Closing WebSocket connection", "addr", remoteAddr, "current", len(h.clients))
		h.clientsLock.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		defer cancel()
		for {
			_, _, err := conn.ReadMessage()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				slog.Error("Reading message", "err", err)
				return
			}
		}
	}()

	// Send first data (if any)
	if !h.receivedTime.IsZero() {
		msg := &httpSenderMessage{
			HeartRate: h.heartRate,
			Time:      h.receivedTime.Format(time.RFC3339),
		}
		if err = conn.WriteJSON(msg); err != nil {
			slog.Error("Writing message", "err", err)
			return
		}
	}

	// Send real-time data
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if err = conn.WriteJSON(msg); err != nil {
				slog.Error("Writing message", "err", err)
				return
			}
		}
	}
}

type oscExporter struct {
	client       *osc.Client
	heartRateMax float32
	addrName     string
}

func newOSCExporter(sendIP string, sendPort int, addrName string) *oscExporter {
	slog.Info("OSC config", "addr", addrName, "ip", sendIP+":"+strconv.Itoa(sendPort))
	client := osc.NewClient(sendIP, sendPort)
	return &oscExporter{
		client:       client,
		heartRateMax: 256.0,
		addrName:     addrName,
	}
}

func (o *oscExporter) Send(rate int) error {
	msg := osc.NewMessage(o.addrName)
	floatRate := float32(rate) / o.heartRateMax
	msg.Append(floatRate)
	return o.client.Send(msg)
}

type prometheusExporter struct {
	rateGauge prometheus.Gauge
	registry  *prometheus.Registry

	lastReceived     time.Time
	lastReceivedLock sync.RWMutex
}

func newPrometheusExporter(port int) *prometheusExporter {
	rateGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "heart_rate",
		Help: "Current heart rate in beats per minute",
	})
	// Create a custom registry without default collectors
	registry := prometheus.NewRegistry()
	registry.MustRegister(rateGauge)
	exporter := &prometheusExporter{
		rateGauge: rateGauge,
		registry:  registry,
	}

	// Start HTTP server for metrics
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", exporter)

		slog.Info("Prometheus metrics server listening...", "port", port)
		if err := http.ListenAndServe(":"+strconv.Itoa(port), mux); err != nil {
			slog.Error("Starting prometheus metrics server", "err", err)
			os.Exit(1)
		}
	}()

	return exporter
}

func (p *prometheusExporter) Send(rate int) error {
	p.lastReceivedLock.Lock()
	p.lastReceived = time.Now()
	p.lastReceivedLock.Unlock()

	p.rateGauge.Set(float64(rate))
	return nil
}

// ServeHTTP implements http.Handler to serve metrics only when data is fresh
func (p *prometheusExporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.lastReceivedLock.RLock()
	lastReceived := p.lastReceived
	p.lastReceivedLock.RUnlock()

	// If no data received yet or data is stale (older than 30 seconds), return empty response
	if lastReceived.IsZero() || time.Since(lastReceived) > 30*time.Second {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Otherwise, serve metrics from our custom registry
	promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}
