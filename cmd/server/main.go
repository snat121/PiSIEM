package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/snat121/PiSIEM/internal/agent"
	"github.com/snat121/PiSIEM/internal/config"
	"github.com/snat121/PiSIEM/internal/engine"
	"github.com/snat121/PiSIEM/internal/storage"
	"github.com/snat121/PiSIEM/internal/syslog"
	"github.com/snat121/PiSIEM/internal/web"
	"github.com/snat121/PiSIEM/ui"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel)
	logger.Info("pisiem starting", "config", *cfgPath)

	rules, err := config.LoadRules(cfg.RulesPath)
	if err != nil {
		logger.Warn("rules load failed; running without signature rules", "path", cfg.RulesPath, "err", err)
	} else {
		logger.Info("rules loaded", "count", len(rules))
	}

	store, err := storage.Open(cfg.DBPath, 5000, cfg.BufferFlushSize, cfg.BufferFlushIntervalSeconds, logger)
	if err != nil {
		logger.Error("open storage", "err", err)
		os.Exit(1)
	}
	store.Run()

	wh := engine.NewWebhook(logger)
	eng := engine.New(rules, cfg.EnableAnomalyDetection, cfg.AnomalyMultiplier, cfg.AnomalyWebhookURL, wh, logger)

	stopEvict := make(chan struct{})
	go eng.RunEvictionLoop(stopEvict)

	// Shared sink: run each event through the engine, then into the storage
	// buffer. Both syslog and agent ingestion share this path, so every event
	// sees the same rules and anomaly detector.
	sink := func(e storage.LogEvent) {
		eng.Process(e)
		store.Ingest(e)
	}

	ctx, cancel := context.WithCancel(context.Background())

	sl := syslog.NewListener(cfg.SyslogUDPPort, cfg.SyslogTCPPort, cfg.EnableTCPSyslog, sink, logger)
	if err := sl.Start(ctx); err != nil {
		logger.Error("syslog start", "err", err)
		cancel()
		store.Stop()
		os.Exit(1)
	}

	var al *agent.Listener
	if cfg.EnableAgentListener {
		al = agent.NewListener(cfg.AgentPort, newAgentAdapter(sink), logger)
		if err := al.Start(ctx); err != nil {
			logger.Error("agent listener start", "err", err)
		}
	}

	webSrv, err := web.NewServer(":"+strconv.Itoa(cfg.WebPort), store, ui.FS, logger)
	if err != nil {
		logger.Error("web init", "err", err)
		sl.Stop()
		if al != nil {
			al.Stop()
		}
		cancel()
		store.Stop()
		os.Exit(1)
	}
	if err := webSrv.Start(); err != nil {
		logger.Error("web start", "err", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("shutdown initiated; draining buffer", "signal", sig.String())

	cancel()
	close(stopEvict)
	sl.Stop()
	if al != nil {
		al.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := webSrv.Stop(shutdownCtx); err != nil {
		logger.Warn("web shutdown", "err", err)
	}

	store.Stop()
	logger.Info("pisiem stopped cleanly")
}

// newAgentAdapter converts the agent wire-format event to a storage.LogEvent
// (tagged source="agent") and forwards it into the shared sink. Preserves the
// JSON line as RawLog for forensic fidelity.
func newAgentAdapter(sink func(storage.LogEvent)) agent.EventHandler {
	return func(e agent.AgentEvent, _ net.Addr) {
		raw, _ := json.Marshal(e)
		ts := e.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		sink(storage.LogEvent{
			Timestamp:  ts,
			Host:       e.Host,
			Severity:   e.Severity,
			Facility:   1,
			Message:    e.Message,
			RawLog:     string(raw),
			Source:     "agent",
			SourceFile: e.SourceFile,
		})
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
