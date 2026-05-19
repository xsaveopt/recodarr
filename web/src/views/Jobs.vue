<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import DataTable, { type DataTablePageEvent } from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";
import Button from "primevue/button";
import Checkbox from "primevue/checkbox";
import InputText from "primevue/inputtext";
import Select from "primevue/select";
import MultiSelect from "primevue/multiselect";
import Dialog from "primevue/dialog";
import ProgressBar from "primevue/progressbar";

import { api } from "@/api/client";
import { useEncodeProgress } from "@/composables/useEncodeProgress";
import { useNotify } from "@/composables/useNotify";
import type { Job, JobDebug, JobStatus, Profile } from "@/types/api";

const notify = useNotify();
const { progressByJob } = useEncodeProgress();
const route = useRoute();
const router = useRouter();
const jobs = ref<Job[]>([]);
const totalRecords = ref(0);
const loading = ref(false);
const profiles = ref<Profile[]>([]);
const busy = ref<Set<number>>(new Set());
const selectedJobs = ref<Job[]>([]);
const bulkBusy = ref(false);
// Anchor for shift-click range selection. Holds the id of the last checkbox
// the user toggled WITHOUT shift — shift+click on another row then selects
// (or deselects) every row between the anchor and the new target on the
// current page.
const rangeAnchorId = ref<number | null>(null);

// Statuses the user can pick in the Clear History dialog. Encoding jobs are
// never deletable from here — cancel them first.
const CLEARABLE_STATUSES = ["done", "failed", "skipped", "waiting_for_seed", "ready"] as const;
const clearDialogOpen = ref(false);
const clearStatuses = ref<string[]>(["done", "failed", "skipped"]);

const selectedIds = computed(() => selectedJobs.value.map((j) => j.id));
const selectedDeletable = computed(() =>
  selectedJobs.value.filter((j) => j.status !== "encoding").length,
);
const selectedRetryable = computed(() =>
  selectedJobs.value.filter((j) => j.status === "failed" || j.status === "skipped" || j.status === "done").length,
);
// Profile reassignment skips in-flight encodes — that's the only status where
// changing the profile would be a no-op (current run is already mid-stream).
const selectedEditable = computed(() =>
  selectedJobs.value.filter((j) => j.status !== "encoding").length,
);
const bulkProfileId = ref<number | null>(null);

const bulkProfileOptions = computed(() => [
  { id: 0, name: "— Clear profile —" },
  // Only allow assigning non-deleted profiles. Deleted ones still show up in
  // the read-only profile column for historical jobs but can't be picked here.
  ...profiles.value.filter((p) => !p.deleted).map((p) => ({ id: p.id, name: p.name })),
]);

// All known values — the multi-select defaults to all of them checked, so
// "show everything" is the visual default and the user un-checks to filter out.
const ALL_STATUSES = ["waiting_for_seed", "ready", "encoding", "done", "failed", "skipped"] as const;
const ALL_KINDS = ["sonarr", "radarr"] as const;

function parseList(raw: string | string[] | undefined, allowed: readonly string[]): string[] {
  if (!raw) return [...allowed]; // unset query param → "show everything" preset
  const value = Array.isArray(raw) ? raw.join(",") : raw;
  const picked = value
    .split(",")
    .map((s) => s.trim())
    .filter((s) => allowed.includes(s));
  // Empty after filtering = nothing checked, which the UI represents as
  // status=__none__ in the URL so it round-trips honestly.
  return picked;
}

// URL-bound state so refresh / shareable links survive.
const titleFilter = ref<string>((route.query.q as string) ?? "");
const statusFilter = ref<string[]>(parseList(route.query.status as string | undefined, ALL_STATUSES));
const kindFilter = ref<string[]>(parseList(route.query.kind as string | undefined, ALL_KINDS));
const profileFilter = ref<number | null>(
  route.query.profileId ? Number(route.query.profileId) : null,
);
const pageSize = ref<number>(
  route.query.size ? Math.min(500, Math.max(10, Number(route.query.size))) : 50,
);
const pageOffset = ref<number>(route.query.offset ? Math.max(0, Number(route.query.offset)) : 0);

