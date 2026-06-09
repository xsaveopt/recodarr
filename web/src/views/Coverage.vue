<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Checkbox from "primevue/checkbox";
import ProgressBar from "primevue/progressbar";
import ProgressSpinner from "primevue/progressspinner";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { ArrInstance, ScanItem } from "@/types/api";

const notify = useNotify();

const instances = ref<ArrInstance[]>([]);
const activeInstanceId = ref<number | null>(null);
const items = ref<ScanItem[]>([]);
const noMappings = ref(false);
const suffixDisabled = ref(false);
const scanned = ref(false);
const scanning = ref(false);
const loadError = ref("");
const selected = ref<ScanItem[]>([]);
const titleFilter = ref("");
const hideComplete = ref(true);
const queuing = ref(false);

const activeInstance = computed(() =>
  instances.value.find((i) => i.id === activeInstanceId.value) ?? null,
);
const eligibleInstances = computed(() =>
  instances.value.filter((i) => i.enabled && (i.kind === "sonarr" || i.kind === "radarr")),
);

const filteredItems = computed(() => {
  const needle = titleFilter.value.trim().toLowerCase();
  return items.value.filter((it) => {
    if (hideComplete.value && it.unencodedCount === 0) return false;
    if (needle && !it.title.toLowerCase().includes(needle)) return false;
    return true;
  });
});

const totals = computed(() =>
  items.value.reduce(
    (acc, it) => {
      acc.files += it.fileCount;
      acc.encoded += it.encodedCount;
      return acc;
    },
    { files: 0, encoded: 0 },
  ),
);

const selectedUnencoded = computed(() =>
  selected.value.reduce((acc, it) => acc + it.unencodedCount, 0),
);

function pct(it: ScanItem) {
  if (it.fileCount === 0) return 0;
  return Math.round((it.encodedCount / it.fileCount) * 100);
}

function status(it: ScanItem): { label: string; severity: string } {
  if (it.fileCount === 0) return { label: "no files", severity: "secondary" };
  if (it.unencodedCount === 0) return { label: "complete", severity: "success" };
  if (it.encodedCount === 0) return { label: "none", severity: "danger" };
  return { label: "partial", severity: "warn" };
}

async function loadInstances() {
  const list = await notify.tryRun(() => api.arr.list(), "Couldn't load *arr instances");
  if (!list) return;
  instances.value = list;
  if (activeInstanceId.value == null && eligibleInstances.value.length > 0) {
    activeInstanceId.value = eligibleInstances.value[0].id;
  }
}

async function scan() {
  if (activeInstanceId.value == null) return;
  scanning.value = true;
  loadError.value = "";
  selected.value = [];
  try {
    const res = await api.arr.scanLibrary(activeInstanceId.value);
    items.value = res.items;
    noMappings.value = res.noMappings;
    suffixDisabled.value = res.suffixDisabled;
    scanned.value = true;
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err);
    items.value = [];
  } finally {
    scanning.value = false;
  }
}

function selectInstance(id: number) {
  activeInstanceId.value = id;
}

watch(activeInstanceId, () => {
  items.value = [];
  scanned.value = false;
  noMappings.value = false;
  suffixDisabled.value = false;
  selected.value = [];
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
    if (res.skipped > 0) parts.push(`${res.skipped} skipped (already encoded)`);
    notify.success(parts.join(", "));
    if (res.errors && res.errors.length > 0) {
      notify.info(
        `${res.errors.length} item${res.errors.length === 1 ? "" : "s"} had errors — see logs`,
      );
    }
    selected.value = [];
    await scan();
  } catch (err) {
    notify.error(err instanceof Error ? err.message : "Queue failed");
  } finally {
    queuing.value = false;
  }
}

onMounted(loadInstances);
</script>

<template>
  <section class="coverage">
    <header class="page-head">
      <h1>Coverage</h1>
      <p class="muted">
        Scan tagged libraries for encode coverage by checking for a Recodarr marker file
        alongside each episode/movie file. Queue the items that aren't fully encoded.
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
        <Button
          icon="pi pi-search"
          :label="scanned ? 'Re-scan' : 'Scan'"
          :disabled="scanning || activeInstanceId == null"
          :loading="scanning"
          @click="scan"
        />
        <InputText
          v-model="titleFilter"
          placeholder="Filter by title"
          class="filter"
          :disabled="!scanned"
        />
        <label class="checkbox-row">
          <Checkbox v-model="hideComplete" :binary="true" :disabled="!scanned" />
          <span>Hide fully encoded</span>
        </label>
        <span v-if="scanned && items.length > 0" class="summary muted">
          {{ totals.encoded }}/{{ totals.files }} files encoded across {{ items.length }} items
        </span>
        <span class="spacer"></span>
        <Button
          icon="pi pi-play"
          :label="selected.length === 0
            ? 'Queue selected'
            : `Queue ${selected.length} (${selectedUnencoded} file${selectedUnencoded === 1 ? '' : 's'})`"
          :disabled="selected.length === 0 || queuing"
          :loading="queuing"
          @click="doQueue"
        />
      </div>

      <div v-if="scanning" class="loading">
        <ProgressSpinner style="width: 32px; height: 32px" />
      </div>
      <div v-else-if="loadError" class="error">{{ loadError }}</div>
      <div v-else-if="suffixDisabled" class="empty">
        Encode marker files are disabled, so coverage can't be determined. Enable the
        output suffix in <RouterLink to="/settings">Settings</RouterLink> and re-encode to
        start writing markers.
      </div>
      <div v-else-if="!scanned" class="empty">
        Press <strong>Scan</strong> to check encode coverage for tagged items in
        {{ activeInstance?.name }}. This reads the *arr file list and stats each file, so
        it may take a moment on large libraries.
      </div>
      <div v-else-if="noMappings" class="empty">
        This instance kind has no tag→profile mappings.
        <RouterLink to="/settings">Add one in Settings</RouterLink>
        so Recodarr knows which items to track.
      </div>
      <div v-else-if="filteredItems.length === 0" class="empty">
        <template v-if="items.length === 0">
          No tagged items in this library that match a mapping.
        </template>
        <template v-else>
          Everything tagged is fully encoded. Uncheck "Hide fully encoded" to review.
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
        <Column field="profileName" header="Profile" sortable style="width: 11rem" />
        <Column field="unencodedCount" header="Encoded" sortable style="width: 14rem">
          <template #body="{ data }">
            <div class="cov-cell">
              <ProgressBar :value="pct(data)" :showValue="false" style="height: 6px" />
              <span class="cov-text">{{ data.encodedCount }}/{{ data.fileCount }}</span>
            </div>
          </template>
        </Column>
        <Column header="Status" style="width: 8rem">
          <template #body="{ data }">
            <Tag :value="status(data).label" :severity="status(data).severity" />
          </template>
        </Column>
      </DataTable>
    </template>
  </section>
</template>

<style scoped>
.coverage {
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
  min-width: 16rem;
}
.checkbox-row {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
}
.summary {
  font-size: 0.8rem;
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
.cov-cell {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.cov-cell :deep(.p-progressbar) {
  flex: 1;
}
.cov-text {
  font-size: 0.8rem;
  white-space: nowrap;
  color: var(--rc-fg-2);
}
</style>
