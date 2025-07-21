package main

import (
	"flag"
	"log/slog"
	"os"
	"time"
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

	oscEnabled        = flag.Bool("osc-enabled", true, "Enable OSC sending")
	oscSendIP         = flag.String("osc-ip", "127.0.0.1", "IP address of OSC to send data to")
	oscSendPort       = flag.Int("osc-port", 9000, "OSC port to send data to")
	oscAddrName       = flag.String("osc-addr", "/avatar/parameters/HeartRate", "Name of OSC address")
	oscEnableAddrName = flag.String("osc-enable-addr", "/avatar/parameters/HREnabled", "Name of OSC address for 'enabled' parameter")
	oscEnableDebounce = flag.String("osc-enable-debounce", "60s", "Debounce time for until sending disabled state")

	promEnabled = flag.Bool("prom-enabled", false, "Enable Prometheus metrics")
	promPort    = flag.Int("prom-port", 9090, "Prometheus metrics port to listen on")
)

func main() {
	slog.Info("hds-osc", "version", GetFormattedVersion())
	flag.Parse()

	var exporters []exporter
	if *wsServerEnabled {
		slog.Info("WebSocket server enabled", "port", *wsServerPort)
		exporters = append(exporters, newHTTPServerExporter(*wsServerPort))
	}
	if *oscEnabled {
		slog.Info("OSC enabled", "ip", *oscSendIP, "port", *oscSendPort, "addr", *oscAddrName)
		enableDebounce, err := time.ParseDuration(*oscEnableDebounce)
		if err != nil {
			slog.Error("Invalid debounce time", "err", err)
			os.Exit(1)
		}
		exporters = append(exporters, newOSCExporter(*oscSendIP, *oscSendPort, *oscAddrName, *oscEnableAddrName, enableDebounce))
	}
	if *promEnabled {
		slog.Info("Prometheus enabled", "port", *promPort)
		exporters = append(exporters, newPrometheusExporter(*promPort))
	}

	var r receiver
	switch *receiveMode {
	case "hds":
		slog.Info("HTTP HDS receiver enabled", "port", *hdsPort, "exporters", len(exporters))
		r = newHDSReceiver(exporters)
	case "ws-pull":
		slog.Info("WebSocket pull receiver enabled", "url", *wsPullURL, "exporters", len(exporters))
		r = newWSPullReceiver(exporters, *wsPullURL)
	default:
		slog.Error("Invalid receive mode", "mode", *receiveMode)
		os.Exit(1)
	}

	r.Start()
}