function compactList(picked: string[], allowed: readonly string[]): string | undefined {
  // All checked = no filter, omit from URL & query.
  if (picked.length === allowed.length) return undefined;
  if (picked.length === 0) return undefined; // 0-of-N is the same as all-of-N to backend; UI shows empty box
  return picked.join(",");
}

function syncURL() {
  router.replace({
    query: {
      ...route.query,
      q: titleFilter.value || undefined,
      status: compactList(statusFilter.value, ALL_STATUSES),
      kind: compactList(kindFilter.value, ALL_KINDS),
      profileId: profileFilter.value || undefined,
      offset: pageOffset.value > 0 ? String(pageOffset.value) : undefined,
      size: pageSize.value !== 50 ? String(pageSize.value) : undefined,
    },
  });
}

// Debounce the free-text search so we don't fire a query per keystroke.
let searchTimer: number | null = null;
watch(titleFilter, () => {
  if (searchTimer != null) window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(() => {
    pageOffset.value = 0;
    void load();
  }, 250);
});
watch([statusFilter, kindFilter, profileFilter], () => {
  pageOffset.value = 0;
  void load();
});
const logJob = ref<Job | null>(null);
const debugJob = ref<Job | null>(null);
const debugInfo = ref<JobDebug | null>(null);
const debugLoading = ref(false);
const debugError = ref<string | null>(null);

async function openDebug(j: Job) {
  debugJob.value = j;
  debugInfo.value = null;
  debugError.value = null;
  await loadDebug(j.id);
}

async function loadDebug(id: number) {
  debugLoading.value = true;
  try {
    debugInfo.value = await api.jobs.debug(id);
    debugError.value = null;
  } catch (e) {
    debugError.value = e instanceof Error ? e.message : String(e);
  } finally {
    debugLoading.value = false;
  }
}
let timer: number | null = null;

const statusOptions = [
  { value: "waiting_for_seed", label: "Waiting for seed" },
  { value: "ready", label: "Ready" },
  { value: "encoding", label: "Encoding" },
  { value: "done", label: "Done" },
  { value: "failed", label: "Failed" },
  { value: "skipped", label: "Skipped (filtered)" },
];

const kindOptions = [
  { value: "sonarr", label: "Sonarr" },
  { value: "radarr", label: "Radarr" },
];

// Hide deleted profiles from the filter dropdown — they can't be picked for new
// work, only viewed in history.
const profileOptions = computed(() => [
  { value: null, label: "All profiles" },
  ...profiles.value
    .filter((p) => !p.deleted)
    .map((p) => ({ value: p.id, label: `${p.name} (${p.encoder})` })),
]);

const profileNameById = computed(() => {
  const m = new Map<number, string>();
  for (const p of profiles.value) {
    m.set(p.id, p.deleted ? `${p.name} (deleted)` : p.name);
  }
  return m;
});

async function load() {
  loading.value = true;
  try {
    const res = await notify.tryRun(
      () =>
        api.jobs.list({
          status: compactList(statusFilter.value, ALL_STATUSES),
          kind: compactList(kindFilter.value, ALL_KINDS),
          profileId: profileFilter.value ?? undefined,
          q: titleFilter.value.trim() || undefined,
          limit: pageSize.value,
          offset: pageOffset.value,
        }),
      "Couldn't load jobs",
    );
    if (res !== undefined) {
      jobs.value = res?.jobs ?? [];
      totalRecords.value = res?.total ?? 0;
    }
    syncURL();
  } finally {
    loading.value = false;
  }
}

function onPage(ev: DataTablePageEvent) {
  pageOffset.value = ev.first;
  pageSize.value = ev.rows;
  void load();
}

