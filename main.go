package main

import (
	"flag"
	"log/slog"
	"os"
)

// Receiving components
var (
	receiveMode = flag.String("receive-mode", "hds", "Receive mode: hds, ws-pull")
	hdsPort     = flag.Int("hds-port", 3476, "HTTP port to listen on HDS data")
	wsPullURL   = flag.String("ws-pull-url", "ws://localhost:8080/ws", "WebSocket URL to pull data from")
)

// Exporting components
var (
	wsServerEnabled = flag.Bool("ws-server-enabled", false, "Enable WebSocket server")
	wsServerPort    = flag.Int("ws-server-port", 8080, "WebSocket server port to listen on")

	oscEnabled  = flag.Bool("osc-enabled", true, "Enable OSC sending")
	oscSendIP   = flag.String("osc-ip", "127.0.0.1", "IP address of OSC to send data to")
	oscSendPort = flag.Int("osc-port", 9000, "OSC port to send data to")
	oscAddrName = flag.String("osc-addr", "/avatar/parameters/HeartRate", "Name of OSC address")

	promEnabled = flag.Bool("prom-enabled", false, "Enable Prometheus metrics")
	promPort    = flag.Int("prom-port", 9090, "Prometheus metrics port to listen on")
)

func main() {
	flag.Parse()

	var exporters []exporter
	if *wsServerEnabled {
		exporters = append(exporters, newHTTPServerExporter(*wsServerPort))
	}
	if *oscEnabled {
		exporters = append(exporters, newOSCExporter(*oscSendIP, *oscSendPort, *oscAddrName))
	}
	if *promEnabled {
		exporters = append(exporters, newPrometheusExporter(*promPort))
	}

	var r receiver
	switch *receiveMode {
	case "hds":
		r = newHDSReceiver(exporters)
	case "ws-pull":
		r = newWSPullReceiver(exporters, *wsPullURL)
	default:
		slog.Error("Invalid receive mode", "mode", *receiveMode)
		os.Exit(1)
	}

	r.Start()
}
