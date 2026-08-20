<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";
import Button from "primevue/button";
import Checkbox from "primevue/checkbox";
import InputText from "primevue/inputtext";
import Menu from "primevue/menu";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { LibraryFile, LibraryItem, LibraryMappableTag, LibraryScan } from "@/types/api";

type LibraryRow = LibraryItem & { key: string };

const props = defineProps<{ kind: "sonarr" | "radarr" }>();

const notify = useNotify();
const scan = ref<LibraryScan | null>(null);
const loading = ref(false);
const deep = ref(false);
const search = ref("");
const unmappedOnly = ref(false);
const expandedRows = ref<Record<string, boolean>>({});
const filesByItem = ref<Record<string, LibraryFile[] | "loading" | "error">>({});
const tagMenu = ref<InstanceType<typeof Menu> | null>(null);
const menuRow = ref<LibraryRow | null>(null);
const taggingKey = ref<string | null>(null);

const parentLabel = computed(() => (props.kind === "sonarr" ? "series" : "movies"));

async function load(refresh = false) {
  loading.value = true;
  const res = await notify.tryRun(
    () => api.library.scan(props.kind, { deep: deep.value, refresh }),
    `Couldn't scan ${props.kind}`,
  );
  if (res) {
    scan.value = res;
    for (const inst of res.instances) {
      if (inst.error) notify.error(`${inst.name}: ${inst.error}`);
    }
  }
  loading.value = false;
}

onMounted(() => load());
watch(deep, () => {
  expandedRows.value = {};
  load();
});

const items = computed<LibraryRow[]>(() => {
  const all = scan.value?.items ?? [];
  const q = search.value.trim().toLowerCase();
  return all
    .filter((it) => {
      if (unmappedOnly.value && it.mapped) return false;
      if (!q) return true;
      return it.title.toLowerCase().includes(q) || it.tagLabels.join(" ").toLowerCase().includes(q);
    })
    .map((it) => ({ ...it, key: `${it.instanceId}:${it.itemId}` }));
});

const totals = computed(() => {
  let size = 0;
  let seconds = 0;
  let unmapped = 0;
  for (const it of items.value) {
    size += it.totalSize;
    seconds += it.runtimeSeconds;
    if (!it.mapped) unmapped += it.totalSize;
  }
  return { size, bitrate: seconds > 0 ? (size * 8) / seconds : 0, unmapped };
});

async function onExpand(it: LibraryRow) {
  if (filesByItem.value[it.key] && filesByItem.value[it.key] !== "error") return;
  filesByItem.value = { ...filesByItem.value, [it.key]: "loading" };
  try {
    const files = await api.library.files(props.kind, it.instanceId, it.itemId);
    filesByItem.value = { ...filesByItem.value, [it.key]: files };
  } catch {
    filesByItem.value = { ...filesByItem.value, [it.key]: "error" };
  }
}

function instanceFor(instanceId: number) {
  return scan.value?.instances.find((i) => i.id === instanceId);
}

function openTagMenu(event: Event, row: LibraryRow) {
  const inst = instanceFor(row.instanceId);
  const app = props.kind === "sonarr" ? "Sonarr" : "Radarr";
  if (!inst || inst.tagCount === 0) {
    notify.error(
      `${inst?.name ?? app} has no tags. Create a tag in ${app} first, then map it to a HandBrake profile under Settings → Mappings.`,
    );
    return;
  }
  if (!inst.mappableTags.length) {
    notify.error(
      `None of ${inst.name}'s tags are mapped to a HandBrake profile. Add a mapping under Settings → Mappings first.`,
    );
    return;
  }
  menuRow.value = row;
  tagMenu.value?.toggle(event);
}

const tagMenuItems = computed(() => {
  const row = menuRow.value;
  if (!row) return [];
  const inst = instanceFor(row.instanceId);
  return (inst?.mappableTags ?? []).map((t) => ({
    label: t.tagLabel,
    icon: row.tagLabels.includes(t.tagLabel) ? "pi pi-check" : "pi pi-tag",
    disabled: row.tagLabels.includes(t.tagLabel),
    command: () => applyTag(row, t),
  }));
});