async function loadProfiles() {
  // includeDeleted: historical jobs may reference profiles the user has since
  // removed; we still want to render the profile name (with a "(deleted)"
  // suffix) instead of falling back to "#42".
  const list = await notify.tryRun(
    () => api.profiles.list({ includeDeleted: true }),
    "Couldn't load profiles",
  );
  if (list) profiles.value = list;
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
  const j = jobs.value.find((x) => x.id === id);
  const name = j ? `${j.title} (job #${id})` : `job #${id}`;
  notify.confirmDelete({
    name,
    header: "Remove job from history?",
    acceptLabel: "Remove from history",
    message:
      "This removes the queue entry only. The encoded file on disk is NOT touched, and Sonarr/Radarr are not contacted.",
    onAccept: async () => {
      const ok = await withBusy(id, () =>
        notify.tryAct(() => api.jobs.remove(id), `Removed job #${id}`, "Couldn't remove"),
      );
      if (ok) jobs.value = jobs.value.filter((x) => x.id !== id);
    },
  });
}

function openClearDialog() {
  clearStatuses.value = ["done", "failed", "skipped"];
  clearDialogOpen.value = true;
}

async function clearByStatus() {
  if (clearStatuses.value.length === 0) {
    notify.error("Pick at least one status to clear");
    return;
  }
  const picked = [...clearStatuses.value];
  const res = await notify.tryRun(
    () => api.jobs.clearTerminal(picked),
    "Couldn't clear history",
  );
  if (res !== undefined) {
    if (res.deleted === 0) notify.info("Nothing to clear");
    else notify.success(`Cleared ${res.deleted} entry/entries from history`);
    clearDialogOpen.value = false;
    await load();
  }
}

async function bulkDelete() {
  const ids = selectedIds.value;
  if (ids.length === 0) return;
  notify.confirmDelete({
    name: `${selectedDeletable.value} selected job(s)`,
    header: "Remove selected from history?",
    acceptLabel: "Remove from history",
    message:
      "Removes the selected entries from this list. Files on disk are NOT touched, and Sonarr/Radarr are not contacted. Encoding jobs in the selection are skipped — cancel them first.",
    onAccept: async () => {
      bulkBusy.value = true;
      try {
        const res = await notify.tryRun(() => api.jobs.bulkDelete(ids), "Bulk delete failed");
        if (res !== undefined) {
          notify.success(`Removed ${res.deleted} entry/entries from history`);
          selectedJobs.value = [];
          await load();
        }
      } finally {
        bulkBusy.value = false;
      }
    },
  });
}

// Shift-click range select: expand selection from rangeAnchorId to ev.data.id
// (inclusive) on the visible page. Mirrors the typical file-manager pattern —
// click row, shift-click another, every row in between flips to match.
function applyRangeSelection(targetId: number, select: boolean) {
  const anchor = rangeAnchorId.value;
  if (anchor == null || anchor === targetId) return;
  const a = jobs.value.findIndex((j) => j.id === anchor);
  const b = jobs.value.findIndex((j) => j.id === targetId);
  if (a < 0 || b < 0) return;
  const [lo, hi] = a < b ? [a, b] : [b, a];
  const range = jobs.value.slice(lo, hi + 1);
  const rangeIds = new Set(range.map((j) => j.id));
  const others = selectedJobs.value.filter((j) => !rangeIds.has(j.id));
  selectedJobs.value = select ? [...others, ...range] : others;
}

function onRowSelect(ev: { data: Job; originalEvent: Event }) {
  const oe = ev.originalEvent as MouseEvent | KeyboardEvent | undefined;
  if (oe && "shiftKey" in oe && oe.shiftKey) {
    applyRangeSelection(ev.data.id, true);
  } else {
    rangeAnchorId.value = ev.data.id;
  }
}

function onRowUnselect(ev: { data: Job; originalEvent: Event }) {
  const oe = ev.originalEvent as MouseEvent | KeyboardEvent | undefined;
  if (oe && "shiftKey" in oe && oe.shiftKey) {
    applyRangeSelection(ev.data.id, false);
  } else {
    rangeAnchorId.value = ev.data.id;
  }
}

