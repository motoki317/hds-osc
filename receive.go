package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type receiver interface {
	Start()
}

// Example values:
// heartRate:80
// stepCount:80
// distanceTraveled:60.89095629064832
// speed:0.8606014661155669
// calories:7
type healthData struct {
	Time             time.Time `json:"time"`
	HeartRate        int       `json:"heartRate"`
	StepCount        int       `json:"stepCount"`
	DistanceTraveled float64   `json:"distanceTraveled"`
	Speed            float64   `json:"speed"`
	Calories         int       `json:"calories"`
}

func (d *healthData) Update(key string, value float64) {
	d.Time = time.Now()
	switch key {
	case "heartRate":
		d.HeartRate = int(value)
	case "stepCount":
		d.StepCount = int(value)
	case "distanceTraveled":
		d.DistanceTraveled = value
	case "speed":
		d.Speed = value
	case "calories":
		d.Calories = int(value)
	default:
		slog.Warn("Unknown key", "key", key)
	}
}

type hdsReceiver struct {
	exporters []exporter
	data      healthData
}

func newHDSReceiver(exporters []exporter) *hdsReceiver {
	return &hdsReceiver{
		exporters: exporters,
	}
}

func (h *hdsReceiver) Start() {
	// See: https://github.com/Rexios80/hds_desktop/blob/master/bin/hds_desktop.dart
	mux := http.NewServeMux()
	mux.Handle("PUT /", http.HandlerFunc(h.dataHandler))

	slog.Info("HDS Receiver listening...", "port", *hdsPort)
	if err := http.ListenAndServe(":"+strconv.Itoa(*hdsPort), mux); err != nil {
		slog.Error(err.Error())
	}
}

type hdsRequest struct {
	Data string `json:"data"`
}

func (h *hdsReceiver) dataHandler(w http.ResponseWriter, r *http.Request) {
	var data hdsRequest
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		slog.Error("error decoding request", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("Received hds req", "data", data.Data)
	parts := strings.Split(data.Data, ":")
	if len(parts) != 2 {
		slog.Error("Invalid data format", "data", data.Data)
		http.Error(w, "Invalid data format", http.StatusBadRequest)
		return
	}
	key, valueStr := parts[0], parts[1]
	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		slog.Error("Error parsing value", "value", valueStr)
		http.Error(w, "Invalid value format", http.StatusBadRequest)
		return
	}
	h.data.Update(key, value)

	w.WriteHeader(http.StatusOK)

	for _, s := range h.exporters {
		if err = s.Update(h.data, key); err != nil {
			slog.Error("Sending data", "err", err)
		}
	}
}

type wsPullReceiver struct {
	exporters   []exporter
	addr        string
	nextBackoff time.Duration
}

const (
	wsPullFirstWait  = time.Second
	wsPullMaxBackoff = 10 * time.Minute
)

func newWSPullReceiver(exporters []exporter, addr string) *wsPullReceiver {
	return &wsPullReceiver{
		exporters:   exporters,
		addr:        addr,
		nextBackoff: wsPullFirstWait,
	}
}

func (h *wsPullReceiver) connect() error {
	c, _, err := websocket.DefaultDialer.Dial(h.addr, nil)
	if err != nil {
		return fmt.Errorf("dialing websocket server: %v", err)
	}
	defer c.Close()

	slog.Info("WebSocket connected, now receiving messages...")
	for {
		_, rawMsg, err := c.ReadMessage()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading websocket: %v", err)
		}

		var msg wsUpdateMessage
		if err = json.NewDecoder(bytes.NewReader(rawMsg)).Decode(&msg); err != nil {
			return fmt.Errorf("decoding websocket message: %v", err)
		}
		slog.Info("Received msg", "updatedKey", msg.UpdatedKey, "data", msg.Data)

		for _, s := range h.exporters {
			if err = s.Update(msg.Data, msg.UpdatedKey); err != nil {
				slog.Error("Sending data", "err", err)
			}
		}
	}
}

func (h *wsPullReceiver) Start() {
	for {
		err := h.connect()
		if err != nil {
			slog.Error("WebSocket connection", "err", err)
		}

		// Sleep before reconnecting
		if err == nil {
			h.nextBackoff = wsPullFirstWait
			slog.Info("Reconnecting in", "duration", h.nextBackoff)
			time.Sleep(h.nextBackoff)
		} else {
			slog.Error("Reconnecting in", "duration", h.nextBackoff)
			time.Sleep(h.nextBackoff)
			h.nextBackoff = min(h.nextBackoff*2, wsPullMaxBackoff)
		}
	}
}
