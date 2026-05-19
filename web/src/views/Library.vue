<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Checkbox from "primevue/checkbox";
import Dialog from "primevue/dialog";
import ProgressSpinner from "primevue/progressspinner";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { ArrInstance, LibraryItem } from "@/types/api";

const notify = useNotify();

const instances = ref<ArrInstance[]>([]);
const activeInstanceId = ref<number | null>(null);
const items = ref<LibraryItem[]>([]);
const noMappings = ref(false);
const loading = ref(false);
const loadError = ref("");
const selected = ref<LibraryItem[]>([]);
const titleFilter = ref("");
const hideProcessed = ref(true);
const queuing = ref(false);

const confirmOpen = ref(false);
// Soft cap: any single click that would create more than this many jobs prompts first.
const QUEUE_CONFIRM_THRESHOLD = 100;

const activeInstance = computed(() =>
  instances.value.find((i) => i.id === activeInstanceId.value) ?? null,
);
const eligibleInstances = computed(() =>
  instances.value.filter((i) => i.enabled && (i.kind === "sonarr" || i.kind === "radarr")),
);

const filteredItems = computed(() => {
  const needle = titleFilter.value.trim().toLowerCase();
  return items.value.filter((it) => {
    // "Already known" = either completed or still queued/encoding. Both should
    // hide by default since queueing them again would just be a no-op (the
    // backend's HasActiveJob check would skip the insert).
    if (hideProcessed.value && (it.doneJobs > 0 || it.activeJobs > 0)) return false;
    if (needle && !it.title.toLowerCase().includes(needle)) return false;
    return true;
  });
});

const selectedFileCount = computed(() =>
  selected.value.reduce((acc, it) => acc + it.fileCount, 0),
);

async function loadInstances() {
  const list = await notify.tryRun(() => api.arr.list(), "Couldn't load *arr instances");
  if (!list) return;
  instances.value = list;
  if (activeInstanceId.value == null && eligibleInstances.value.length > 0) {
    activeInstanceId.value = eligibleInstances.value[0].id;
  }
}

async function loadLibrary() {
  if (activeInstanceId.value == null) {
    items.value = [];
    noMappings.value = false;
    return;
  }
  loading.value = true;
  loadError.value = "";
  selected.value = [];
  try {
    const res = await api.arr.library(activeInstanceId.value);
    items.value = res.items;
    noMappings.value = res.noMappings;
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err);
    items.value = [];
  } finally {
    loading.value = false;
  }
}

function selectInstance(id: number) {
  activeInstanceId.value = id;
}

watch(activeInstanceId, () => {
  loadLibrary();
});

async function doQueue() {
  if (activeInstanceId.value == null || selected.value.length === 0) return;
  queuing.value = true;
  try {
    const res = await api.arr.queueLibrary(
      activeInstanceId.value,
      selected.value.map((it) => it.itemId),
    );
    const parts = [`${res.inserted} job${res.inserted === 1 ? "" : "s"} queued`];
    if (res.skipped > 0) parts.push(`${res.skipped} skipped`);
    notify.success(parts.join(", "));
    if (res.errors && res.errors.length > 0) {
      notify.info(
        `${res.errors.length} item${res.errors.length === 1 ? "" : "s"} had errors — see logs`,
      );
    }
    selected.value = [];
    await loadLibrary();
  } catch (err) {
    notify.error(err instanceof Error ? err.message : "Queue failed");
  } finally {
    queuing.value = false;
    confirmOpen.value = false;
  }
}

function onQueueClick() {
  if (selectedFileCount.value > QUEUE_CONFIRM_THRESHOLD) {
    confirmOpen.value = true;
    return;
  }
  doQueue();
}

