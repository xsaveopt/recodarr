package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/xsaveopt/recodarr/internal/arr"
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

		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

	r.Route("/webhook", func(r chi.Router) {
		r.Use(maxBody(1 << 20))
		r.Post("/sonarr/{id}", handleArrWebhook(st, arr.KindSonarr))
		r.Post("/radarr/{id}", handleArrWebhook(st, arr.KindRadarr))
	})

	r.Handle("/*", spaHandler(assets))

	return r
}
