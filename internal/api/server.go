package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/xsaveopt/recodarr/internal/auth"
	"github.com/xsaveopt/recodarr/internal/health"
	"github.com/xsaveopt/recodarr/internal/job"
	"github.com/xsaveopt/recodarr/internal/metrics"
	"github.com/xsaveopt/recodarr/internal/store"
)

func NewRouter(st *store.Store, worker *job.Worker, hc *health.Checker, lls LogLevelSetter, assets fs.FS, access *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if os.Getenv("RECODARR_TRUST_PROXY") == "1" {
		r.Use(middleware.ClientIPFromXFF())
	}
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(access))
	r.Use(securityHeaders)

	a := auth.New(st.DB)

	r.Route("/api", func(r chi.Router) {
		r.Use(requireCustomHeader)

		r.Use(maxBody(1 << 20))

		r.Route("/auth", func(r chi.Router) {
			r.Use(middleware.Timeout(15 * time.Second))
			registerAuthRoutes(r, a)
		})

		r.Group(func(r chi.Router) {
			r.Use(a.Middleware)

			r.Get("/worker/progress", workerProgressSSE(worker))

			r.Group(func(r chi.Router) {
				r.Use(middleware.Timeout(30 * time.Second))
				registerAdminRoutes(r, st, worker, hc, lls)
			})
		})
	})

	r.Method("GET", "/metrics", metrics.Handler(st, worker, os.Getenv("RECODARR_METRICS_TOKEN")))

	r.Get("/health", healthHandler(st))

	r.Handle("/*", spaHandler(assets))

	return r
}

func healthHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := st.DB.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("degraded"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("up"))
	}
}
