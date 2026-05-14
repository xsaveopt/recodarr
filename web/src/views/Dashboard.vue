<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { RouterLink } from "vue-router";
import Button from "primevue/button";
import Tag from "primevue/tag";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import ProgressBar from "primevue/progressbar";

import { api } from "@/api/client";
import { useEncodeProgress } from "@/composables/useEncodeProgress";
import { useNotify } from "@/composables/useNotify";
import type { Job, JobStats, JobStatus, WorkerStatus } from "@/types/api";

const notify = useNotify();
const stats = ref<JobStats | null>(null);
const recentJobs = ref<Job[]>([]);
const workerStatus = ref<WorkerStatus | null>(null);
let timer: number | null = null;

// When SSE signals an encode finished, optimistically strip the id from our
// local worker-status mirror so the row vanishes immediately, and trigger a
// refresh to reconcile. Without this, the row lingers until the next 10s poll
// because the snapshot still lists the id with its last-known percent.
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
    void load();
  },
});

// Sorted list of in-flight encodes for rendering. Prefer SSE map; fall back to
// the worker-status snapshot if SSE hasn't delivered an event yet (first paint).
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
  return `${ws.encodingJobIds.length} / ${ws.maxParallelEncodes} slot${ws.maxParallelEncodes === 1 ? "" : "s"} in use`;
});

