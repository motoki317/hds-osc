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

type hdsReceiver struct {
	exporters []exporter
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

const hdsDataPrefix = "heartRate:"

func (h *hdsReceiver) dataHandler(w http.ResponseWriter, r *http.Request) {
	var data hdsRequest
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		slog.Error("error decoding request", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("Received req", "data", data.Data)
	if !strings.HasPrefix(data.Data, hdsDataPrefix) {
		slog.Error("Invalid data format", "data", data.Data)
		http.Error(w, "Invalid data format", http.StatusBadRequest)
		return
	}
	rateStr := strings.TrimPrefix(data.Data, hdsDataPrefix)
	rate, err := strconv.Atoi(rateStr)
	if err != nil {
		slog.Error("Error parsing heartRate", "data", data.Data)
		http.Error(w, "Invalid data format", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	for _, s := range h.exporters {
		if err = s.Send(rate); err != nil {
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

		var msg httpSenderMessage
		if err = json.NewDecoder(bytes.NewReader(rawMsg)).Decode(&msg); err != nil {
			return fmt.Errorf("decoding websocket message: %v", err)
		}
		slog.Info("Received msg", "msg", msg)

		for _, s := range h.exporters {
			if err = s.Send(msg.HeartRate); err != nil {
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
