<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { RouterLink } from "vue-router";

import { api } from "@/api/client";
import { useEncodeProgress } from "@/composables/useEncodeProgress";
import { useNotify } from "@/composables/useNotify";
import { useWorkerStatus } from "@/composables/useWorkerStatus";
import type { HealthSnapshot, Job, JobStats, JobStatus } from "@/types/api";

const notify = useNotify();
const stats = ref<JobStats | null>(null);
const recentJobs = ref<Job[]>([]);
const { status: workerStatus, refresh: refreshWorker } = useWorkerStatus();
const health = ref<HealthSnapshot | null>(null);
let timer: number | null = null;

const { progressByJob, prune } = useEncodeProgress({
  onComplete: (jobId) => {
    if (workerStatus.value) {
      const ids = workerStatus.value.encodingJobIds.filter((i) => i !== jobId);
      workerStatus.value = {
        ...workerStatus.value,
        encodingJobIds: ids,
        encodingJobId: ids[0] ?? 0,
        isEncoding: ids.length > 0,
        progress: workerStatus.value.progress.filter((p) => p.jobId !== jobId),
      };
    }
    void load(true);
  },
});

const activeEncodes = computed(() => {
  const ids = workerStatus.value?.encodingJobIds ?? [];
  return ids.map((id) => {
    const live = progressByJob.value[id];
    if (live) return live;
    const snap = workerStatus.value?.progress.find((p) => p.jobId === id);
    return snap ?? { jobId: id, title: `job #${id}`, percent: 0, fps: 0, eta: "" };
  });
});

const slotsLabel = computed(() => {
  const ws = workerStatus.value;
  if (!ws) return "";
  return `${ws.encodingJobIds.length} / ${ws.maxParallelEncodes}`;
});

const queuedJobs = computed(() =>
  recentJobs.value.filter((j) => j.status === "ready" || j.status === "waiting_for_seed"),
);

// Each call updates its slice of state as soon as it returns, so the page
// paints progressively instead of blocking on the slowest of the four. Errors
// from any one call surface as a toast but don't kill the rest. Only the first
// load shows toasts on failure — background ticks fail silently so a transient
// blip doesn't spam the user.
async function loadOne<T>(fn: () => Promise<T>, errMsg: string, silent: boolean): Promise<T | null> {
  try {
    return await fn();
  } catch (e) {
    if (!silent) notify.error(e, errMsg);
    return null;
  }
}

async function load(silent = false) {
  await Promise.all([
    loadOne(() => api.stats.get(), "Couldn't load stats", silent).then((r) => {
      if (r) stats.value = r;
    }),
    loadOne(() => api.jobs.list({ limit: 12 }), "Couldn't load recent jobs", silent).then((r) => {
      if (r) recentJobs.value = r.jobs;
    }),
    refreshWorker().then(() => {
      if (workerStatus.value) prune(workerStatus.value.encodingJobIds);
    }),
    loadOne(() => api.status.get(), "Couldn't load health", silent).then((r) => {
      if (r) health.value = r;
    }),
  ]);
}

function relativeTime(iso: string | null): string {
  if (!iso) return "never";
  const diff = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  return `${Math.floor(diff / 3600)}h ago`;
}

