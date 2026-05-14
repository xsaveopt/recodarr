<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Select from "primevue/select";
import Dialog from "primevue/dialog";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { Job, JobStatus } from "@/types/api";

const notify = useNotify();
const jobs = ref<Job[]>([]);
const busy = ref<Set<number>>(new Set());
const titleFilter = ref("");
const statusFilter = ref<string>("");
const logJob = ref<Job | null>(null);

async function copyLog() {
  if (!logJob.value) return;
  const text = `Job #${logJob.value.id} — ${logJob.value.title}\n\nError: ${logJob.value.error || "(none)"}\n\nLog:\n${logJob.value.encodeLog || "(empty)"}`;
  try {
    await navigator.clipboard.writeText(text);
    notify.success("Log copied");
  } catch {
    notify.info("Clipboard blocked — select manually");
  }
}
let timer: number | null = null;

const statusOptions = [
  { value: "", label: "All statuses" },
  { value: "waiting_for_seed", label: "Waiting for seed" },
  { value: "ready", label: "Ready" },
  { value: "encoding", label: "Encoding" },
  { value: "done", label: "Done" },
  { value: "failed", label: "Failed" },
];

const filteredJobs = computed(() => {
  let result = jobs.value;
  if (statusFilter.value) {
    result = result.filter((j) => j.status === statusFilter.value);
  }
  if (titleFilter.value.trim()) {
    const q = titleFilter.value.trim().toLowerCase();
    result = result.filter((j) => j.title.toLowerCase().includes(q));
  }
  return result;
});

async function load() {
  const res = await notify.tryRun(() => api.jobs.list(), "Couldn't load jobs");
  if (res !== undefined) jobs.value = res ?? [];
}

async function withBusy<T>(id: number, fn: () => Promise<T>) {
  busy.value = new Set([...busy.value, id]);
  try {
    return await fn();
  } finally {
    busy.value.delete(id);
    busy.value = new Set(busy.value);
  }
}

async function retry(id: number) {
  const ok = await withBusy(id, () => notify.tryRun(() => api.jobs.retry(id), "Retry failed"));
  if (ok !== undefined) {
    notify.success(`Re-queued job #${id}`);
    await load();
  }
}

async function retryAll() {
  const res = await notify.tryRun(() => api.jobs.retryAllFailed(), "Couldn't retry");
  if (res) {
    if (res.retried === 0) notify.info("No failed jobs to retry");
    else notify.success(`Re-queued ${res.retried} job(s)`);
    await load();
  }
}

async function cancel(id: number) {
  const ok = await withBusy(id, () => notify.tryRun(() => api.jobs.cancel(id), "Cancel failed"));
  if (ok !== undefined) {
    notify.success(`Cancelled job #${id}`);
    await load();
  }
}

function remove(id: number) {
  notify.confirmDelete({
    name: `job #${id}`,
    onAccept: async () => {
      const ok = await withBusy(id, () => notify.tryRun(() => api.jobs.remove(id), "Couldn't delete"));
      if (ok !== undefined) jobs.value = jobs.value.filter((j) => j.id !== id);
    },
  });
}

function clearAll() {
  notify.confirmDelete({
    name: "all done and failed jobs",
    onAccept: async () => {
      const res = await notify.tryRun(() => api.jobs.clearTerminal(), "Couldn't clear");
      if (res) {
        if (res.deleted === 0) notify.info("Nothing to delete");
        else notify.success(`Deleted ${res.deleted} job(s)`);
        await load();
      }
    },
  });
}

const severities: Record<JobStatus, "info" | "warn" | "success" | "danger" | "secondary"> = {
  waiting_for_seed: "secondary",
  ready: "info",
  encoding: "warn",
  done: "success",
  failed: "danger",
};

