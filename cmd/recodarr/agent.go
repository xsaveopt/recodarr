package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sratabix/recodarr/internal/agent"
	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/logging"
)

func runAgent() error {
	dataDir := envOr("RECODARR_DATA_DIR", "/data")
	addr := envOr("RECODARR_AGENT_ADDR", ":8090")

	token := os.Getenv("RECODARR_AGENT_TOKEN")
	if strings.TrimSpace(token) == "" {
		return errors.New("RECODARR_AGENT_TOKEN is required in agent mode (any non-empty value; generate one with: openssl rand -hex 32)")
	}

	maxParallel := 1
	if v := os.Getenv("RECODARR_AGENT_MAX_PARALLEL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("RECODARR_AGENT_MAX_PARALLEL: expected integer ≥ 1, got %q", v)
		}
		maxParallel = n
	}

	localFS := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RECODARR_AGENT_LOCAL_FS"))) {
	case "1", "true", "yes", "on":
		localFS = true
	}

	sinks, err := logging.Setup(logging.Options{
		Dir:           filepath.Join(dataDir, "logs"),
		AppLevel:      logging.ParseLevel(envOr("RECODARR_AGENT_LOG_LEVEL", "INFO")),
		RotateEnabled: true,
		MaxSizeMB:     50,
		MaxAgeDays:    30,
		MaxBackups:    5,
	})
	if err != nil {
		return fmt.Errorf("logging setup: %w", err)
	}
	defer sinks.Close()
	logger := sinks.App

	if v := handbrake.VersionString(); strings.HasPrefix(v, "(HandBrakeCLI not found)") {
		logger.Warn("HandBrakeCLI not found on PATH — encodes will fail until installed")
	} else {
		logger.Info("handbrake detected", "version", strings.SplitN(v, "\n", 2)[0])
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	agentDir := filepath.Join(dataDir, "agent")
	store, err := agent.OpenStore(agentDir)
	if err != nil {
		return fmt.Errorf("open agent store: %w", err)
	}

	runner := agent.NewRunner(store, maxParallel, sinks.HandbrakeFor)
	go runner.Run(ctx)

	server := agent.NewServer(store, runner, token, localFS, sinks.HandbrakeFor)

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("recodarr agent listening", "addr", addr, "data", agentDir, "maxParallel", maxParallel, "localFS", localFS)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

var _ = slog.LevelInfo
