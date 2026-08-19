// Command opscore-server is the Phase 12.2 thin deployment entrypoint.
//
// It is a thin binding ONLY to the composition root (internal/harness): it loads
// the (optional) deployment config, calls harness.Build then harness.Serve, and
// never imports the frozen execution path or new's its own Reader graph
// (ADR-026 SHOULD-1 / SHOULD-7 — dependency direction is cmd → harness → facades/
// external/capabilities, never the reverse). Phase 15.2 adds operational glue
// only: config load (fail-closed), structured logging, and signal-driven
// graceful shutdown. It is still the SOLE composition root (A-1).
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/YuDong999/opscore/internal/harness"
)

func main() {
	configPath := flag.String("config", "", "path to opscore.json deployment config (optional)")
	addr := flag.String("addr", "", "external/v1 listen address (overrides config server.listen)")
	probeAddr := flag.String("probe", "", "operational health/readiness bind (overrides config server.probe)")
	flag.Parse()

	// Load deployment config (fail-closed, ADR-032 §3.6). Flag-only fallback uses
	// all operational defaults.
	cfg := harness.DefaultConfig()
	if *configPath != "" {
		loaded, err := harness.LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("opscore-server: invalid deployment config: %v", err)
		}
		cfg = loaded
	}
	if *addr != "" {
		cfg.ListenAddr = *addr
	}
	if *probeAddr != "" {
		cfg.ProbeAddr = *probeAddr
	}

	// Structured logging — observability only (A-7). Never a control channel.
	setupLogging(cfg.Logging)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h, err := harness.Build(ctx, cfg)
	if err != nil {
		log.Fatalf("opscore-server: build failed: %v", err)
	}

	// Surface the write plane explicitly so ops can confirm fail-closed posture
	// (MUST-P17-14): if no OPSCORE_MANAGEMENT_TOKEN was supplied the server runs
	// as a read-only deployment and "(disabled)" is logged instead of an address.
	mgmtField := h.ManagementAddr()
	if !h.ManagementEnabled() {
		mgmtField = "(disabled)"
	}

	slog.Info("opscore-server: serving",
		"external", h.ExternalAddr(),
		"probe", h.ProbeAddr(),
		"management", mgmtField,
		"schema", cfg.Version,
		"policyStore", cfg.PolicyStoreDir,
	)

	if err := h.Serve(ctx); err != nil {
		log.Fatalf("opscore-server: serve failed: %v", err)
	}
	slog.Info("opscore-server: shutdown complete")
}

// setupLogging configures the process-wide slog logger from deployment config.
// It is observability only (A-7) — it never switches capability behavior.
func setupLogging(lc harness.LoggingConfig) {
	level := slog.LevelInfo
	switch lc.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "info", "":
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if lc.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