function formatBytes(n?: number) {
  if (n == null) return "—";
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

function shortTime(s?: string) {
  if (!s) return "—";
  return new Date(s).toLocaleString();
}

onMounted(() => {
  void load();
  timer = window.setInterval(load, 5000);
});
onUnmounted(() => {
  if (timer != null) window.clearInterval(timer);
});
</script>

<template>
  <section>
    <div class="head">
      <h2>Jobs</h2>
      <div class="head-actions">
        <Button text icon="pi pi-replay" label="Retry all failed" @click="retryAll" />
        <Button text severity="danger" icon="pi pi-trash" label="Clear done/failed" @click="clearAll" />
        <Button text icon="pi pi-refresh" label="Refresh" @click="load" />
      </div>
    </div>
    <div class="filters">
      <InputText v-model="titleFilter" placeholder="Search title…" class="filter-input" />
      <Select
        v-model="statusFilter"
        :options="statusOptions"
        optionLabel="label"
        optionValue="value"
        class="filter-select"
      />
    </div>

    <DataTable :value="filteredJobs" stripedRows size="small">
      <template #empty><span class="muted">No jobs match the current filter.</span></template>
      <Column field="id" header="#" style="width: 4rem" />
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
        <template #body="{ data }">{{ formatBytes(data.finalSize) }}</template>
      </Column>
      <Column header="Saved" style="width: 5rem">
        <template #body="{ data }">{{ savings(data) }}</template>
      </Column>
      <Column header="Updated" style="width: 10rem">
        <template #body="{ data }">{{ shortTime(data.updatedAt) }}</template>
      </Column>
      <Column header="Error" style="min-width: 10rem">
        <template #body="{ data }">
          <button
            v-if="data.error"
            class="err-msg link"
            type="button"
            @click="logJob = data"
            :title="'Click for full log'"
          >{{ data.error }}</button>
          <span v-else-if="data.refreshError" class="warn-msg" :title="data.refreshError">
            done, but *arr refresh failed
          </span>
          <span v-else>—</span>
        </template>
      </Column>
      <Column header="" style="width: 8rem">
        <template #body="{ data }">
          <Button
            v-if="data.status === 'encoding'"
            text
            size="small"
            severity="warn"
            icon="pi pi-stop-circle"
            title="Cancel"
            :loading="busy.has(data.id)"
            @click="cancel(data.id)"
          />
          <Button
            v-if="data.status === 'failed'"
            text
            size="small"
            icon="pi pi-refresh"
            title="Retry"
            :loading="busy.has(data.id)"
            @click="retry(data.id)"
          />
          <Button
            v-if="data.status === 'done' || data.status === 'failed'"
            text
            size="small"
            severity="danger"
            icon="pi pi-trash"
            title="Delete"
            :loading="busy.has(data.id)"
            @click="remove(data.id)"
          />
        </template>
      </Column>
    </DataTable>

    <Dialog
      :visible="logJob !== null"
      @update:visible="(v) => (logJob = v ? logJob : null)"
      modal
      :header="logJob ? `Job #${logJob.id} — ${logJob.title}` : ''"
      :style="{ width: '60rem', maxWidth: '95vw' }"
    >
      <div v-if="logJob" class="log-dialog">
        <div class="log-meta">
          <span><strong>Status:</strong> {{ logJob.status }}</span>
          <span><strong>Attempts:</strong> {{ logJob.attempts }}</span>
        </div>
        <div v-if="logJob.error" class="log-error">{{ logJob.error }}</div>
        <pre class="log-pre">{{ logJob.encodeLog || "(no captured output)" }}</pre>
      </div>
      <template #footer>
        <Button text label="Copy" icon="pi pi-copy" @click="copyLog" />
        <Button label="Close" @click="logJob = null" />
      </template>
    </Dialog>
  </section>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.5rem;
}
.head-actions {
  display: flex;
  gap: 0.25rem;
}
.filters {
  display: flex;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
  align-items: center;
}
.filter-input {
  width: 16rem;
}
.filter-select {
  width: 14rem;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  margin-bottom: 0.75rem;
}
.muted {
  color: var(--app-muted);
}
.err-msg {
  color: var(--app-error-fg);
  max-width: 18rem;
  display: inline-block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
  cursor: pointer;
}
.err-msg.link {
  background: none;
  border: none;
  padding: 0;
  font: inherit;
  text-decoration: underline dotted;
}
.warn-msg {
  color: var(--app-warn-fg);
  font-size: 0.85rem;
  cursor: help;
}
.log-dialog {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.log-meta {
  display: flex;
  gap: 1.5rem;
  font-size: 0.9rem;
  color: var(--app-muted);
}
.log-error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
}
.log-pre {
  background: var(--app-log-bg);
  color: var(--app-log-fg);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  padding: 0.75rem;
  border-radius: 6px;
  max-height: 60vh;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
}
</style>
