package api

import (
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sratabix/recodarr/internal/arr"
	"github.com/sratabix/recodarr/internal/auth"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/store"
)

func NewRouter(st *store.Store, worker *job.Worker, assets fs.FS) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	a := auth.New(st.DB)

	r.Route("/api", func(r chi.Router) {
		// CSRF: require a custom header on every mutating /api/* request. The SPA fetcher
		// adds it; cross-origin browser forms cannot (without a preflight we never allow).
		r.Use(requireCustomHeader)

		r.Route("/auth", func(r chi.Router) {
			r.Use(middleware.Timeout(15 * time.Second))
			registerAuthRoutes(r, a)
		})

		// Everything below requires a valid session cookie.
		r.Group(func(r chi.Router) {
			r.Use(a.Middleware)

			// Streaming endpoint must not be wrapped in a timeout middleware.
			r.Get("/worker/progress", workerProgressSSE(worker))

			r.Group(func(r chi.Router) {
				r.Use(middleware.Timeout(30 * time.Second))
				r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				})
				registerAdminRoutes(r, st, worker)
			})
		})
	})

	// Webhooks authenticate via per-instance X-Webhook-Token (see webhooks.go).
	r.Route("/webhook", func(r chi.Router) {
		r.Post("/sonarr/{id}", handleArrWebhook(st, arr.KindSonarr))
		r.Post("/radarr/{id}", handleArrWebhook(st, arr.KindRadarr))
	})

	r.Handle("/*", spaHandler(assets))

	return r
}
