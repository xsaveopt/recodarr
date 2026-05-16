package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sratabix/recodarr/internal/api"
	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/auth"
	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/logging"
	"github.com/sratabix/recodarr/internal/qbit"
	"github.com/sratabix/recodarr/internal/store"
	"github.com/sratabix/recodarr/web"
)

func main() {
	dataDir := envOr("RECODARR_DATA_DIR", "/data")
	addr := envOr("RECODARR_ADDR", ":8080")

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin":
			runResetAdmin(dataDir)
			return
		case "-h", "--help", "help":
			printHelp()
			return
		}
	}

	sinks, err := logging.Setup(logging.Options{
		Dir:      filepath.Join(dataDir, "logs"),
		AppLevel: slog.LevelInfo,
	})
	if err != nil {
		// Pre-Setup error: slog.Default is still the bootstrap JSON handler,
		// which is fine for this one-shot failure path.
		slog.Error("logging setup", "err", err)
		os.Exit(1)
	}
	defer sinks.Close()
	logger := sinks.App

	// Route outbound HTTP for the *arr and qBit clients through the logging
	// transport so calls land in outbound.log instead of stdout.
	qbit.HTTPTransport = logging.OutboundTransport(http.DefaultTransport, sinks.Outbound)
	arr.HTTPTransport = logging.OutboundTransport(http.DefaultTransport, sinks.Outbound)

	st, err := store.Open(dataDir + "/recodarr.db")
	if err != nil {
		logger.Error("open store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	// Best-effort cleanup of expired session rows on boot.
	_ = auth.New(st.DB).PurgeExpiredSessions(context.Background())

	// Probe HandBrakeCLI now so a missing binary is loud at startup, not silent
	// until the first encode hours later.
	if v := handbrake.VersionString(); strings.HasPrefix(v, "(HandBrakeCLI not found)") {
		logger.Warn("HandBrakeCLI not found on PATH — encodes will fail until installed")
	} else {
		logger.Info("handbrake detected", "version", strings.SplitN(v, "\n", 2)[0])
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	recoverOrphanEncodes(ctx, st)

	worker := job.NewWorker(st)
	worker.HandbrakeWriterFor = sinks.HandbrakeFor
	go worker.Run(ctx)

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewRouter(st, worker, web.Assets(), sinks.Access),
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
}

// recoverOrphanEncodes resets any 'encoding' jobs left over from a previous crash and
// removes their leftover .recodarr.tmp.* sibling files so the next encode starts clean.
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
					slog.Info("removed stale encode tmp", "path", full)
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