function formatBytes(n: number) {
  if (n === 0) return "0 B";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(2)} ${units[i]}`;
}

function statusLabel(s: JobStatus): string {
  return s === "waiting_for_seed" ? "seeding" : s;
}

const queued = computed(() => (stats.value?.ready ?? 0) + (stats.value?.waitingForSeed ?? 0));

const isEmpty = computed(() => {
  const s = stats.value;
  if (!s) return false;
  return (
    s.done === 0 &&
    s.encoding === 0 &&
    s.ready === 0 &&
    s.waitingForSeed === 0 &&
    s.failed === 0 &&
    s.skipped === 0
  );
});

onMounted(() => {
  void load();
  timer = window.setInterval(() => void load(true), 10000);
});
onUnmounted(() => {
  if (timer != null) window.clearInterval(timer);
});
</script>

<template>
  <div class="dash">
    <!-- Health issues — shown only when something is wrong -->
    <section v-if="health && health.issues.length" class="issues">
      <article
        v-for="(iss, idx) in health.issues"
        :key="idx"
        class="issue"
        :class="`issue-${iss.level}`"
      >
        <i
          class="pi issue-icon"
          :class="iss.level === 'error' ? 'pi-exclamation-circle' : 'pi-info-circle'"
        ></i>
        <div class="issue-body">
          <div class="issue-title">{{ iss.title }}</div>
          <div v-if="iss.detail" class="issue-detail">{{ iss.detail }}</div>
        </div>
        <span class="issue-source">{{ iss.source }}</span>
      </article>
    </section>

    <!-- Status strip: worker + window -->
    <div v-if="workerStatus" class="status-strip">
      <span
        class="dot"
        :class="
          workerStatus.paused ? 'dot-paused' : workerStatus.isEncoding ? 'dot-active' : 'dot-idle'
        "
      ></span>
      <span class="strip-text">
        <strong>{{
          workerStatus.paused ? "Paused" : workerStatus.isEncoding ? "Encoding" : "Idle"
        }}</strong>
        <span v-if="!workerStatus.paused && workerStatus.isEncoding" class="muted">
          · {{ workerStatus.encodingJobIds.length }} of {{ workerStatus.maxParallelEncodes }} slots
        </span>
        <span v-else-if="workerStatus.paused" class="muted">· jobs continue to queue</span>
      </span>
      <span
        v-if="workerStatus.window?.hasLimit"
        class="strip-pill"
        :class="workerStatus.window.active ? 'pill-ok' : 'pill-warn'"
      >
        Window {{ workerStatus.window.start }}–{{ workerStatus.window.end }} ·
        {{ workerStatus.window.active ? "active" : "paused" }}
      </span>
      <span
        v-if="health"
        class="strip-pill"
        :class="health.ok ? 'pill-ok' : 'pill-warn'"
        :title="health.ok ? 'All system checks passing' : `${health.issues.length} active issue(s)`"
      >
        <i class="pi" :class="health.ok ? 'pi-check-circle' : 'pi-exclamation-circle'"></i>
        {{ health.ok ? "Healthy" : `${health.issues.length} issue${health.issues.length === 1 ? "" : "s"}` }}
      </span>
      <span class="strip-tick muted tnum"
        >last tick {{ relativeTime(workerStatus.lastTickAt) }}</span
      >
    </div>

    <!-- Active encodes — the hero -->
    <section v-if="activeEncodes.length" class="block">
      <div class="block-head">
        <h2 class="block-title">Active encode<span v-if="activeEncodes.length > 1">s</span></h2>
        <span class="block-meta tnum">{{ slotsLabel }}</span>
      </div>
      <div class="encodes">
        <article v-for="ev in activeEncodes" :key="ev.jobId" class="encode">
          <div class="encode-row">
            <span class="encode-title" :title="ev.title">{{ ev.title }}</span>
            <span class="encode-pct tnum">{{ ev.percent.toFixed(1) }}%</span>
          </div>
          <div class="bar">
            <div class="bar-fill" :style="{ width: `${Math.min(100, ev.percent)}%` }"></div>
          </div>
          <div class="encode-meta muted tnum">
            <span v-if="ev.fps">{{ ev.fps.toFixed(1) }} fps</span>
            <span v-if="ev.eta">ETA {{ ev.eta }}</span>
            <span class="dim">job #{{ ev.jobId }}</span>
          </div>
        </article>
      </div>
    </section>

    <!-- Stats grid: 5 minimal tiles, no color-coding noise -->
    <section v-if="stats" class="stats">
      <div class="stat">
        <div class="stat-label">Total saved</div>
        <div class="stat-value tnum">{{ formatBytes(stats.totalSavedBytes) }}</div>
      </div>
      <RouterLink class="stat" :to="{ name: 'jobs', query: { status: 'done' } }">
        <div class="stat-label">Done</div>
        <div class="stat-value tnum">{{ stats.done }}</div>
      </RouterLink>
      <RouterLink class="stat" :to="{ name: 'jobs', query: { status: 'encoding' } }">
        <div class="stat-label">Encoding</div>
        <div class="stat-value tnum">
          {{ stats.encoding }}
          <span v-if="stats.encoding" class="stat-pulse"></span>
        </div>
      </RouterLink>
      <RouterLink class="stat" :to="{ name: 'jobs', query: { status: 'ready' } }">
        <div class="stat-label">Queued</div>
        <div class="stat-value tnum">{{ queued }}</div>
      </RouterLink>
      <RouterLink class="stat" :to="{ name: 'jobs', query: { status: 'failed' } }">
        <div class="stat-label">Failed</div>
        <div class="stat-value tnum" :class="{ 'stat-bad': stats.failed > 0 }">
          {{ stats.failed }}
        </div>
      </RouterLink>
      <RouterLink class="stat" :to="{ name: 'jobs', query: { status: 'skipped' } }">
        <div class="stat-label">Skipped</div>
        <div class="stat-value tnum">{{ stats.skipped }}</div>
      </RouterLink>
    </section>

    <!-- Two-column body: queue + recent -->
    <div class="grid">
      <section class="block">
        <div class="block-head">
          <h2 class="block-title">Queue</h2>
          <RouterLink :to="{ name: 'jobs' }" class="block-link">All jobs →</RouterLink>
        </div>
        <ul v-if="queuedJobs.length" class="list">
          <li v-for="j in queuedJobs" :key="j.id" class="list-row">
            <span class="row-marker" :class="`marker-${j.status}`"></span>
            <span class="row-title" :title="j.title">{{ j.title }}</span>
            <span class="row-status muted">{{ statusLabel(j.status) }}</span>
          </li>
        </ul>
        <p v-else class="empty">Nothing queued.</p>
      </section>

      <section class="block">
        <div class="block-head">
          <h2 class="block-title">Recent activity</h2>
        </div>
        <ul class="list">
          <li v-for="j in recentJobs" :key="j.id" class="list-row">
            <span class="row-marker" :class="`marker-${j.status}`"></span>
            <span class="row-title" :title="j.title">{{ j.title }}</span>
            <span v-if="j.finalSize != null && j.originalSize != null" class="row-saved tnum muted">
              −{{ Math.round((1 - j.finalSize / j.originalSize) * 100) }}%
            </span>
            <span v-else class="row-saved muted">{{ statusLabel(j.status) }}</span>
            <span class="row-time tnum muted">{{ relativeTime(j.updatedAt) }}</span>
          </li>
        </ul>
      </section>
    </div>

    <!-- Empty state -->
    <div v-if="isEmpty" class="empty-card">
      <h3>Welcome to Recodarr.</h3>
      <p class="muted">
        To get started, add a Sonarr or Radarr instance and a tag → profile mapping in
        <RouterLink to="/settings">Settings</RouterLink>, then paste the webhook URL into *arr's
        <em>Connect → Webhook</em> page.
      </p>
    </div>
  </div>
</template>

<style scoped>
.dash {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
}
.muted {
  color: var(--rc-muted);
}
.dim {
  color: var(--rc-faint);
}

/* Health issues */
.issues {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.issue {
  display: flex;
  align-items: flex-start;
  gap: 0.7rem;
  padding: 0.65rem 0.85rem;
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  background: var(--rc-surface);
  font-size: 0.85rem;
}
.issue-error {
  border-color: color-mix(in srgb, var(--rc-danger) 35%, var(--rc-border));
  background: var(--rc-danger-soft, color-mix(in srgb, var(--rc-danger) 8%, var(--rc-surface)));
}
.issue-warn {
  border-color: color-mix(in srgb, var(--rc-warn) 35%, var(--rc-border));
  background: var(--rc-warn-soft);
}
.issue-icon {
  flex-shrink: 0;
  margin-top: 2px;
  font-size: 1rem;
}
.issue-error .issue-icon {
  color: var(--rc-danger);
}
.issue-warn .issue-icon {
  color: var(--rc-warn);
}
.issue-body {
  flex: 1;
  min-width: 0;
}
.issue-title {
  font-weight: 600;
  color: var(--rc-fg);
}
.issue-detail {
  margin-top: 0.2rem;
  color: var(--rc-fg-2);
  font-size: 0.8rem;
  line-height: 1.4;
}
.issue-source {
  font-size: 0.7rem;
  color: var(--rc-muted);
  font-family: var(--rc-mono, monospace);
  text-transform: lowercase;
  flex-shrink: 0;
  padding: 0.1rem 0.4rem;
  border-radius: var(--rc-r-sm);
  background: color-mix(in srgb, var(--rc-fg) 6%, transparent);
}

/* Status strip */
.status-strip {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  font-size: 0.825rem;
  padding: 0.35rem 0.65rem;
  border: 1px solid var(--rc-border);
  background: var(--rc-surface);
  border-radius: var(--rc-r-md);
  color: var(--rc-fg-2);
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  flex-shrink: 0;
}
.dot-idle {
  background: var(--rc-faint);
}
.dot-paused {
  background: var(--rc-warn);
}
.dot-active {
  background: var(--rc-ok);
  box-shadow: 0 0 0 0 rgba(74, 222, 128, 0.5);
  animation: pulse 1.6s cubic-bezier(0, 0, 0.2, 1) infinite;
}
@keyframes pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(74, 222, 128, 0.45);
  }
  70% {
    box-shadow: 0 0 0 8px rgba(74, 222, 128, 0);
  }
  100% {
    box-shadow: 0 0 0 0 rgba(74, 222, 128, 0);
  }
}
.strip-text strong {
  font-weight: 600;
}
.strip-tick {
  margin-left: auto;
  font-size: 0.78rem;
}
.strip-pill {
  font-size: 0.72rem;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  border: 1px solid var(--rc-border);
}
.pill-ok {
  color: var(--rc-ok);
  background: var(--rc-ok-soft);
  border-color: transparent;
}
.pill-warn {
  color: var(--rc-warn);
  background: var(--rc-warn-soft);
  border-color: transparent;
}

/* Block (card) primitive */
.block {
  background: var(--rc-surface);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-lg);
  padding: 0.875rem 1rem 1rem;
}
.block-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 0.65rem;
}
.block-title {
  margin: 0;
  font-size: 0.78rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--rc-muted);
}
.block-meta {
  font-size: 0.78rem;
  color: var(--rc-muted);
}
.block-link {
  font-size: 0.78rem;
  color: var(--rc-muted);
}
.block-link:hover {
  color: var(--rc-fg);
  text-decoration: none;
}

/* Active encodes */
.encodes {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
}
.encode {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
.encode-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
}
.encode-title {
  font-size: 0.95rem;
  font-weight: 500;
  color: var(--rc-fg);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.encode-pct {
  font-size: 1.05rem;
  font-weight: 600;
  color: var(--rc-fg);
}
.bar {
  height: 4px;
  background: var(--rc-surface-2);
  border-radius: 999px;
  overflow: hidden;
}
.bar-fill {
  height: 100%;
  background: linear-gradient(90deg, var(--rc-accent) 0%, var(--rc-accent) 100%);
  transition: width 0.4s ease;
}
.encode-meta {
  display: flex;
  gap: 0.85rem;
  font-size: 0.78rem;
}

/* Stats */
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 0.5rem;
}
.stat {
  background: var(--rc-surface);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  padding: 0.7rem 0.85rem;
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  text-decoration: none;
  color: inherit;
  transition:
    border-color 0.08s ease,
    background 0.08s ease;
}
a.stat:hover {
  border-color: var(--rc-border-strong);
  background: var(--rc-surface-2);
  text-decoration: none;
}
.stat-label {
  font-size: 0.72rem;
  color: var(--rc-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.stat-value {
  font-size: 1.4rem;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--rc-fg);
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
}
.stat-bad {
  color: var(--rc-danger);
}
.stat-pulse {
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--rc-warn);
  animation: pulse-soft 1.4s ease infinite;
}
@keyframes pulse-soft {
  0%,
  100% {
    opacity: 0.4;
  }
  50% {
    opacity: 1;
  }
}

/* Two-column grid */
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.85rem;
}
@media (max-width: 880px) {
  .grid {
    grid-template-columns: 1fr;
  }
}

/* List rows */
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.list-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.45rem 0;
  font-size: 0.825rem;
  border-bottom: 1px solid var(--rc-border);
}
.list-row:last-child {
  border-bottom: none;
}
.row-marker {
  width: 6px;
  height: 6px;
  border-radius: 999px;
  background: var(--rc-faint);
  flex-shrink: 0;
}
.marker-ready {
  background: var(--rc-info);
}
.marker-waiting_for_seed {
  background: var(--rc-faint);
}
.marker-encoding {
  background: var(--rc-warn);
}
.marker-done {
  background: var(--rc-ok);
}
.marker-failed {
  background: var(--rc-danger);
}
.marker-skipped {
  background: transparent;
  border: 1.5px solid var(--rc-faint);
}
.row-title {
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--rc-fg);
}
.row-status,
.row-saved {
  font-size: 0.78rem;
}
.row-time {
  font-size: 0.78rem;
  min-width: 4.5rem;
  text-align: right;
}
.empty {
  margin: 0;
  padding: 0.5rem 0;
  font-size: 0.85rem;
  color: var(--rc-muted);
}

/* First-run welcome */
.empty-card {
  background: var(--rc-surface);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-lg);
  padding: 1.25rem 1.5rem;
}
.empty-card h3 {
  margin: 0 0 0.4rem;
  font-size: 1rem;
}
.empty-card p {
  margin: 0;
  font-size: 0.875rem;
}
</style>