async function bulkSetProfile() {
  const ids = selectedIds.value;
  if (ids.length === 0 || bulkProfileId.value == null) return;
  const pid = bulkProfileId.value;
  const label =
    pid === 0
      ? "Clear profile on selected job(s)?"
      : `Set profile "${profiles.value.find((p) => p.id === pid)?.name ?? pid}" on selected job(s)?`;
  notify.confirmDelete({
    name: `${selectedEditable.value} job(s)`,
    header: label,
    acceptLabel: "Apply",
    message:
      "In-flight encodes in the selection are skipped — cancel and retry them to pick up the new profile. Done jobs get the new profile too; re-queue them to actually re-encode.",
    onAccept: async () => {
      bulkBusy.value = true;
      try {
        const res = await notify.tryRun(
          () => api.jobs.bulkSetProfile(ids, pid),
          "Bulk profile update failed",
        );
        if (res !== undefined) {
          if (res.updated === 0) notify.info("Nothing eligible to update in the selection");
          else notify.success(`Updated profile on ${res.updated} job(s)`);
          bulkProfileId.value = null;
          selectedJobs.value = [];
          await load();
        }
      } finally {
        bulkBusy.value = false;
      }
    },
  });
}

async function bulkRetry() {
  const ids = selectedIds.value;
  if (ids.length === 0) return;
  bulkBusy.value = true;
  try {
    const res = await notify.tryRun(() => api.jobs.bulkRetry(ids), "Bulk retry failed");
    if (res !== undefined) {
      if (res.retried === 0) notify.info("Nothing eligible to retry in the selection");
      else notify.success(`Re-queued ${res.retried} job(s)`);
      selectedJobs.value = [];
      await load();
    }
  } finally {
    bulkBusy.value = false;
  }
}

