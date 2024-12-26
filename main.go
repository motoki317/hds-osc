package main

import (
	"encoding/json"
	"flag"
	"github.com/hypebeast/go-osc/osc"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

var (
	listenPort  = flag.Int("port", 3476, "Websocket port to listen on")
	oscSendIP   = flag.String("osc-ip", "127.0.0.1", "IP address of OSC to send data to")
	oscSendPort = flag.Int("osc-port", 9000, "OSC port to send data to")
	oscAddrName = flag.String("osc-addr", "/avatar/parameters/HeartRate", "Name of OSC address")
)

type hdsRequest struct {
	Data string `json:"data"`
}

const dataPrefix = "heartRate:"

func dataHandler(w http.ResponseWriter, r *http.Request) {
	var data hdsRequest
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		slog.Error("error decoding request", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("Received req", "data", data.Data)
	if !strings.HasPrefix(data.Data, dataPrefix) {
		slog.Error("Invalid data format", "data", data.Data)
		http.Error(w, "Invalid data format", http.StatusBadRequest)
		return
	}
	rateStr := strings.TrimPrefix(data.Data, dataPrefix)
	rate, err := strconv.Atoi(rateStr)
	if err != nil {
		slog.Error("Error parsing heartRate", "data", data.Data)
		http.Error(w, "Invalid data format", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	if err = sendOSCData(rate); err != nil {
		slog.Error("Error sending osc data", "err", err)
	}
}

const heartRateMax = 256.0

var oscClient *osc.Client

func sendOSCData(rate int) error {
	msg := osc.NewMessage(*oscAddrName)
	floatRate := float32(rate) / heartRateMax
	msg.Append(floatRate)
	return oscClient.Send(msg)
}

func main() {
	flag.Parse()

	slog.Info("OSC config", "addr", *oscAddrName, "ip", *oscSendIP+":"+strconv.Itoa(*oscSendPort))
	oscClient = osc.NewClient(*oscSendIP, *oscSendPort)

	// See: https://github.com/Rexios80/hds_desktop/blob/master/bin/hds_desktop.dart
	mux := http.NewServeMux()
	mux.Handle("PUT /", http.HandlerFunc(dataHandler))
	http.Handle("/", mux)

	slog.Info("Server listening...", "port", *listenPort)
	if err := http.ListenAndServe(":"+strconv.Itoa(*listenPort), nil); err != nil {
		slog.Error(err.Error())
	}
}
