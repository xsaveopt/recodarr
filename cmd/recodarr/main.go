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
	"strings"
	"syscall"
	"time"

	agentpkg "github.com/xsaveopt/recodarr/internal/agent"
	"github.com/xsaveopt/recodarr/internal/api"
	"github.com/xsaveopt/recodarr/internal/arr"
	"github.com/xsaveopt/recodarr/internal/auth"
	"github.com/xsaveopt/recodarr/internal/handbrake"
	"github.com/xsaveopt/recodarr/internal/health"
	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/logging"
	"github.com/xsaveopt/recodarr/internal/qbit"
	"github.com/xsaveopt/recodarr/internal/store"
	"github.com/xsaveopt/recodarr/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin":
			runResetAdmin(envOr("RECODARR_DATA_DIR", "/data"))
			return nil
		case "-h", "--help", "help":
			printHelp()
			return nil
		}
	}

	switch strings.ToLower(envOr("RECODARR_MODE", "server")) {
	case "server":
		return runServer()
	case "agent":
		return runAgent()
	default:
		return fmt.Errorf("unknown RECODARR_MODE=%q (expected: server, agent)", os.Getenv("RECODARR_MODE"))
	}
}

func runServer() error {
	dataDir := envOr("RECODARR_DATA_DIR", "/data")
	addr := envOr("RECODARR_ADDR", ":8080")

	st, err := store.Open(dataDir + "/recodarr.db")
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	logCfg, _ := st.LoadAppSettings(context.Background())

	sinks, err := logging.Setup(logging.Options{
		Dir:           filepath.Join(dataDir, "logs"),
		AppLevel:      logging.ParseLevel(logCfg.LogAppLevel),
		RotateEnabled: logCfg.LogRotateEnabled,
		MaxSizeMB:     logCfg.LogMaxSizeMB,
		MaxAgeDays:    logCfg.LogMaxAgeDays,
		MaxBackups:    logCfg.LogMaxBackups,
		Compress:      logCfg.LogCompress,
	})
	if err != nil {
		return fmt.Errorf("logging setup: %w", err)
	}
	defer sinks.Close()
	logger := sinks.App

	qbit.HTTPTransport = logging.OutboundTransport(http.DefaultTransport, sinks.Outbound)
	arr.HTTPTransport = logging.OutboundTransport(http.DefaultTransport, sinks.Outbound)

	_ = auth.New(st.DB).PurgeExpiredSessions(context.Background())

	if v := handbrake.VersionString(); strings.HasPrefix(v, "(HandBrakeCLI not found)") {
		logger.Warn("HandBrakeCLI not found on PATH — encodes will fail until installed")
	} else {
		logger.Info("handbrake detected", "version", strings.SplitN(v, "\n", 2)[0])

		go handbrake.QueryCaps()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	recoverOrphanEncodes(ctx, st)

	worker := job.NewWorker(st)
	worker.HandbrakeWriterFor = sinks.HandbrakeFor
	go worker.Run(ctx)

	hc := health.New(st)
	go hc.Run(ctx)

	worker.SetRemoteEncoderResolver(func(rctx context.Context) job.RemoteEncoder {
		cfg, err := st.LoadAppSettings(rctx)
		if err != nil || !cfg.AgentEnabled || cfg.AgentURL == "" || cfg.AgentToken == "" {
			return nil
		}
		client := agentpkg.NewClient(cfg.AgentURL, cfg.AgentToken)
		hs, err := client.Ping(rctx)
		if err != nil {
			if cfg.AgentFallbackLocal {
				slog.Warn("remote agent unreachable, falling back to local encode", "url", cfg.AgentURL, "err", err)
				return nil
			}

			slog.Warn("remote agent unreachable, fallback disabled — encode will fail", "url", cfg.AgentURL, "err", err)
			return client
		}
		client.SetLocalFS(hs.LocalFS)
		return client
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(st, worker, hc, sinks, web.Assets(), sinks.Access),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("recodarr listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

func recoverOrphanEncodes(ctx context.Context, st *store.Store) {
	paths, err := st.RecoverOrphanEncoding(ctx)
	if err != nil {
		slog.Error("recover orphan encodes", "err", err)
		return
	}
	if len(paths) == 0 {
		return
	}
	slog.Warn("recovered orphan encoding jobs", "count", len(paths))
	for _, p := range paths {
		dir := filepath.Dir(p)
		base := filepath.Base(p)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		needle := "." + base + ".recodarr.tmp"
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), needle) {
				full := filepath.Join(dir, e.Name())
				if err := os.Remove(full); err == nil {
					slog.Debug("removed stale encode tmp", "path", full)
				}
			}
		}
	}
}

func runResetAdmin(dataDir string) {
	st, err := store.Open(dataDir + "/recodarr.db")
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	resetErr := auth.New(st.DB).ResetAdmin(context.Background())
	_ = st.Close()
	if resetErr != nil {
		slog.Error("reset admin", "err", resetErr)
		os.Exit(1)
	}
	slog.Info("admin user removed; visit the app to set up a new one")
}

func printHelp() {
	const usage = `recodarr — *arr-companion re-encoder

Usage:
  recodarr               start the server
  recodarr reset-admin   wipe the admin user; first visit shows setup screen again
  recodarr help          show this message

Env:
  RECODARR_DATA_DIR  data directory (default /data)
  RECODARR_ADDR      listen address (default :8080)
`
	_, _ = os.Stdout.WriteString(usage)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
