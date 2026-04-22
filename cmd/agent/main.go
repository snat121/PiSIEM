package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snat121/PiSIEM/internal/agent"
	"github.com/snat121/PiSIEM/internal/config"
)

func main() {
	cfgPath := flag.String("config", "agent.yaml", "path to agent.yaml")
	flag.Parse()

	cfg, err := config.LoadAgent(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("pisiem-agent starting",
		"server", fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		"files", len(cfg.WatchFiles))

	tcfg := agent.TailerConfig{
		ServerHost:           cfg.ServerHost,
		ServerPort:           cfg.ServerPort,
		BatchSize:            cfg.BatchSize,
		BatchInterval:        time.Duration(cfg.BatchIntervalSeconds) * time.Second,
		ReconnectMaxInterval: time.Duration(cfg.ReconnectMaxIntervalSeconds) * time.Second,
		LocalBufferMaxEvents: cfg.LocalBufferMaxEvents,
	}
	for _, f := range cfg.WatchFiles {
		tcfg.Files = append(tcfg.Files, agent.TailFile{Path: f.Path, Severity: f.Severity})
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := agent.NewTailer(tcfg, logger)
	t.Start(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("shutdown initiated", "signal", sig.String())
	cancel()
	t.Stop()
	logger.Info("pisiem-agent stopped cleanly")
}
