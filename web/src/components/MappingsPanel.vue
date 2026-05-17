<script setup lang="ts">
import { onMounted, ref, computed } from "vue";
import Button from "primevue/button";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Select from "primevue/select";
import Tag from "primevue/tag";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { TagMapping, InstanceTag, Profile } from "@/types/api";

const notify = useNotify();
const mappings = ref<TagMapping[]>([]);
const profiles = ref<Profile[]>([]);
const availableTags = ref<InstanceTag[]>([]);
const loadingTags = ref(false);
const validationError = ref<string | null>(null);

const kindOptions = [
  { value: "sonarr", label: "Sonarr" },
  { value: "radarr", label: "Radarr" },
  { value: "both", label: "Both" },
];

const newKind = ref<"sonarr" | "radarr" | "both" | null>(null);
const newTag = ref<InstanceTag | null>(null);
const newProfileId = ref<number | null>(null);

const filteredTags = computed(() => {
  if (!newKind.value || newKind.value === "both") return availableTags.value;
  return availableTags.value.filter((t) => t.kind === newKind.value);
});

async function load() {
  const res = await notify.tryRun(
    () => Promise.all([api.tagMappings.list(), api.profiles.list()]),
    "Couldn't load mappings",
  );
  if (res) {
    mappings.value = res[0] ?? [];
    profiles.value = res[1] ?? [];
  }
}

async function fetchTags() {
  loadingTags.value = true;
  try {
    availableTags.value = (await api.arr.allTags()) ?? [];
  } catch {
    availableTags.value = [];
  } finally {
    loadingTags.value = false;
  }
}

async function add() {
  validationError.value = null;
  if (!newKind.value) {
    validationError.value = "Select a target (Sonarr / Radarr / Both).";
    return;
  }
  if (!newTag.value) {
    validationError.value = "Select a tag.";
    return;
  }
  if (!newProfileId.value) {
    validationError.value = "Select a profile.";
    return;
  }
  const m = await notify.tryRun(
    () =>
      api.tagMappings.create({
        arrKind: newKind.value!,
        tagId: newTag.value!.tagId,
        tagLabel: newTag.value!.tagLabel,
        profileId: newProfileId.value!,
      }),
    "Couldn't add mapping",
  );
  if (m) {
    mappings.value.push(m);
    newKind.value = null;
    newTag.value = null;
    newProfileId.value = null;
    notify.success(`Added mapping for ${m.tagLabel}`);
  }
}

function remove(m: TagMapping) {
  notify.confirmDelete({
    name: `${m.tagLabel} → ${profileName(m.profileId)}`,
    onAccept: async () => {
      const ok = await notify.tryAct(
        () => api.tagMappings.remove(m.id),
        `Deleted mapping for ${m.tagLabel}`,
        "Couldn't delete mapping",
      );
      if (ok) {
        mappings.value = mappings.value.filter((x) => x.id !== m.id);
      }
    },
  });
}

function profileName(id: number) {
  return profiles.value.find((p) => p.id === id)?.name ?? `#${id}`;
}

function kindSeverity(kind: string) {
  if (kind === "sonarr") return "info";
  if (kind === "radarr") return "warn";
  return "secondary";
}

onMounted(() => {
  void load();
  void fetchTags();
});
</script>

<template>
  <div class="panel">
    <p class="muted">
      Tag → profile mappings are evaluated on every incoming webhook. Add a tag from Sonarr, Radarr,
      or both — items carrying that tag will be queued with the chosen profile.
    </p>

    <DataTable :value="mappings" stripedRows size="small" class="mapping-table">
      <template #empty>
        <span class="muted">No mappings yet — add one below.</span>
      </template>
      <Column header="Target" style="width: 8rem">
        <template #body="{ data }">
          <Tag :value="data.arrKind" :severity="kindSeverity(data.arrKind)" />
        </template>
      </Column>
      <Column field="tagLabel" header="Tag" />
      <Column header="Profile">
        <template #body="{ data }">{{ profileName(data.profileId) }}</template>
      </Column>
      <Column header="" style="width: 4rem">
        <template #body="{ data }">
          <Button text size="small" severity="danger" icon="pi pi-trash" @click="remove(data)" />
        </template>
      </Column>
    </DataTable>

    <div class="add-row">
      <div v-if="validationError" class="error">{{ validationError }}</div>
      <div class="add-form">
        <Select
          v-model="newKind"
          :options="kindOptions"
          optionLabel="label"
          optionValue="value"
          placeholder="Target"
          class="sel-kind"
        />
        <Select
          v-model="newTag"
          :options="filteredTags"
          :optionLabel="(t: InstanceTag) => t.instanceName + ' / ' + t.tagLabel"
          :loading="loadingTags"
          placeholder="Tag"
          class="sel-tag"
        />
        <Select
          v-model="newProfileId"
          :options="profiles"
          optionLabel="name"
          optionValue="id"
          placeholder="Profile"
          class="sel-profile"
        />
        <Button icon="pi pi-plus" label="Add" @click="add" />
        <Button
          text
          icon="pi pi-refresh"
          :loading="loadingTags"
          title="Reload tags from *arr"
          @click="fetchTags"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding-top: 0.5rem;
}
.muted {
  color: var(--app-muted);
  margin: 0;
  font-size: 0.9rem;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  font-size: 0.9rem;
}
.mapping-table {
  width: 100%;
}
.add-row {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.add-form {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  flex-wrap: wrap;
}
.sel-kind {
  width: 9rem;
}
.sel-tag {
  flex: 1;
  min-width: 12rem;
}
.sel-profile {
  width: 12rem;
}
</style>