const severities: Record<JobStatus, "info" | "warn" | "success" | "danger" | "secondary"> = {
  waiting_for_seed: "secondary",
  ready: "info",
  encoding: "warn",
  done: "success",
  failed: "danger",
  skipped: "secondary",
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

function formatDuration(seconds?: number) {
  if (seconds == null) return "—";
  if (seconds < 60) return `${seconds}s`;
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return h > 0 ? `${h}h ${m}m ${s}s` : `${m}m ${s}s`;
}

function savings(j: Job) {
  if (j.originalSize == null || j.finalSize == null) return "—";
  const pct = Math.round((1 - j.finalSize / j.originalSize) * 100);
  return `${pct}%`;
}

function retryTooltip(status: string): string {
  switch (status) {
    case "skipped":
      return "Re-queue (re-runs filters; current profile settings apply)";
    case "done":
      return "Re-encode the current file with the current profile settings (use to test profile changes)";
    default:
      return "Retry";
  }
}

function shortTime(s?: string) {
  if (!s) return "—";
  const diff = Math.floor((Date.now() - new Date(s).getTime()) / 1000);
  if (diff < 5) return "just now";
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

onMounted(() => {
  void loadProfiles();
  void load();
  timer = window.setInterval(load, 5000);
});
onUnmounted(() => {
  if (timer != null) window.clearInterval(timer);
});
</script>

<template>
  <section class="page">
    <div class="head">
      <div>
        <h1 class="page-title">Jobs</h1>
        <p class="page-sub">
          Every encode tracked by Recodarr. The buttons here only manage this list — they never
          touch files on disk or talk to Sonarr/Radarr.
        </p>
      </div>
      <div class="head-actions">
        <Button text icon="pi pi-replay" label="Retry all failed" @click="retryAll" />
        <Button
          text
          severity="danger"
          icon="pi pi-trash"
          label="Clear history…"
          title="Pick which statuses to clear from this list. Files on disk are not touched."
          @click="openClearDialog"
        />
        <Button text icon="pi pi-refresh" label="Refresh" @click="load" />
      </div>
    </div>
    <div class="filters">
      <InputText v-model="titleFilter" placeholder="Search title…" class="filter-input" />
      <MultiSelect
        v-model="statusFilter"
        :options="statusOptions"
        optionLabel="label"
        optionValue="value"
        :maxSelectedLabels="3"
        :selectedItemsLabel="`{0} statuses`"
        placeholder="No statuses selected"
        class="filter-select"
      />
      <MultiSelect
        v-model="kindFilter"
        :options="kindOptions"
        optionLabel="label"
        optionValue="value"
        :maxSelectedLabels="2"
        placeholder="No sources selected"
        class="filter-select filter-select-narrow"
      />
      <Select
        v-model="profileFilter"
        :options="profileOptions"
        optionLabel="label"
        optionValue="value"
        class="filter-select"
        :loading="profiles.length === 0"
        showClear
        placeholder="All profiles"
      />
    </div>

    <div v-if="selectedJobs.length > 0" class="bulk-bar">
      <span class="bulk-count">
        <strong>{{ selectedJobs.length }}</strong> selected
        <span v-if="selectedDeletable < selectedJobs.length" class="muted small">
          · {{ selectedJobs.length - selectedDeletable }} encoding (skipped on delete)
        </span>
      </span>
      <span class="bulk-profile">
        <Select
          v-model="bulkProfileId"
          :options="bulkProfileOptions"
          optionLabel="name"
          optionValue="id"
          placeholder="Set profile…"
          class="bulk-profile-select"
          :disabled="selectedEditable === 0 || bulkBusy"
        />
        <Button
          text
          size="small"
          icon="pi pi-check"
          :label="`Apply to ${selectedEditable}`"
          :disabled="bulkProfileId == null || selectedEditable === 0 || bulkBusy"
          :loading="bulkBusy"
          title="Reassign profile for the selected jobs. Encoding jobs are skipped."
          @click="bulkSetProfile"
        />
      </span>
      <Button
        text
        size="small"
        icon="pi pi-refresh"
        :label="`Retry ${selectedRetryable}`"
        :disabled="selectedRetryable === 0 || bulkBusy"
        :loading="bulkBusy"
        title="Re-queue every failed / skipped / done job in the selection"
        @click="bulkRetry"
      />
      <Button
        text
        size="small"
        severity="danger"
        icon="pi pi-trash"
        :label="`Delete ${selectedDeletable}`"
        :disabled="selectedDeletable === 0 || bulkBusy"
        :loading="bulkBusy"
        title="Remove selected entries from history (files on disk untouched)"
        @click="bulkDelete"
      />
      <Button text size="small" label="Clear selection" @click="selectedJobs = []" />
    </div>

    <DataTable
      v-model:selection="selectedJobs"
      :value="jobs"
      :loading="loading"
      lazy
      paginator
      dataKey="id"
      @row-select="onRowSelect"
      @row-unselect="onRowUnselect"
      :rows="pageSize"
      :first="pageOffset"
      :totalRecords="totalRecords"
      :rowsPerPageOptions="[25, 50, 100, 200]"
      @page="onPage"
      stripedRows
      size="small"
    >
      <template #empty><span class="muted">No jobs match the current filter.</span></template>
      <Column selectionMode="multiple" headerStyle="width: 2.5rem" />
      <Column field="id" header="#" style="width: 4rem" />
      <Column field="title" header="Title" />
      <Column field="arrKind" header="Source" style="width: 9rem">
        <template #body="{ data }">
          <div class="source-cell">
            <Tag :value="data.arrKind" :severity="data.arrKind === 'sonarr' ? 'info' : 'warn'" />
            <Tag
              v-if="data.source === 'backfill'"
              value="backfill"
              severity="secondary"
              title="Manually queued from the Library page"
            />
          </div>
        </template>
      </Column>
      <Column header="Profile" style="width: 10rem">
        <template #body="{ data }">
          <span v-if="data.profileId">{{ profileNameById.get(data.profileId) ?? `#${data.profileId}` }}</span>
          <span v-else class="muted">—</span>
        </template>
      </Column>
      <Column field="status" header="Status" style="width: 12rem">
        <template #body="{ data }">
          <Tag :value="data.status" :severity="severities[data.status as JobStatus]" />
          <div
            v-if="data.status === 'encoding' && progressByJob[data.id]"
            class="row-progress"
            :title="`${progressByJob[data.id].percent.toFixed(1)}% — ETA ${progressByJob[data.id].eta || '?'}`"
          >
            <ProgressBar
              :value="Math.round(progressByJob[data.id].percent * 10) / 10"
              :showValue="false"
              style="height: 4px"
            />
            <span class="row-progress-meta">
              {{ progressByJob[data.id].percent.toFixed(0) }}%
              <span v-if="progressByJob[data.id].eta">· {{ progressByJob[data.id].eta }}</span>
            </span>
          </div>
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
      <Column header="Updated" style="width: 7rem">
        <template #body="{ data }">
          <span :title="new Date(data.updatedAt).toLocaleString()">{{
            shortTime(data.updatedAt)
          }}</span>
        </template>
      </Column>
      <Column header="Error" style="min-width: 10rem">
        <template #body="{ data }">
          <button
            v-if="data.error"
            class="err-msg link"
            type="button"
            @click="logJob = data"
            :title="'Click for full log'"
          >
            {{ data.error }}
          </button>
          <span v-else-if="data.refreshError" class="warn-msg" :title="data.refreshError">
            done, but *arr refresh failed
          </span>
          <span v-else>—</span>
        </template>
      </Column>
      <Column header="" style="width: 10rem">
        <template #body="{ data }">
          <Button
            text
            size="small"
            icon="pi pi-info-circle"
            title="Show diagnostic info (downloadId, qBit lookup, stalled reason)"
            @click="openDebug(data)"
          />
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
            v-if="data.status === 'failed' || data.status === 'skipped' || data.status === 'done'"
            text
            size="small"
            icon="pi pi-refresh"
            :title="retryTooltip(data.status)"
            :loading="busy.has(data.id)"
            @click="retry(data.id)"
          />
          <Button
            v-if="data.status === 'done' || data.status === 'failed' || data.status === 'skipped'"
            text
            size="small"
            severity="danger"
            icon="pi pi-trash"
            title="Remove from history (file on disk is not touched)"
            :loading="busy.has(data.id)"
            @click="remove(data.id)"
          />
        </template>
      </Column>
    </DataTable>

    <Dialog
      v-model:visible="clearDialogOpen"
      modal
      header="Clear history"
      :style="{ width: '32rem', maxWidth: '95vw' }"
    >
      <div class="clear-dialog">
        <p class="muted small">
          Pick which job statuses to remove from this list. Files on disk are NOT touched, and
          Sonarr/Radarr are not contacted. Encoding jobs are never deletable here — cancel them
          first.
        </p>
        <div class="clear-options">
          <label v-for="s in CLEARABLE_STATUSES" :key="s" class="clear-row">
            <Checkbox v-model="clearStatuses" :value="s" />
            <span>{{ s }}</span>
          </label>
        </div>
      </div>
      <template #footer>
        <Button text label="Cancel" @click="clearDialogOpen = false" />
        <Button
          severity="danger"
          icon="pi pi-trash"
          label="Clear selected statuses"
          :disabled="clearStatuses.length === 0"
          @click="clearByStatus"
        />
      </template>
    </Dialog>

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
        <Button label="Close" @click="logJob = null" />
      </template>
    </Dialog>

    <Dialog
      :visible="debugJob !== null"
      @update:visible="(v) => (debugJob = v ? debugJob : null)"
      modal
      :header="debugJob ? `Job #${debugJob.id} — diagnostics` : ''"
      :style="{ width: '48rem', maxWidth: '95vw' }"
    >
      <div v-if="debugJob" class="debug-dialog">
        <div v-if="debugLoading" class="muted">Running live qBit lookup…</div>
        <div v-else-if="debugError" class="log-error">{{ debugError }}</div>
        <template v-else-if="debugInfo">
          <div v-if="debugInfo.stalledReason" class="debug-reason">
            {{ debugInfo.stalledReason }}
          </div>
          <dl class="debug-grid">
            <dt>Status</dt>
            <dd>
              {{ debugInfo.status }}
              <span
                v-if="
                  debugInfo.status === 'waiting_for_seed' &&
                  debugInfo.waitingForSeedCount > debugInfo.seedCheckBatchLimit
                "
                class="log-error inline"
              >
                · {{ debugInfo.waitingForSeedCount }} jobs waiting,
                only {{ debugInfo.seedCheckBatchLimit }} checked per tick — older IDs are processed first
              </span>
            </dd>
            <dt>downloadId</dt>
            <dd>
              <code v-if="debugInfo.downloadId">{{ debugInfo.downloadId }}</code>
              <span v-else class="muted">(empty)</span>
              <span class="muted"> · length {{ debugInfo.downloadIdLength }}</span>
            </dd>
            <dt>File path</dt>
            <dd><code>{{ debugInfo.filePath }}</code></dd>
            <dt>qBit configured</dt>
            <dd>{{ debugInfo.qbit.configured ? "yes" : "no" }}</dd>
            <template v-if="debugInfo.qbit.configured">
              <dt>qBit URL</dt>
              <dd><code>{{ debugInfo.qbit.url }}</code></dd>
              <dt>qBit reachable</dt>
              <dd>
                {{ debugInfo.qbit.reachable ? "yes" : "no" }}
                <span v-if="debugInfo.qbit.loginError" class="log-error inline">
                  · {{ debugInfo.qbit.loginError }}
                </span>
              </dd>
              <template v-if="debugInfo.qbit.reachable">
                <dt>Lookup</dt>
                <dd v-if="debugInfo.qbit.lookupError" class="log-error inline">
                  {{ debugInfo.qbit.lookupError }}
                </dd>
                <dd v-else-if="debugInfo.qbit.lookup?.found">
                  <div><strong>Found.</strong></div>
                  <div>name: <code>{{ debugInfo.qbit.lookup.name }}</code></div>
                  <div>state: <code>{{ debugInfo.qbit.lookup.state }}</code></div>
                  <div>category: <code>{{ debugInfo.qbit.lookup.category || "(none)" }}</code></div>
                  <div>save path: <code>{{ debugInfo.qbit.lookup.savePath }}</code></div>
                  <div>
                    progress:
                    <code>{{ ((debugInfo.qbit.lookup.progress ?? 0) * 100).toFixed(1) }}%</code>
                  </div>
                </dd>
                <dd v-else-if="debugInfo.qbit.lookup">
                  qBit does not have this hash.
                </dd>
                <dd v-else class="muted">(not performed)</dd>
              </template>
            </template>
            <dt>Attempts</dt>
            <dd>{{ debugInfo.attempts }}</dd>
          </dl>
          <template v-if="debugInfo.encode">
            <h4 class="debug-section">Encode</h4>
            <dl class="debug-grid">
              <template v-if="debugInfo.encode.profileName">
                <dt>Profile</dt>
                <dd>
                  {{ debugInfo.encode.profileName }}
                  <span class="muted">· {{ debugInfo.encode.profileEncoder }}</span>
                  <span v-if="debugInfo.encode.profileId" class="muted"> · id {{ debugInfo.encode.profileId }}</span>
                </dd>
              </template>
              <template v-if="debugInfo.encode.originalBytes !== undefined">
                <dt>Original</dt>
                <dd>{{ formatBytes(debugInfo.encode.originalBytes) }}</dd>
              </template>
              <template v-if="debugInfo.encode.finalBytes !== undefined">
                <dt>Final</dt>
                <dd>{{ formatBytes(debugInfo.encode.finalBytes) }}</dd>
              </template>
              <template v-if="debugInfo.encode.savedBytes !== undefined">
                <dt>Saved</dt>
                <dd>
                  <span :class="(debugInfo.encode.savedBytes ?? 0) >= 0 ? '' : 'log-error inline'">
                    {{ formatBytes(Math.abs(debugInfo.encode.savedBytes ?? 0)) }}
                    <span v-if="debugInfo.encode.savedPercent !== undefined">
                      ({{ (debugInfo.encode.savedPercent ?? 0).toFixed(1) }}%)
                    </span>
                    <span v-if="(debugInfo.encode.savedBytes ?? 0) < 0"> larger than source</span>
                  </span>
                </dd>
              </template>
              <template v-if="debugInfo.encode.startedAt">
                <dt>Started</dt>
                <dd>{{ new Date(debugInfo.encode.startedAt).toLocaleString() }}</dd>
              </template>
              <template v-if="debugInfo.encode.finishedAt">
                <dt>Finished</dt>
                <dd>
                  {{ new Date(debugInfo.encode.finishedAt).toLocaleString() }}
                  <span v-if="debugInfo.encode.durationSeconds !== undefined" class="muted">
                    · took {{ formatDuration(debugInfo.encode.durationSeconds) }}
                  </span>
                </dd>
              </template>
              <template v-if="debugInfo.encode.error">
                <dt>Error</dt>
                <dd class="log-error inline">{{ debugInfo.encode.error }}</dd>
              </template>
              <template v-if="debugInfo.encode.refreshError">
                <dt>*arr refresh</dt>
                <dd class="log-error inline">{{ debugInfo.encode.refreshError }}</dd>
              </template>
            </dl>
          </template>
        </template>
      </div>
      <template #footer>
        <Button
          label="Re-run lookup"
          icon="pi pi-refresh"
          :loading="debugLoading"
          @click="debugJob && loadDebug(debugJob.id)"
        />
        <Button label="Close" severity="secondary" @click="debugJob = null" />
      </template>
    </Dialog>
  </section>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
}
.page-title {
  margin: 0;
  font-size: 1.4rem;
  letter-spacing: -0.02em;
}
.page-sub {
  margin: 0.15rem 0 0;
  font-size: 0.85rem;
  color: var(--rc-muted);
}
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
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
.filter-select-narrow {
  width: 10rem;
}
.bulk-profile {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
}
.bulk-profile-select {
  width: 12rem;
}
.bulk-bar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.75rem;
  margin-bottom: 0.5rem;
  background: var(--rc-surface-2);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  font-size: 0.85rem;
}
.bulk-count {
  margin-right: auto;
}
.small {
  font-size: 0.78rem;
}
.clear-dialog {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.clear-options {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.clear-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  font-size: 0.9rem;
}
.clear-row span {
  font-family: var(--rc-font-mono);
  font-size: 0.82rem;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  margin-bottom: 0.75rem;
}
.source-cell {
  display: inline-flex;
  gap: 0.25rem;
  align-items: center;
  flex-wrap: wrap;
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
.row-progress {
  margin-top: 0.3rem;
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.row-progress-meta {
  font-size: 0.7rem;
  color: var(--app-muted);
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
.debug-dialog {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.debug-reason {
  background: var(--app-warn-bg, rgba(255, 200, 0, 0.08));
  color: var(--app-warn-fg);
  padding: 0.6rem 0.8rem;
  border-radius: 4px;
  font-size: 0.9rem;
  line-height: 1.4;
}
.debug-section {
  margin: 0.6rem 0 0.2rem;
  font-size: 0.85rem;
  color: var(--app-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.debug-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.4rem 1rem;
  margin: 0;
  font-size: 0.85rem;
}
.debug-grid dt {
  color: var(--app-muted);
  font-weight: 500;
}
.debug-grid dd {
  margin: 0;
  word-break: break-all;
}
.debug-grid code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  background: var(--app-log-bg);
  padding: 0.05rem 0.3rem;
  border-radius: 3px;
}
.log-error.inline {
  display: inline;
  padding: 0;
  background: none;
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
