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
	Update(data healthData, updatedKey string) error
}

type nonBlockingExporter struct {
	exporters []exporter
}

type httpServerExporter struct {
	upgrader websocket.Upgrader

	clients     []chan *wsUpdateMessage
	clientsLock sync.Mutex

	data healthData
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

func (h *httpServerExporter) Update(data healthData, updatedKey string) error {
	// Send msg to all connected clients
	msg := wsUpdateMessage{Data: data, UpdatedKey: updatedKey}
	h.clientsLock.Lock()
	for _, ch := range h.clients {
		select {
		case ch <- &msg:
		default:
		}
	}
	h.clientsLock.Unlock()
	return nil
}

func (h *httpServerExporter) getLatest(w http.ResponseWriter, _ *http.Request) {
	if h.data.Time.IsZero() {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(h.data); err != nil {
		slog.Error("Serving GET /latest", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

type wsUpdateMessage struct {
	Data       healthData `json:"data"`
	UpdatedKey string     `json:"updatedKey"`
}

func (h *httpServerExporter) connectWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("Upgrading connection", "err", err)
		return
	}
	defer conn.Close()

	remoteAddr := conn.RemoteAddr()

	ch := make(chan *wsUpdateMessage)
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
	data := h.data // copy
	if !data.Time.IsZero() {
		msg := wsUpdateMessage{Data: data, UpdatedKey: "all"}
		if err = conn.WriteJSON(&msg); err != nil {
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
	heartRateMax float64
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

func (o *oscExporter) Update(data healthData, updatedKey string) error {
	if updatedKey != "heartRate" && updatedKey != "all" {
		return nil
	}
	msg := osc.NewMessage(o.addrName)
	floatRate := float64(data.HeartRate) / o.heartRateMax
	msg.Append(floatRate)
	return o.client.Send(msg)
}

type prometheusExporter struct {
	registry *prometheus.Registry

	data     healthData
	dataLock sync.RWMutex

	heartRate        prometheus.GaugeFunc
	stepCount        prometheus.CounterFunc
	distanceTraveled prometheus.CounterFunc
	speed            prometheus.GaugeFunc
	calories         prometheus.CounterFunc
}

func newPrometheusExporter(port int) *prometheusExporter {
	e := &prometheusExporter{}
	// Create a custom registry without default collectors
	e.registry = prometheus.NewRegistry()

	e.heartRate = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "heart_rate",
		Help: "Current heart rate in beats per minute",
	}, func() float64 {
		e.dataLock.RLock()
		defer e.dataLock.RUnlock()
		return float64(e.data.HeartRate)
	})
	e.registry.MustRegister(e.heartRate)

	e.stepCount = prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "step_count",
		Help: "Current step count",
	}, func() float64 {
		e.dataLock.RLock()
		defer e.dataLock.RUnlock()
		return float64(e.data.StepCount)
	})
	e.registry.MustRegister(e.stepCount)

	e.distanceTraveled = prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "distance_traveled",
		Help: "Current distance traveled in meters",
	}, func() float64 {
		e.dataLock.RLock()
		defer e.dataLock.RUnlock()
		return e.data.DistanceTraveled
	})
	e.registry.MustRegister(e.distanceTraveled)

	e.speed = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "speed",
		Help: "Current speed in meters per second",
	}, func() float64 {
		e.dataLock.RLock()
		defer e.dataLock.RUnlock()
		return e.data.Speed
	})
	e.registry.MustRegister(e.speed)

	e.calories = prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "calories",
		Help: "Current calories burned",
	}, func() float64 {
		e.dataLock.RLock()
		defer e.dataLock.RUnlock()
		return float64(e.data.Calories)
	})
	e.registry.MustRegister(e.calories)

	// Start HTTP server for metrics
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", e)

		slog.Info("Prometheus metrics server listening...", "port", port)
		if err := http.ListenAndServe(":"+strconv.Itoa(port), mux); err != nil {
			slog.Error("Starting prometheus metrics server", "err", err)
			os.Exit(1)
		}
	}()

	return e
}

func (p *prometheusExporter) Update(data healthData, _ string) error {
	p.dataLock.Lock()
	p.data = data
	p.dataLock.Unlock()
	return nil
}

// ServeHTTP implements http.Handler to serve metrics only when data is fresh
func (p *prometheusExporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.dataLock.RLock()
	lastReceived := p.data.Time
	p.dataLock.RUnlock()

	// If no data received yet or data is stale (older than 30 seconds), return empty response
	if lastReceived.IsZero() || time.Since(lastReceived) > 30*time.Second {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Otherwise, serve metrics from our custom registry
	promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}