async function applyTag(row: LibraryRow, tag: LibraryMappableTag) {
  taggingKey.value = row.key;
  const res = await notify.tryRun(
    () => api.library.addTag(props.kind, row.instanceId, row.itemId, tag.tagId),
    `Couldn't tag ${row.title}`,
  );
  taggingKey.value = null;
  if (!res) return;
  const item = scan.value?.items.find(
    (i) => i.instanceId === row.instanceId && i.itemId === row.itemId,
  );
  if (item) {
    item.mapped = true;
    if (!item.tagLabels.includes(res.tagLabel)) item.tagLabels = [...item.tagLabels, res.tagLabel];
    if (!item.mappedTags.includes(res.tagLabel))
      item.mappedTags = [...item.mappedTags, res.tagLabel];
  }
  notify.success(
    `Tagged ${row.title} as “${res.tagLabel}” — Recodarr will pick it up on the next library poll and encode it with ${tag.profileName}.`,
  );
}

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

function formatBitrate(bps?: number) {
  if (!bps) return "—";
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(2)} Mbps`;
  return `${Math.round(bps / 1000)} kbps`;
}

function formatRuntime(seconds?: number) {
  if (!seconds) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.round((seconds % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function scannedAt() {
  if (!scan.value) return "";
  return new Date(scan.value.scannedAt).toLocaleTimeString();
}
</script>

<template>
  <div class="library-panel">
    <Menu ref="tagMenu" :model="tagMenuItems" popup>
      <template #start>
        <div class="menu-head">
          Tag it in {{ props.kind === "sonarr" ? "Sonarr" : "Radarr" }} as
        </div>
      </template>
    </Menu>
    <div class="toolbar">
      <InputText v-model="search" placeholder="Filter by title or tag" class="search" />
      <label class="check">
        <Checkbox v-model="unmappedOnly" binary />
        <span>Unmapped only</span>
      </label>
      <label class="check">
        <Checkbox v-model="deep" binary />
        <span>Exact bitrate (slower)</span>
      </label>
      <span class="spacer" />
      <span v-if="scan" class="muted">scanned {{ scannedAt() }}</span>
      <Button
        label="Rescan"
        icon="pi pi-refresh"
        severity="secondary"
        size="small"
        :loading="loading"
        @click="load(true)"
      />
    </div>

    <div class="summary">
      <div class="stat">
        <span class="stat-label">{{ parentLabel }}</span>
        <span class="stat-value">{{ items.length }}</span>
      </div>
      <div class="stat">
        <span class="stat-label">total size</span>
        <span class="stat-value">{{ formatBytes(totals.size) }}</span>
      </div>
      <div class="stat">
        <span class="stat-label">average bitrate</span>
        <span class="stat-value">{{ formatBitrate(totals.bitrate) }}</span>
      </div>
      <div class="stat">
        <span class="stat-label">not covered by a mapping</span>
        <span class="stat-value">{{ formatBytes(totals.unmapped) }}</span>
      </div>
    </div>

    <p v-if="!deep" class="muted note">
      Bitrates are estimated from size divided by the runtime reported by
      {{ props.kind === "sonarr" ? "Sonarr" : "Radarr" }}. Tick “Exact bitrate” to read each file's
      media info instead — accurate, but it hits the API once per {{ parentLabel }}.
    </p>

    <DataTable
      v-model:expandedRows="expandedRows"
      :value="items"
      :loading="loading"
      dataKey="key"
      paginator
      :rows="50"
      :rowsPerPageOptions="[25, 50, 100, 200]"
      sortField="bitrateBps"
      :sortOrder="-1"
      stripedRows
      size="small"
      @rowExpand="onExpand($event.data)"
    >
      <template #empty>
        <span class="muted"
          >Nothing to show — no enabled {{ props.kind }} instance with files.</span
        >
      </template>
      <Column expander headerStyle="width: 2.5rem" />
      <Column field="title" header="Title" sortable>
        <template #body="{ data }">
          <div class="title-cell">
            <span>{{ data.title }}</span>
            <span v-if="data.year" class="muted">({{ data.year }})</span>
          </div>
        </template>
      </Column>
      <Column field="bitrateBps" header="Bitrate" sortable style="width: 9rem">
        <template #body="{ data }">
          <span class="bitrate">{{ formatBitrate(data.bitrateBps) }}</span>
          <span v-if="!data.bitrateExact" class="muted approx" title="estimated from runtime"
            >~</span
          >
        </template>
      </Column>
      <Column field="totalSize" header="Size" sortable style="width: 8rem">
        <template #body="{ data }">{{ formatBytes(data.totalSize) }}</template>
      </Column>
      <Column field="fileCount" header="Files" sortable style="width: 5rem" />
      <Column field="runtimeSeconds" header="Runtime" sortable style="width: 7rem">
        <template #body="{ data }">{{ formatRuntime(data.runtimeSeconds) }}</template>
      </Column>
      <Column field="videoCodec" header="Codec" sortable style="width: 7rem">
        <template #body="{ data }">
          <span v-if="data.videoCodec">{{ data.videoCodec }}</span>
          <span v-else class="muted">—</span>
        </template>
      </Column>
      <Column field="mapped" header="Recodarr" sortable style="width: 8rem">
        <template #body="{ data }">
          <Tag
            :value="data.mapped ? 'mapped' : 'unmapped'"
            :severity="data.mapped ? 'success' : 'secondary'"
          />
        </template>
      </Column>
      <Column header="Tags">
        <template #body="{ data }">
          <span v-if="!data.tagLabels.length" class="muted">—</span>
          <span
            v-for="tag in data.tagLabels"
            :key="tag"
            class="tag-chip"
            :class="{ mappedTag: data.mappedTags.includes(tag) }"
            >{{ tag }}</span
          >
        </template>
      </Column>
      <Column field="instanceName" header="Instance" sortable style="width: 9rem" />
      <Column header="" style="width: 3rem">
        <template #body="{ data }">
          <Button
            icon="pi pi-plus"
            size="small"
            text
            rounded
            :severity="data.mapped ? 'secondary' : 'primary'"
            :loading="taggingKey === data.key"
            :title="data.mapped ? 'Add another tag' : 'Add to Recodarr by tagging it'"
            :aria-label="data.mapped ? 'Add another tag' : 'Add to Recodarr'"
            @click="openTagMenu($event, data)"
          />
        </template>
      </Column>

      <template #expansion="{ data }">
        <div class="files">
          <div v-if="filesByItem[data.key] === 'loading'" class="muted">Loading files…</div>
          <div v-else-if="filesByItem[data.key] === 'error'" class="muted">
            Couldn't load files for this {{ props.kind === "sonarr" ? "series" : "movie" }}.
          </div>
          <DataTable
            v-else
            :value="(filesByItem[data.key] as LibraryFile[]) ?? []"
            dataKey="fileId"
            size="small"
          >
            <template #empty><span class="muted">No files.</span></template>
            <Column field="relativePath" header="File" />
            <Column header="Bitrate" style="width: 9rem">
              <template #body="{ data: f }">{{ formatBitrate(f.bitrateBps) }}</template>
            </Column>
            <Column header="Video" style="width: 9rem">
              <template #body="{ data: f }">{{ formatBitrate(f.videoBitrate) }}</template>
            </Column>
            <Column header="Audio" style="width: 9rem">
              <template #body="{ data: f }">{{ formatBitrate(f.audioBitrate) }}</template>
            </Column>
            <Column header="Size" style="width: 8rem">
              <template #body="{ data: f }">{{ formatBytes(f.size) }}</template>
            </Column>
            <Column header="Runtime" style="width: 7rem">
              <template #body="{ data: f }">{{ formatRuntime(f.runtimeSeconds) }}</template>
            </Column>
            <Column field="videoCodec" header="Codec" style="width: 7rem" />
            <Column field="resolution" header="Resolution" style="width: 9rem" />
            <Column field="quality" header="Quality" style="width: 10rem" />
          </DataTable>
        </div>
      </template>
    </DataTable>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin-bottom: 0.75rem;
}
.search {
  min-width: 16rem;
}
.check {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.875rem;
}
.spacer {
  flex: 1;
}
.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--p-content-border-color);
  border-radius: 0.5rem;
  margin-bottom: 0.75rem;
}
.stat {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.stat-label {
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--p-text-muted-color);
}
.stat-value {
  font-size: 1.1rem;
  font-weight: 600;
}
.note {
  margin: 0 0 0.75rem;
  font-size: 0.8125rem;
}
.title-cell {
  display: flex;
  gap: 0.4rem;
  align-items: baseline;
}
.bitrate {
  font-variant-numeric: tabular-nums;
}
.approx {
  margin-left: 0.2rem;
}
.tag-chip {
  display: inline-block;
  padding: 0.05rem 0.4rem;
  margin-right: 0.25rem;
  border-radius: 0.25rem;
  font-size: 0.75rem;
  border: 1px solid var(--p-content-border-color);
}
.tag-chip.mappedTag {
  border-color: var(--p-primary-color);
  color: var(--p-primary-color);
}
.files {
  padding: 0.5rem 0.5rem 0.75rem;
}
.muted {
  color: var(--p-text-muted-color);
}
.menu-head {
  padding: 0.5rem 0.75rem 0.25rem;
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--p-text-muted-color);
}
</style>
