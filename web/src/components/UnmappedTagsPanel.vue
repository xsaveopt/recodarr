<script setup lang="ts">
import { ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import Button from "primevue/button";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Tag from "primevue/tag";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { UnmappedTag } from "@/types/api";

const notify = useNotify();
const route = useRoute();
const router = useRouter();
const unmappedTags = ref<UnmappedTag[]>([]);
const loading = ref(true);
const loaded = ref(false);

async function load() {
  loading.value = true;
  const res = await notify.tryRun(() => api.arr.unmappedTags(), "Couldn't load unmapped tags");
  unmappedTags.value = res ?? [];
  loading.value = false;
  loaded.value = true;
}

function mapTag(u: UnmappedTag) {
  void router.replace({
    query: {
      ...route.query,
      tab: "mappings",
      mapKind: u.kind,
      mapInstance: String(u.instanceId),
      mapTag: String(u.tagId),
    },
  });
}

function kindSeverity(kind: string) {
  if (kind === "sonarr") return "info";
  if (kind === "radarr") return "warn";
  return "secondary";
}

watch(
  () => route.query.tab,
  (tab) => {
    if (tab === "unmapped" && !loaded.value) void load();
  },
  { immediate: true },
);
</script>

<template>
  <div class="panel">
    <div class="head">
      <p class="muted">
        Tags applied to at least one library item that have no tag → profile mapping. Items carrying
        only these tags are skipped by the reconciler. Loading queries every enabled *arr instance,
        so it can take a moment on large libraries.
      </p>
      <Button
        text
        icon="pi pi-refresh"
        :loading="loading"
        title="Re-check *arr libraries"
        @click="load"
      />
    </div>

    <DataTable :value="unmappedTags" :loading="loading" stripedRows size="small" class="table">
      <template #empty>
        <span class="muted">{{
          loaded ? "Every tag applied to your library is mapped." : "Checking your *arr libraries…"
        }}</span>
      </template>
      <Column header="Target" style="width: 8rem">
        <template #body="{ data }">
          <Tag :value="data.kind" :severity="kindSeverity(data.kind)" />
        </template>
      </Column>
      <Column field="tagLabel" header="Tag" />
      <Column field="instanceName" header="Instance" />
      <Column header="Items" style="width: 6rem">
        <template #body="{ data }">
          {{ data.itemCount }}
        </template>
      </Column>
      <Column header="" style="width: 7rem">
        <template #body="{ data }">
          <Button text size="small" label="Map" icon="pi pi-plus" @click="mapTag(data)" />
        </template>
      </Column>
    </DataTable>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding-top: 0.5rem;
}
.head {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
}
.muted {
  color: var(--app-muted);
  margin: 0;
  font-size: 0.9rem;
}
.table {
  width: 100%;
}
</style>
