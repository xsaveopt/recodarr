package metrics

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sratabix/recodarr/internal/handbrake"
	"github.com/sratabix/recodarr/internal/job"
	"github.com/sratabix/recodarr/internal/store"
)

const scrapeTimeout = 5 * time.Second

func Handler(st *store.Store, w *job.Worker, token string) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newCollector(st, w),
	)

	h := promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	})

	if token == "" {
		return h
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			rw.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(rw, r)
	})
}

type collector struct {
	store  *store.Store
	worker *job.Worker

	jobsByStatus *prometheus.Desc
	bytesSaved   *prometheus.Desc
	activeEnc    *prometheus.Desc
	maxParallel  *prometheus.Desc
	windowActive *prometheus.Desc
	paused       *prometheus.Desc
	lastTick     *prometheus.Desc
	hbAvail      *prometheus.Desc
	encodePct    *prometheus.Desc
	encodeFps    *prometheus.Desc
}

func newCollector(st *store.Store, w *job.Worker) *collector {
	return &collector{
		store:  st,
		worker: w,

		jobsByStatus: prometheus.NewDesc(
			"recodarr_jobs",
			"Number of jobs in the queue, partitioned by status.",
			[]string{"status"}, nil,
		),
		bytesSaved: prometheus.NewDesc(
			"recodarr_bytes_saved_total",
			"Total bytes reclaimed by completed encodes (sum of original_size − final_size for status=done).",
			nil, nil,
		),
		activeEnc: prometheus.NewDesc(
			"recodarr_worker_active_encodes",
			"Number of encodes currently running.",
			nil, nil,
		),
		maxParallel: prometheus.NewDesc(
			"recodarr_worker_max_parallel_encodes",
			"Configured upper bound on concurrent encodes.",
			nil, nil,
		),
		windowActive: prometheus.NewDesc(
			"recodarr_worker_window_active",
			"1 if the worker is inside its configured encoding window (or no window is set), 0 if outside.",
			nil, nil,
		),
		paused: prometheus.NewDesc(
			"recodarr_worker_paused",
			"1 if the master encoding-paused switch is on, 0 otherwise. Jobs continue to queue while paused.",
			nil, nil,
		),
		lastTick: prometheus.NewDesc(
			"recodarr_worker_last_tick_timestamp_seconds",
			"Unix timestamp of the worker's most recent tick. 0 if never ticked.",
			nil, nil,
		),
		hbAvail: prometheus.NewDesc(
			"recodarr_handbrake_available",
			"1 if HandBrakeCLI was discovered on PATH at startup, 0 otherwise.",
			nil, nil,
		),
		encodePct: prometheus.NewDesc(
			"recodarr_encode_progress_percent",
			"Percent complete of each in-flight encode (0–100).",
			[]string{"job_id"}, nil,
		),
		encodeFps: prometheus.NewDesc(
			"recodarr_encode_fps",
			"Live frames-per-second for each in-flight encode.",
			[]string{"job_id"}, nil,
		),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.jobsByStatus
	ch <- c.bytesSaved
	ch <- c.activeEnc
	ch <- c.maxParallel
	ch <- c.windowActive
	ch <- c.paused
	ch <- c.lastTick
	ch <- c.hbAvail
	ch <- c.encodePct
	ch <- c.encodeFps
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	if stats, err := c.store.GetJobStats(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.WaitingForSeed), "waiting_for_seed")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.WaitingForHardlink), "waiting_for_hardlink")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.Ready), "ready")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.Encoding), "encoding")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.Done), "done")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.Failed), "failed")
		ch <- prometheus.MustNewConstMetric(c.jobsByStatus, prometheus.GaugeValue, float64(stats.Skipped), "skipped")
		ch <- prometheus.MustNewConstMetric(c.bytesSaved, prometheus.GaugeValue, float64(stats.TotalSavedBytes))
	}

	ch <- prometheus.MustNewConstMetric(c.activeEnc, prometheus.GaugeValue, float64(len(c.worker.EncodingJobIDs())))

	if cfg, err := c.store.LoadAppSettings(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.maxParallel, prometheus.GaugeValue, float64(cfg.MaxParallelEncodes))
		paused := 0.0
		if cfg.EncodingPaused {
			paused = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.paused, prometheus.GaugeValue, paused)
	}

	ws := c.worker.WindowStatus(ctx)
	windowActive := 0.0
	if ws.Active {
		windowActive = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.windowActive, prometheus.GaugeValue, windowActive)

	tick := c.worker.LastTickAt()
	tickTs := 0.0
	if !tick.IsZero() {
		tickTs = float64(tick.Unix())
	}
	ch <- prometheus.MustNewConstMetric(c.lastTick, prometheus.GaugeValue, tickTs)

	hb := 0.0
	if !strings.HasPrefix(handbrake.VersionString(), "(HandBrakeCLI not found)") {
		hb = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.hbAvail, prometheus.GaugeValue, hb)

	for _, p := range c.worker.AllProgress() {
		id := strconv.FormatInt(p.JobID, 10)
		ch <- prometheus.MustNewConstMetric(c.encodePct, prometheus.GaugeValue, p.Percent, id)
		ch <- prometheus.MustNewConstMetric(c.encodeFps, prometheus.GaugeValue, p.FPS, id)
	}
}
