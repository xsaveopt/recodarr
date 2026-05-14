<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import InputNumber from "primevue/inputnumber";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { AppSettings } from "@/types/api";

const notify = useNotify();
const intervalSeconds = ref<number>(30);
const maxParallel = ref<number>(1);
const windowStart = ref<string>("");
const windowEnd = ref<string>("");

async function load() {
  const s = await notify.tryRun(() => api.settings.get(), "Couldn't load settings");
  if (s) {
    intervalSeconds.value = parseInt(s.worker_interval_seconds ?? "30") || 30;
    maxParallel.value = parseInt(s.max_parallel_encodes ?? "1") || 1;
    windowStart.value = s.encoding_window_start ?? "";
    windowEnd.value = s.encoding_window_end ?? "";
  }
}

async function save() {
  if (intervalSeconds.value < 5) {
    notify.error("Interval must be at least 5 seconds");
    return;
  }
  if (maxParallel.value < 1 || maxParallel.value > 16) {
    notify.error("Parallel encodes must be 1..16");
    return;
  }
  const updates: AppSettings = {
    worker_interval_seconds: String(intervalSeconds.value),
    max_parallel_encodes: String(maxParallel.value),
    encoding_window_start: windowStart.value.trim(),
    encoding_window_end: windowEnd.value.trim(),
  };
  const ok = await notify.tryRun(() => api.settings.put(updates), "Couldn't save settings");
  if (ok !== undefined) notify.success("Worker settings saved");
}

function clearWindow() {
  windowStart.value = "";
  windowEnd.value = "";
}

onMounted(load);
</script>

<template>
  <div class="panel">
    <p class="muted">
      Controls how frequently Recodarr checks for seed completion and starts encodes.
    </p>
    <div class="form">
      <div class="section-title">Poll interval</div>

      <label>
        <span>Interval (seconds)</span>
        <InputNumber v-model="intervalSeconds" :min="5" :max="3600" showButtons />
      </label>
      <p class="muted small">
        How often the worker polls qBittorrent and picks up ready jobs. Default: 30.
      </p>

      <div class="section-title">Concurrency</div>

      <label>
        <span>Parallel encodes</span>
        <InputNumber v-model="maxParallel" :min="1" :max="16" showButtons />
      </label>
      <p class="muted small">
        How many encodes can run at once. Default 1. Hardware encoders (NVENC / QSV / VAAPI) share
        one engine on most cards, so &gt;1 there gives no speed-up. Software encoders (x264 / x265)
        scale with CPU cores; usually 2 is the sweet spot on 8+ cores.
      </p>

      <div class="section-title">Encoding window</div>

      <p class="muted small">
        Restrict encoding to a specific time window. Leave both fields empty to encode at any time.
        Overnight ranges are supported (e.g. 22:00 – 06:00).
      </p>
      <label>
        <span>Start (HH:MM)</span>
        <InputText v-model="windowStart" placeholder="22:00" />
      </label>
      <label>
        <span>End (HH:MM)</span>
        <InputText v-model="windowEnd" placeholder="06:00" />
      </label>
      <div>
        <Button text size="small" label="Clear window (always encode)" @click="clearWindow" />
      </div>
    </div>

    <div class="actions">
      <Button label="Save" icon="pi pi-check" @click="save" />
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
.section-title {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--app-muted);
  margin-top: 0.25rem;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
</style>
