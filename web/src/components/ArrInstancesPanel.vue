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
  } catch {
    /* ignore */
  }
}

function persistTests() {
  try {
    localStorage.setItem(TEST_LS_KEY, JSON.stringify(testResults.value));
  } catch {
    /* ignore */
  }
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
    webhookSecret: "",
    hasApiKey: false,
    hasWebhookSecret: false,
  };
}

function startEdit(row: ArrInstance) {
  validationError.value = null;
  // Strip secrets from the editing copy — they were never sent. Blank means "keep".
  editing.value = { ...row, apiKey: "", webhookSecret: "" };
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
  const isCreate = !e.id;
  const saved = await notify.tryRun(async () => {
    return e.id
      ? await api.arr.update(e as ArrInstance)
      : await api.arr.create(e as Omit<ArrInstance, "id">);
  }, "Couldn't save instance");
  if (saved) {
    notify.success(`Saved ${e.name}`);
    editing.value = null;
    await load();
    // After a fresh create, auto-pop the connect dialog so the user can grab
    // the auto-generated webhook secret right away.
    if (isCreate && saved.id) {
      await openConnect({ ...(saved as ArrInstance) });
    }
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

function webhookURL(row: ArrInstance) {
  return `${window.location.origin}/webhook/${row.kind}/${row.id}`;
}

// "Connect" dialog: shows the webhook URL + Basic-auth username/password.
const connectFor = ref<ArrInstance | null>(null);
const connectUser = ref<string>("");
const connectPass = ref<string>("");
const connectLoading = ref(false);

async function openConnect(row: ArrInstance) {
  connectFor.value = row;
  connectUser.value = "";
  connectPass.value = "";
  connectLoading.value = true;
  try {
    const r = await api.arr.revealWebhookSecret(row.id);
    connectUser.value = r.username;
    connectPass.value = r.password;
  } catch (e) {
    notify.error(e);
    connectFor.value = null;
  } finally {
    connectLoading.value = false;
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
        Sonarr/Radarr instances. After saving, click <strong>Show</strong> to reveal the webhook URL
        plus the Basic-auth username/password to paste into *arr's Settings → Connect → Webhook.
      </p>
      <Button icon="pi pi-plus" label="Add" @click="startCreate" />
    </div>
    <DataTable :value="items" stripedRows size="small">
      <template #empty>
        <span class="muted">No instances yet — add one to start receiving webhooks.</span>
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
      <Column header="Connect" style="width: 8rem">
        <template #body="{ data }">
          <Button
            text
            size="small"
            icon="pi pi-link"
            label="Show"
            title="Show webhook URL & token"
            @click="openConnect(data)"
          />
        </template>
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
      :visible="connectFor !== null"
      @update:visible="(v) => (connectFor = v ? connectFor : null)"
      modal
      :header="connectFor ? `Connect ${connectFor.kind} → Recodarr` : ''"
      :style="{ width: '38rem' }"
    >
      <div v-if="connectFor" class="connect-body">
        <p class="muted small">
          In <strong>{{ connectFor.kind }}</strong
          >: Settings → Connect → + → Webhook. Paste the URL, set Method to <strong>POST</strong>,
          tick <strong>On File Import</strong> (and <strong>On File Upgrade</strong>), then fill in
          the Username and Password fields below.
        </p>

        <label class="connect-row">
          <span>URL</span>
          <code class="copyable">{{ webhookURL(connectFor) }}</code>
        </label>

        <label class="connect-row">
          <span>Username</span>
          <code class="copyable">{{ connectUser || "recodarr" }}</code>
        </label>

        <label class="connect-row">
          <span>Password</span>
          <code class="copyable">{{ connectLoading ? "loading…" : connectPass }}</code>
        </label>
      </div>
      <template #footer>
        <Button label="Done" @click="connectFor = null" />
      </template>
    </Dialog>

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
        <label>
          <span>Webhook password</span>
          <Password
            v-model="editing.webhookSecret"
            toggleMask
            :feedback="false"
            :placeholder="
              editing.hasWebhookSecret
                ? '(stored — leave blank to keep)'
                : '(auto-generated if empty)'
            "
          />
        </label>
        <p class="muted small">
          Used as the HTTP Basic-auth password for incoming webhooks (username is always
          <code>recodarr</code>). Leave blank to auto-generate. After saving you'll see a panel with
          everything you need to paste into *arr.
        </p>
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
.connect-body {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.connect-row {
  display: grid;
  grid-template-columns: 10rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.copyable {
  display: block;
  background: var(--app-row-alt);
  border: 1px solid var(--app-panel-border);
  border-radius: 6px;
  padding: 0.4rem 0.6rem;
  font-size: 0.85rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  word-break: break-all;
  user-select: all; /* one click selects the whole value */
  cursor: text;
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
