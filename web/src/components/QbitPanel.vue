<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Password from "primevue/password";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { QbitInstance } from "@/types/api";

const notify = useNotify();
const item = ref<QbitInstance>({
  id: 0,
  name: "qbit",
  url: "",
  username: "",
  password: "",
  hasPassword: false,
});
const validationError = ref<string | null>(null);
const testResult = ref<{ ok: boolean; error?: string } | null>(null);
const testing = ref(false);

async function load() {
  const list = await notify.tryRun(() => api.qbit.list(), "Couldn't load qBit");
  if (list && list.length) item.value = { ...list[0], password: "" };
}

async function save() {
  validationError.value = null;
  if (!item.value.url?.trim()) {
    validationError.value = "URL is required.";
    return;
  }
  if (!item.value.username?.trim()) {
    validationError.value = "Username is required.";
    return;
  }
  const out = await notify.tryRun(() => api.qbit.upsert(item.value), "Couldn't save qBit");
  if (out) {
    item.value = { ...out, password: "" };
    notify.success("Saved qBit credentials");
  }
}

async function test() {
  if (!item.value.url?.trim()) {
    testResult.value = { ok: false, error: "URL is required." };
    return;
  }
  testing.value = true;
  testResult.value = null;
  try {
    const useStored = item.value.id > 0 && !item.value.password?.trim();
    testResult.value = useStored
      ? await api.qbit.test(item.value.id)
      : await api.qbit.testCredentials(
          item.value.url,
          item.value.username,
          item.value.password ?? "",
        );
  } catch (e) {
    testResult.value = { ok: false, error: (e as Error).message };
  } finally {
    testing.value = false;
  }
}

function remove() {
  if (!item.value.id) return;
  notify.confirmDelete({
    name: "qBit credentials",
    onAccept: async () => {
      const ok = await notify.tryAct(
        () => api.qbit.remove(item.value.id),
        "Deleted qBit credentials",
        "Couldn't delete",
      );
      if (ok) {
        item.value = {
          id: 0,
          name: "qbit",
          url: "",
          username: "",
          password: "",
          hasPassword: false,
        };
      }
    },
  });
}

onMounted(load);
</script>

<template>
  <div class="panel">
    <p class="muted">
      Single qBittorrent instance. Recodarr polls it to detect when a torrent has finished seeding.
    </p>
    <div v-if="validationError" class="error">{{ validationError }}</div>

    <div class="form">
      <label>
        <span>Name</span>
        <InputText v-model="item.name" />
      </label>
      <label>
        <span>URL</span>
        <InputText v-model="item.url" placeholder="http://qbit:8080" />
      </label>
      <label>
        <span>Username</span>
        <InputText v-model="item.username" />
      </label>
      <label>
        <span>Password</span>
        <Password
          v-model="item.password"
          toggleMask
          :feedback="false"
          :placeholder="item.hasPassword ? '(stored — leave blank to keep)' : ''"
        />
      </label>
    </div>

    <div v-if="testResult" :class="testResult.ok ? 'ok' : 'error'">
      {{ testResult.ok ? "Connection successful." : `Connection failed: ${testResult.error}` }}
    </div>

    <div class="actions">
      <Button label="Save" icon="pi pi-check" @click="save" />
      <Button text icon="pi pi-wifi" label="Test" :loading="testing" @click="test" />
      <Button
        v-if="item.id"
        text
        severity="danger"
        icon="pi pi-trash"
        label="Delete"
        @click="remove"
      />
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  max-width: 36rem;
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
.ok {
  background: var(--app-ok-bg);
  color: var(--app-ok-fg);
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
  grid-template-columns: 8rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
</style>
