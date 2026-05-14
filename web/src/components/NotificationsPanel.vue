<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import ToggleSwitch from "primevue/toggleswitch";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { AppSettings } from "@/types/api";

const notify = useNotify();
const notifyUrl = ref<string>("");
const notifyOnDone = ref<boolean>(true);
const notifyOnFail = ref<boolean>(true);
const testing = ref(false);

async function load() {
  const s = await notify.tryRun(() => api.settings.get(), "Couldn't load settings");
  if (s) {
    notifyUrl.value = s.notify_url ?? "";
    notifyOnDone.value = s.notify_on_done !== "false";
    notifyOnFail.value = s.notify_on_fail !== "false";
  }
}

async function save() {
  const updates: AppSettings = {
    notify_url: notifyUrl.value.trim(),
    notify_on_done: notifyOnDone.value ? "true" : "false",
    notify_on_fail: notifyOnFail.value ? "true" : "false",
  };
  const ok = await notify.tryRun(() => api.settings.put(updates), "Couldn't save settings");
  if (ok !== undefined) notify.success("Notification settings saved");
}

async function test() {
  if (!notifyUrl.value.trim()) {
    notify.error("No webhook URL configured");
    return;
  }
  testing.value = true;
  try {
    const res = await fetch(notifyUrl.value.trim(), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: "Recodarr",
        message: "Test notification from Recodarr.",
        status: "test",
      }),
    });
    if (res.ok) notify.success(`Test sent (HTTP ${res.status})`);
    else notify.error(`Server responded with HTTP ${res.status}`);
  } catch (e) {
    notify.error(e);
  } finally {
    testing.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="panel">
    <p class="muted">
      Send a webhook notification when an encode finishes or fails.
      Compatible with ntfy, Gotify, and any HTTP endpoint accepting JSON.
    </p>
    <div class="form">
      <label>
        <span>Webhook URL</span>
        <InputText v-model="notifyUrl" placeholder="https://ntfy.sh/my-topic" />
      </label>
      <label class="row">
        <span>Notify on done</span>
        <ToggleSwitch v-model="notifyOnDone" />
      </label>
      <label class="row">
        <span>Notify on fail</span>
        <ToggleSwitch v-model="notifyOnFail" />
      </label>
    </div>

    <p class="muted small">
      Recodarr posts JSON with <code>title</code>, <code>message</code>, <code>status</code>,
      <code>filePath</code>, and <code>savedBytes</code> fields.
      For ntfy, point directly at a topic URL (e.g. <code>https://ntfy.sh/my-topic</code>).
    </p>

    <div class="actions">
      <Button label="Save" icon="pi pi-check" @click="save" />
      <Button text icon="pi pi-send" label="Send test" :loading="testing" @click="test" />
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  max-width: 38rem;
}
.muted {
  color: var(--app-muted);
  margin: 0;
}
.small {
  font-size: 0.85rem;
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
.info {
  background: var(--app-info-bg);
  color: var(--app-info-fg);
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
  grid-template-columns: 10rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.form label.row {
  grid-template-columns: 10rem auto;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
</style>
