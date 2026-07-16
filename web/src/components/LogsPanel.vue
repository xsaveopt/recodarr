<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import InputNumber from "primevue/inputnumber";
import Select from "primevue/select";
import ToggleSwitch from "primevue/toggleswitch";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { AppSettings } from "@/types/api";

const notify = useNotify();

const appLevel = ref<string>("INFO");
const rotateEnabled = ref<boolean>(true);
const maxSizeMB = ref<number>(50);
const maxAgeDays = ref<number>(30);
const maxBackups = ref<number>(5);
const compress = ref<boolean>(false);

const levels = [
  { label: "DEBUG — everything", value: "DEBUG" },
  { label: "INFO — status events (default)", value: "INFO" },
  { label: "WARN — warnings and errors only", value: "WARN" },
  { label: "ERROR — errors only", value: "ERROR" },
];

function readInt(v: string | undefined, fallback: number): number {
  if (v === undefined || v === "") return fallback;
  const n = parseInt(v, 10);
  return Number.isFinite(n) ? n : fallback;
}

async function load() {
  const s = await notify.tryRun(() => api.settings.get(), "Couldn't load settings");
  if (s) {
    appLevel.value = (s.log_app_level ?? "INFO").toUpperCase();
    rotateEnabled.value = s.log_rotate_enabled !== "false";
    maxSizeMB.value = readInt(s.log_max_size_mb, 50);
    maxAgeDays.value = readInt(s.log_max_age_days, 30);
    maxBackups.value = readInt(s.log_max_backups, 5);
    compress.value = s.log_compress === "true";
  }
}

async function save() {
  if (maxSizeMB.value < 1) {
    notify.error("Max file size must be ≥ 1 MB");
    return;
  }
  const updates: AppSettings = {
    log_app_level: appLevel.value,
    log_rotate_enabled: rotateEnabled.value ? "true" : "false",
    log_max_size_mb: String(maxSizeMB.value),
    log_max_age_days: String(maxAgeDays.value),
    log_max_backups: String(maxBackups.value),
    log_compress: compress.value ? "true" : "false",
  };
  await notify.tryAct(
    () => api.settings.put(updates),
    "Log settings saved",
    "Couldn't save settings",
  );
}

onMounted(load);
</script>

<template>
  <div class="panel">
    <section class="block">
      <h3 class="block-title">Container output (<code>docker logs</code>)</h3>
      <p class="muted">
        Threshold for the human-readable stream that <code>docker logs</code> shows. Lower the level
        for less noise — file logs are unaffected. Changes apply immediately.
      </p>
      <label class="row">
        <span>App log level</span>
        <Select v-model="appLevel" :options="levels" optionLabel="label" optionValue="value" />
      </label>
    </section>

    <section class="block">
      <h3 class="block-title">File rotation</h3>
      <p class="muted">
        Controls <code>access.log</code>, <code>outbound.log</code>, and
        <code>handbrake.log</code> under <code>&lt;data-dir&gt;/logs/</code>. Changes take effect on
        next restart.
      </p>
      <label class="row">
        <span>Enable rotation</span>
        <ToggleSwitch v-model="rotateEnabled" />
      </label>
      <p v-if="!rotateEnabled" class="muted small warn">
        Rotation is off — log files will grow without bound. You're responsible for cleaning them
        up.
      </p>
      <div v-if="rotateEnabled" class="form">
        <label class="row">
          <span>Max file size (MB)</span>
          <InputNumber v-model="maxSizeMB" :min="1" :max="1024" showButtons />
        </label>
        <label class="row">
          <span>Max age (days)</span>
          <InputNumber v-model="maxAgeDays" :min="0" :max="3650" showButtons />
        </label>
        <label class="row">
          <span>Max backups kept</span>
          <InputNumber v-model="maxBackups" :min="0" :max="100" showButtons />
        </label>
        <label class="row">
          <span>Gzip rotated files</span>
          <ToggleSwitch v-model="compress" />
        </label>
        <p class="muted small">
          Set days or backups to <code>0</code> to keep forever. A single log file is capped at the
          size above; once exceeded it's rotated and a new one is started.
        </p>
      </div>
    </section>

    <div class="actions">
      <Button label="Save" icon="pi pi-check" @click="save" />
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
  max-width: 42rem;
}
.block {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
}
.block-title {
  margin: 0;
  font-size: 0.95rem;
}
.muted {
  margin: 0;
  color: var(--rc-muted);
  font-size: 0.85rem;
}
.small {
  font-size: 0.78rem;
}
.warn {
  color: var(--rc-warn);
}
.form {
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
}
.row {
  display: grid;
  grid-template-columns: 14rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
</style>
