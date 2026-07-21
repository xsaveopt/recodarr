<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import Password from "primevue/password";
import Select from "primevue/select";
import ToggleSwitch from "primevue/toggleswitch";
import Tag from "primevue/tag";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { ArrInstance } from "@/types/api";

const notify = useNotify();
const items = ref<ArrInstance[]>([]);
const editing = ref<Partial<ArrInstance> | null>(null);
const validationError = ref<string | null>(null);
const testResults = ref<Record<number, { ok: boolean; error?: string; at: number } | null>>({});
const testing = ref<Set<number>>(new Set());

const TEST_LS_KEY = "recodarr.arrTestResults";

const kindOptions = [
  { value: "sonarr", label: "Sonarr" },
  { value: "radarr", label: "Radarr" },
];

function loadCachedTests() {
  try {
    const raw = localStorage.getItem(TEST_LS_KEY);
    if (raw) testResults.value = JSON.parse(raw);
  } catch {}
}

function persistTests() {
  try {
    localStorage.setItem(TEST_LS_KEY, JSON.stringify(testResults.value));
  } catch {}
}

async function load() {
  const res = await notify.tryRun(() => api.arr.list(), "Couldn't load *arr instances");
  if (res !== undefined) items.value = res ?? [];
}

function startCreate() {
  validationError.value = null;
  editing.value = {
    kind: "sonarr",
    name: "",
    url: "",
    apiKey: "",
    enabled: true,
    hasApiKey: false,
  };
}

function startEdit(row: ArrInstance) {
  validationError.value = null;
  editing.value = { ...row, apiKey: "" };
}

async function save() {
  if (!editing.value) return;
  validationError.value = null;
  const e = editing.value;
  if (!e.name?.trim()) {
    validationError.value = "Name is required.";
    return;
  }
  if (!e.url?.trim()) {
    validationError.value = "URL is required.";
    return;
  }
  if (!e.id && !e.apiKey?.trim()) {
    validationError.value = "API key is required.";
    return;
  }
  const saved = await notify.tryRun(async () => {
    return e.id
      ? await api.arr.update(e as ArrInstance)
      : await api.arr.create(e as Omit<ArrInstance, "id">);
  }, "Couldn't save instance");
  if (saved) {
    notify.success(`Saved ${e.name}`);
    editing.value = null;
    await load();
  }
}

function remove(row: ArrInstance) {
  notify.confirmDelete({
    name: row.name,
    onAccept: async () => {
      const ok = await notify.tryAct(
        () => api.arr.remove(row.id),
        `Deleted ${row.name}`,
        "Couldn't delete instance",
      );
      if (ok) {
        delete testResults.value[row.id];
        persistTests();
        await load();
      }
    },
  });
}

async function testConnection(row: ArrInstance) {
  testing.value = new Set([...testing.value, row.id]);
  try {
    const r = await api.arr.test(row.id);
    testResults.value[row.id] = { ...r, at: Date.now() };
  } catch (e) {
    testResults.value[row.id] = { ok: false, error: (e as Error).message, at: Date.now() };
  } finally {
    testing.value.delete(row.id);
    testing.value = new Set(testing.value);
    persistTests();
  }
}

function relativeTime(ms: number): string {
  const diff = Math.floor((Date.now() - ms) / 1000);
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

onMounted(() => {
  loadCachedTests();
  void load();
});
</script>

<template>
  <div class="panel">
    <div class="panel-head">
      <p class="muted">
        Sonarr/Radarr instances. Recodarr polls each enabled instance's library on a schedule and
        queues tagged items automatically — the only *arr-side setup needed is the API key.
      </p>
      <Button icon="pi pi-plus" label="Add" @click="startCreate" />
    </div>
    <DataTable :value="items" stripedRows size="small">
      <template #empty>
        <span class="muted">No instances yet — add one to start polling.</span>
      </template>
      <Column field="kind" header="Kind">
        <template #body="{ data }">
          <Tag :value="data.kind" :severity="data.kind === 'sonarr' ? 'info' : 'warn'" />
        </template>
      </Column>
      <Column field="name" header="Name" />
      <Column field="url" header="URL" />
      <Column field="enabled" header="Enabled">
        <template #body="{ data }">{{ data.enabled ? "yes" : "no" }}</template>
      </Column>
      <Column header="Status" style="width: 11rem">
        <template #body="{ data }">
          <span v-if="!testResults[data.id]" class="muted small">never tested</span>
          <span
            v-else
            :class="testResults[data.id]?.ok ? 'test-ok' : 'test-fail'"
            :title="testResults[data.id]?.error"
          >
            {{ testResults[data.id]?.ok ? "OK" : "Fail" }}
            <span class="small muted">· {{ relativeTime(testResults[data.id]!.at) }}</span>
          </span>
        </template>
      </Column>
      <Column header="" style="width: 11rem">
        <template #body="{ data }">
          <Button
            text
            size="small"
            icon="pi pi-wifi"
            title="Test connection"
            :loading="testing.has(data.id)"
            @click="testConnection(data)"
          />
          <Button text size="small" icon="pi pi-pencil" @click="startEdit(data)" />
          <Button text size="small" severity="danger" icon="pi pi-trash" @click="remove(data)" />
        </template>
      </Column>
    </DataTable>

    <Dialog
      :visible="editing !== null"
      @update:visible="(v) => (editing = v ? editing : null)"
      modal
      :header="editing?.id ? 'Edit instance' : 'Add instance'"
      :style="{ width: '34rem' }"
    >
      <div v-if="editing" class="form">
        <div v-if="validationError" class="error">{{ validationError }}</div>
        <label>
          <span>Kind</span>
          <Select
            v-model="editing.kind"
            :options="kindOptions"
            optionLabel="label"
            optionValue="value"
            :disabled="!!editing.id"
          />
        </label>
        <label>
          <span>Name</span>
          <InputText v-model="editing.name" placeholder="main" />
        </label>
        <label>
          <span>URL</span>
          <InputText v-model="editing.url" placeholder="http://sonarr:8989" />
        </label>
        <label>
          <span>API key</span>
          <Password
            v-model="editing.apiKey"
            toggleMask
            :feedback="false"
            :placeholder="editing.hasApiKey ? '(stored — leave blank to keep)' : ''"
          />
        </label>

        <label class="row">
          <span>Enabled</span>
          <ToggleSwitch v-model="editing.enabled" />
        </label>
      </div>
      <template #footer>
        <Button text label="Cancel" @click="editing = null" />
        <Button label="Save" icon="pi pi-check" @click="save" />
      </template>
    </Dialog>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
.muted {
  color: var(--app-muted);
  margin: 0;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
}
.form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.form label {
  display: grid;
  grid-template-columns: 9rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.form label.row {
  grid-template-columns: 9rem auto;
}
.test-ok {
  font-size: 0.85rem;
  color: var(--app-ok-fg);
  font-weight: 600;
}
.test-fail {
  font-size: 0.85rem;
  color: var(--app-error-fg);
  font-weight: 600;
  cursor: help;
}
.small {
  font-size: 0.75rem;
}
</style>