async function load() {
  const res = await notify.tryRun(
    () => Promise.all([api.stats.get(), api.jobs.list(), api.worker.status()]),
    "Couldn't load dashboard",
  );
  if (res) {
    stats.value = res[0];
    recentJobs.value = res[1].slice(0, 10);
    workerStatus.value = res[2];
    // Authoritative active set — drops any stale entries SSE missed.
    prune(res[2].encodingJobIds);
  }
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

function savings(j: Job) {
  if (j.originalSize == null || j.finalSize == null) return "—";
  const pct = Math.round((1 - j.finalSize / j.originalSize) * 100);
  return `${pct}%`;
}

const severities: Record<JobStatus, "info" | "warn" | "success" | "danger" | "secondary"> = {
  waiting_for_seed: "secondary",
  ready: "info",
  encoding: "warn",
  done: "success",
  failed: "danger",
};

onMounted(() => {
  void load();
  timer = window.setInterval(load, 10000);
});
onUnmounted(() => {
  if (timer != null) window.clearInterval(timer);
});
</script>

<template>
  <section>
    <div class="head">
      <h2>Dashboard</h2>
      <Button text icon="pi pi-refresh" label="Refresh" @click="load" />
    </div>
    <div v-if="stats" class="stat-grid">
      <div class="stat-card saved">
        <div class="stat-value">{{ formatBytes(stats.totalSavedBytes) }}</div>
        <div class="stat-label">Total saved</div>
      </div>
      <RouterLink class="stat-card done" :to="{ name: 'jobs', query: { status: 'done' } }">
        <div class="stat-value">{{ stats.done }}</div>
        <div class="stat-label">Done</div>
      </RouterLink>
      <RouterLink class="stat-card encoding" :to="{ name: 'jobs', query: { status: 'encoding' } }">
        <div class="stat-value">{{ stats.encoding }}</div>
        <div class="stat-label">Encoding</div>
      </RouterLink>
      <RouterLink class="stat-card queued" :to="{ name: 'jobs', query: { status: 'ready' } }">
        <div class="stat-value">{{ stats.ready + stats.waitingForSeed }}</div>
        <div class="stat-label">Queued</div>
      </RouterLink>
      <RouterLink class="stat-card failed" :to="{ name: 'jobs', query: { status: 'failed' } }">
        <div class="stat-value">{{ stats.failed }}</div>
        <div class="stat-label">Failed</div>
      </RouterLink>
    </div>

    <div v-if="activeEncodes.length" class="encode-list">
      <div class="encode-list-head">
        <span class="encode-label">Encoding</span>
        <span class="muted small">{{ slotsLabel }}</span>
      </div>
      <div
        v-for="ev in activeEncodes"
        :key="ev.jobId"
        class="encode-card"
      >
        <div class="encode-head">
          <span class="encode-title" :title="ev.title">{{ ev.title }}</span>
          <span class="encode-eta" v-if="ev.eta">ETA {{ ev.eta }}</span>
        </div>
        <ProgressBar :value="Math.round(ev.percent * 10) / 10" />
        <div class="encode-meta">
          <span>{{ ev.percent.toFixed(1) }}%</span>
          <span v-if="ev.fps">{{ ev.fps.toFixed(1) }} fps</span>
          <span class="muted small">job #{{ ev.jobId }}</span>
        </div>
      </div>
    </div>

    <div v-if="workerStatus" class="worker-row">
      <span class="worker-label">Worker</span>
      <span :class="workerStatus.isEncoding ? 'worker-encoding' : 'worker-idle'">
        {{
          workerStatus.isEncoding
            ? `${workerStatus.encodingJobIds.length} encoding`
            : "idle"
        }}
      </span>
      <span
        v-if="workerStatus.window?.hasLimit"
        :class="workerStatus.window.active ? 'window-active' : 'window-paused'"
      >
        window {{ workerStatus.window.start }}–{{ workerStatus.window.end }}
        ({{ workerStatus.window.active ? "active" : "paused" }})
      </span>
      <span class="worker-tick">last tick: {{ relativeTime(workerStatus.lastTickAt) }}</span>
    </div>

    <div
      v-if="stats && stats.done === 0 && stats.encoding === 0 && stats.ready === 0 && stats.waitingForSeed === 0 && stats.failed === 0"
      class="empty-hint"
    >
      <strong>Nothing here yet.</strong>
      Add a Sonarr/Radarr instance and a tag→profile mapping in
      <RouterLink to="/settings">Settings</RouterLink>, then paste the webhook URL into *arr's
      Connect → Webhook page.
    </div>

    <h3>Recent jobs</h3>
    <DataTable :value="recentJobs" stripedRows size="small">
      <template #empty><span class="muted">No jobs yet.</span></template>
      <Column field="title" header="Title" />
      <Column field="arrKind" header="Source" style="width: 7rem">
        <template #body="{ data }">
          <Tag :value="data.arrKind" :severity="data.arrKind === 'sonarr' ? 'info' : 'warn'" />
        </template>
      </Column>
      <Column field="status" header="Status" style="width: 9rem">
        <template #body="{ data }">
          <Tag :value="data.status" :severity="severities[data.status as JobStatus]" />
        </template>
      </Column>
      <Column header="Original" style="width: 7rem">
        <template #body="{ data }">{{ formatBytes(data.originalSize ?? data.fileSize) }}</template>
      </Column>
      <Column header="Final" style="width: 7rem">
        <template #body="{ data }">{{ data.finalSize != null ? formatBytes(data.finalSize) : '—' }}</template>
      </Column>
      <Column header="Saved" style="width: 5rem">
        <template #body="{ data }">{{ savings(data) }}</template>
      </Column>
    </DataTable>
  </section>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  margin-bottom: 1rem;
}
.muted {
  color: var(--app-muted);
}
.stat-grid {
  display: flex;
  gap: 1rem;
  flex-wrap: wrap;
  margin-bottom: 2rem;
}
.stat-card {
  flex: 1;
  min-width: 8rem;
  padding: 1rem 1.25rem;
  border-radius: 8px;
  border: 1px solid var(--app-panel-border);
  background: var(--app-panel-bg);
  text-decoration: none;
  color: inherit;
  display: block;
  transition: transform 0.06s ease, box-shadow 0.1s ease;
}
a.stat-card:hover {
  transform: translateY(-1px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}
.stat-card.saved { border-color: var(--app-stat-saved-border); background: var(--app-stat-saved-bg); }
.stat-card.done  { border-color: var(--app-stat-done-border); background: var(--app-stat-done-bg); }
.stat-card.encoding { border-color: var(--app-stat-encoding-border); background: var(--app-warn-bg); }
.stat-card.queued { border-color: var(--app-stat-queued-border); background: var(--app-row-alt); }
.stat-card.failed { border-color: var(--app-stat-failed-border); background: var(--app-stat-failed-bg); }
.stat-value {
  font-size: 1.8rem;
  font-weight: 700;
  line-height: 1;
  margin-bottom: 0.25rem;
}
.stat-label {
  font-size: 0.8rem;
  color: var(--app-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
h3 {
  margin: 0 0 0.75rem;
  font-size: 1rem;
}
.worker-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--app-row-alt);
  border-radius: 6px;
  font-size: 0.9rem;
}
.worker-label {
  font-weight: 600;
  color: var(--app-muted);
}
.worker-idle {
  color: var(--app-muted);
}
.worker-encoding {
  color: var(--app-warn-fg);
  font-weight: 600;
}
.worker-tick {
  color: var(--app-muted);
  margin-left: auto;
  font-size: 0.8rem;
}
.window-active {
  color: var(--app-ok-fg);
  font-weight: 600;
}
.window-paused {
  color: var(--app-warn-fg);
  font-weight: 600;
}
.empty-hint {
  background: var(--app-warn-bg);
  border: 1px solid var(--app-warn-fg);
  border-radius: 8px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.5rem;
  font-size: 0.9rem;
}
.empty-hint a { color: var(--app-warn-fg); }
.encode-list {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin-bottom: 1.5rem;
}
.encode-list-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 0.25rem;
}
.encode-card {
  border: 1px solid var(--app-warn-fg);
  background: var(--app-warn-bg);
  border-radius: 8px;
  padding: 0.6rem 0.9rem;
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
.encode-head {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  font-size: 0.9rem;
}
.encode-label {
  font-weight: 700;
  color: var(--app-warn-fg);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-size: 0.75rem;
}
.small {
  font-size: 0.78rem;
}
.encode-title {
  font-weight: 600;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.encode-eta {
  color: var(--app-muted);
  font-size: 0.85rem;
}
.encode-meta {
  display: flex;
  justify-content: space-between;
  font-size: 0.8rem;
  color: var(--app-muted);
}
</style>
