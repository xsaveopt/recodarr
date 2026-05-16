package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/auth"
	"github.com/sratabix/recodarr/internal/health"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/metrics"
	"github.com/sratabix/recodarr/internal/store"
)

func NewRouter(st *store.Store, worker *job.Worker, hc *health.Checker, assets fs.FS, access *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Only honor X-Forwarded-For / X-Real-IP when explicitly told there's a trusted
	// reverse proxy in front. Otherwise an attacker on a directly-exposed deployment
	// could spoof XFF to bypass per-IP login throttling.
	if os.Getenv("RECODARR_TRUST_PROXY") == "1" {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(access))
	r.Use(securityHeaders)

	a := auth.New(st.DB)

	r.Route("/api", func(r chi.Router) {
		// CSRF: require a custom header on every mutating /api/* request. The SPA fetcher
		// adds it; cross-origin browser forms cannot (without a preflight we never allow).
		r.Use(requireCustomHeader)
		// 1 MiB body cap on /api/* — JSON DTOs are tiny.
		r.Use(maxBody(1 << 20))

		r.Route("/auth", func(r chi.Router) {
			r.Use(middleware.Timeout(15 * time.Second))
			registerAuthRoutes(r, a)
		})

		// Health check is intentionally unauthenticated so external uptime
		// monitors / container orchestrators can hit it without a session.
		// Returns no sensitive data.
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})

		// Everything below requires a valid session cookie.
		r.Group(func(r chi.Router) {
			r.Use(a.Middleware)

			// Streaming endpoint must not be wrapped in a timeout middleware.
			r.Get("/worker/progress", workerProgressSSE(worker))

			r.Group(func(r chi.Router) {
				r.Use(middleware.Timeout(30 * time.Second))
				registerAdminRoutes(r, st, worker, hc)
			})
		})
	})

	// Prometheus scrape endpoint. Mounted outside /api so it isn't behind the
	// session-cookie auth middleware. Optional bearer-token gate via
	// RECODARR_METRICS_TOKEN; without it, /metrics is anonymous (matching the
	// convention for self-hosted exporters — no secrets are emitted).
	r.Method("GET", "/metrics", metrics.Handler(st, worker, os.Getenv("RECODARR_METRICS_TOKEN")))

	// Webhooks authenticate via per-instance HTTP Basic auth (see webhooks.go).
	// 1 MiB body cap; *arr Connect payloads are well under this.
	r.Route("/webhook", func(r chi.Router) {
		r.Use(maxBody(1 << 20))
		r.Post("/sonarr/{id}", handleArrWebhook(st, arr.KindSonarr))
		r.Post("/radarr/{id}", handleArrWebhook(st, arr.KindRadarr))
	})

	r.Handle("/*", spaHandler(assets))

	return r
}