function formatBytes(n: number) {
  if (!Number.isFinite(n) || n <= 0) return "—";
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

onMounted(async () => {
  await loadInstances();
  await loadLibrary();
});
</script>

<template>
  <section class="library">
    <header class="page-head">
      <h1>Library</h1>
      <p class="muted">
        Re-encode items already in your *arr libraries that pre-date their tag. Uses the
        same tag→profile mappings as webhooks.
      </p>
    </header>

    <div v-if="eligibleInstances.length === 0" class="empty">
      No enabled Sonarr or Radarr instances. Add one in
      <RouterLink to="/settings">Settings</RouterLink>.
    </div>

    <template v-else>
      <div class="tabs">
        <button
          v-for="inst in eligibleInstances"
          :key="inst.id"
          class="tab"
          :class="{ active: inst.id === activeInstanceId }"
          type="button"
          @click="selectInstance(inst.id)"
        >
          <span class="tab-name">{{ inst.name }}</span>
          <Tag :value="inst.kind" severity="info" />
        </button>
      </div>

      <div class="toolbar">
        <InputText
          v-model="titleFilter"
          placeholder="Filter by title"
          class="filter"
        />
        <label class="checkbox-row">
          <Checkbox v-model="hideProcessed" :binary="true" />
          <span>Hide items already queued or completed</span>
        </label>
        <Button
          icon="pi pi-refresh"
          severity="secondary"
          text
          label="Refresh"
          :disabled="loading"
          @click="loadLibrary"
        />
        <span class="spacer"></span>
        <Button
          icon="pi pi-play"
          :label="selected.length === 0
            ? 'Queue selected'
            : `Queue ${selected.length} (${selectedFileCount} file${selectedFileCount === 1 ? '' : 's'})`"
          :disabled="selected.length === 0 || queuing"
          :loading="queuing"
          @click="onQueueClick"
        />
      </div>

      <div v-if="loading" class="loading">
        <ProgressSpinner style="width: 32px; height: 32px" />
      </div>
      <div v-else-if="loadError" class="error">{{ loadError }}</div>
      <div v-else-if="noMappings" class="empty">
        This instance kind has no tag→profile mappings.
        <RouterLink to="/settings">Add one in Settings</RouterLink>
        so Recodarr knows which profile to apply.
      </div>
      <div v-else-if="filteredItems.length === 0" class="empty">
        <template v-if="items.length === 0">
          No tagged items in this library. Tag a series/movie in
          {{ activeInstance?.kind === "sonarr" ? "Sonarr" : "Radarr" }} that matches one of
          your mappings, then refresh.
        </template>
        <template v-else>
          All tagged items are already queued or processed. Uncheck the filter to re-queue.
        </template>
      </div>
      <DataTable
        v-else
        v-model:selection="selected"
        :value="filteredItems"
        dataKey="itemId"
        stripedRows
        size="small"
        scrollable
        scrollHeight="60vh"
      >
        <Column selectionMode="multiple" headerStyle="width: 3rem" />
        <Column field="title" header="Title" sortable>
          <template #body="{ data }">
            <div class="title-cell">
              <div class="title-main">{{ data.title }}</div>
              <div class="title-path muted">{{ data.path }}</div>
            </div>
          </template>
        </Column>
        <Column field="tagLabel" header="Tag" sortable style="width: 10rem">
          <template #body="{ data }">
            <Tag :value="data.tagLabel" severity="secondary" />
          </template>
        </Column>
        <Column field="profileName" header="Profile" sortable style="width: 12rem" />
        <Column field="fileCount" header="Files" sortable style="width: 6rem" />
        <Column field="totalSize" header="Size" sortable style="width: 8rem">
          <template #body="{ data }">{{ formatBytes(data.totalSize) }}</template>
        </Column>
        <Column header="Jobs" style="width: 9rem">
          <template #body="{ data }">
            <div class="badges">
              <Tag v-if="data.activeJobs > 0" :value="`${data.activeJobs} pending`" severity="warn" />
              <Tag v-if="data.doneJobs > 0" :value="`${data.doneJobs} done`" severity="success" />
              <span v-if="data.activeJobs === 0 && data.doneJobs === 0" class="muted">—</span>
            </div>
          </template>
        </Column>
      </DataTable>
    </template>

    <Dialog
      v-model:visible="confirmOpen"
      modal
      header="Queue a lot of jobs?"
      :style="{ width: '420px' }"
    >
      <p>
        You're about to queue <strong>{{ selectedFileCount }} files</strong> across
        <strong>{{ selected.length }} items</strong>. They'll all enter the worker queue
        in <code>ready</code> state.
      </p>
      <template #footer>
        <Button text label="Cancel" :disabled="queuing" @click="confirmOpen = false" />
        <Button label="Queue" :loading="queuing" @click="doQueue" />
      </template>
    </Dialog>
  </section>
</template>

<style scoped>
.library {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.page-head h1 {
  margin: 0 0 0.25rem;
  font-size: 1.4rem;
}
.muted {
  color: var(--rc-muted);
  font-size: 0.85rem;
}
.tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  padding-bottom: 0.5rem;
  border-bottom: 1px solid var(--rc-border);
}
.tab {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  background: transparent;
  border: 1px solid var(--rc-border);
  color: var(--rc-fg-2);
  padding: 0.35rem 0.65rem;
  border-radius: var(--rc-r-sm);
  cursor: pointer;
  font-size: 0.85rem;
}
.tab:hover {
  background: var(--rc-surface-2);
  color: var(--rc-fg);
}
.tab.active {
  background: var(--rc-surface-2);
  color: var(--rc-fg);
  border-color: var(--rc-accent);
}
.tab-name {
  font-weight: 500;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex-wrap: wrap;
}
.toolbar .filter {
  min-width: 18rem;
}
.checkbox-row {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
}
.spacer {
  flex: 1;
}
.loading {
  display: flex;
  justify-content: center;
  padding: 2rem 0;
}
.error {
  background: var(--rc-surface-2);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  padding: 1rem;
  color: var(--rc-danger);
  font-family: var(--rc-font-mono, monospace);
  font-size: 0.85rem;
}
.empty {
  background: var(--rc-surface-2);
  border: 1px dashed var(--rc-border);
  border-radius: var(--rc-r-md);
  padding: 1.25rem;
  color: var(--rc-fg-2);
  font-size: 0.9rem;
}
.title-cell {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  min-width: 0;
}
.title-main {
  font-weight: 500;
}
.title-path {
  font-size: 0.75rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 36rem;
}
.badges {
  display: inline-flex;
  gap: 0.25rem;
  flex-wrap: wrap;
}
</style>
