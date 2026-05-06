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
const { progress } = useEncodeProgress();
const isEncoding = computed(() => progress.value.jobId !== 0);

const stats = ref<JobStats | null>(null);
const recentJobs = ref<Job[]>([]);
const workerStatus = ref<WorkerStatus | null>(null);
let timer: number | null = null;

async function load() {
  const res = await notify.tryRun(
    () => Promise.all([api.stats.get(), api.jobs.list(), api.worker.status()]),
    "Couldn't load dashboard",
  );
  if (res) {
    stats.value = res[0];
    recentJobs.value = res[1].slice(0, 10);
    workerStatus.value = res[2];
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
      <div class="stat-card done">
        <div class="stat-value">{{ stats.done }}</div>
        <div class="stat-label">Done</div>
      </div>
      <div class="stat-card encoding">
        <div class="stat-value">{{ stats.encoding }}</div>
        <div class="stat-label">Encoding</div>
      </div>
      <div class="stat-card queued">
        <div class="stat-value">{{ stats.ready + stats.waitingForSeed }}</div>
        <div class="stat-label">Queued</div>
      </div>
      <div class="stat-card failed">
        <div class="stat-value">{{ stats.failed }}</div>
        <div class="stat-label">Failed</div>
      </div>
    </div>

    <div v-if="isEncoding" class="encode-card">
      <div class="encode-head">
        <span class="encode-label">Encoding</span>
        <span class="encode-title">{{ progress.title }}</span>
        <span class="encode-eta" v-if="progress.eta">ETA {{ progress.eta }}</span>
      </div>
      <ProgressBar :value="Math.round(progress.percent * 10) / 10" />
      <div class="encode-meta">
        <span>{{ progress.percent.toFixed(1) }}%</span>
        <span v-if="progress.fps">{{ progress.fps.toFixed(1) }} fps</span>
      </div>
    </div>

    <div v-if="workerStatus" class="worker-row">
      <span class="worker-label">Worker</span>
      <span :class="workerStatus.isEncoding ? 'worker-encoding' : 'worker-idle'">
        {{ workerStatus.isEncoding ? `encoding job #${workerStatus.encodingJobId}` : "idle" }}
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
  background: #fee;
  color: #900;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  margin-bottom: 1rem;
}
.muted {
  color: #888;
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
  border: 1px solid #e0e0e0;
  background: #fafafa;
}
.stat-card.saved { border-color: #a0c4ff; background: #e8f4fd; }
.stat-card.done  { border-color: #b5e7a0; background: #eefbe7; }
.stat-card.encoding { border-color: #ffe082; background: #fffde7; }
.stat-card.queued { border-color: #cfd8dc; background: #f5f5f5; }
.stat-card.failed { border-color: #ffb3b3; background: #fff0f0; }
.stat-value {
  font-size: 1.8rem;
  font-weight: 700;
  line-height: 1;
  margin-bottom: 0.25rem;
}
.stat-label {
  font-size: 0.8rem;
  color: #888;
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
  background: #f5f5f5;
  border-radius: 6px;
  font-size: 0.9rem;
}
.worker-label {
  font-weight: 600;
  color: #555;
}
.worker-idle {
  color: #666;
}
.worker-encoding {
  color: #b45309;
  font-weight: 600;
}
.worker-tick {
  color: #999;
  margin-left: auto;
  font-size: 0.8rem;
}
.window-active {
  color: #2e7d32;
  font-weight: 600;
}
.window-paused {
  color: #b45309;
  font-weight: 600;
}
.empty-hint {
  background: #fffbe6;
  border: 1px solid #ffe58f;
  border-radius: 8px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.5rem;
  font-size: 0.9rem;
}
.empty-hint a { color: #b45309; }
.encode-card {
  border: 1px solid #ffe082;
  background: #fffde7;
  border-radius: 8px;
  padding: 0.75rem 1rem;
  margin-bottom: 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.encode-head {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  font-size: 0.9rem;
}
.encode-label {
  font-weight: 700;
  color: #b45309;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  font-size: 0.75rem;
}
.encode-title {
  font-weight: 600;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.encode-eta {
  color: #777;
  font-size: 0.85rem;
}
.encode-meta {
  display: flex;
  justify-content: space-between;
  font-size: 0.8rem;
  color: #666;
}
</style>
