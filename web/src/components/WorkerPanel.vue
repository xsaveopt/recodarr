<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import InputNumber from "primevue/inputnumber";
import ToggleSwitch from "primevue/toggleswitch";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import { useWorkerStatus } from "@/composables/useWorkerStatus";
import type { AppSettings } from "@/types/api";

const notify = useNotify();
const { status: workerStatus, refresh: refreshWorker } = useWorkerStatus();

const intervalSeconds = ref<number>(30);
const maxParallel = ref<number>(1);
const windowStart = ref<string>("");
const windowEnd = ref<string>("");
const enabled = ref<boolean>(true);
const suffixEnabled = ref<boolean>(false);
const outputSuffix = ref<string>("recodarr");

watch(
  () => workerStatus.value?.paused,
  (p) => {
    if (p === undefined) return;
    enabled.value = !p;
  },
  { immediate: true },
);

async function load() {
  const s = await notify.tryRun(() => api.settings.get(), "Couldn't load settings");
  if (s) {
    intervalSeconds.value = parseInt(s.worker_interval_seconds ?? "30") || 30;
    maxParallel.value = parseInt(s.max_parallel_encodes ?? "1") || 1;
    windowStart.value = s.encoding_window_start ?? "";
    windowEnd.value = s.encoding_window_end ?? "";
    if (workerStatus.value == null) enabled.value = s.encoding_paused !== "true";
    suffixEnabled.value = s.output_suffix_enabled === "true";
    outputSuffix.value = (s.output_suffix ?? "").trim() || "recodarr";
  }
}

const suffixValid = computed(() => /^[A-Za-z0-9_-]{1,32}$/.test(outputSuffix.value));
const suffixPreview = computed(
  () => `Movie (2024).mkv  →  Movie (2024).${outputSuffix.value || "recodarr"}`,
);

async function toggleEnabled(next: boolean) {
  const pause = !next;
  const res = await notify.tryRun(
    () => api.worker.setPaused(pause),
    pause ? "Couldn't pause" : "Couldn't resume",
  );
  if (res === undefined) {
    enabled.value = !next;
    return;
  }
  if (pause) {
    notify.success(
      res.cancelled > 0
        ? `Worker disabled — ${res.cancelled} in-flight encode(s) re-queued`
        : "Worker disabled",
    );
  } else {
    notify.success("Worker enabled");
  }
  await refreshWorker();
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
  if (!suffixValid.value) {
    notify.error("Output suffix: 1–32 chars, letters/digits/dash/underscore only");
    return;
  }
  const updates: AppSettings = {
    worker_interval_seconds: String(intervalSeconds.value),
    max_parallel_encodes: String(maxParallel.value),
    encoding_window_start: windowStart.value.trim(),
    encoding_window_end: windowEnd.value.trim(),
    output_suffix_enabled: suffixEnabled.value ? "true" : "false",
    output_suffix: outputSuffix.value.trim(),
  };
  await notify.tryAct(
    () => api.settings.put(updates),
    "Worker settings saved",
    "Couldn't save settings",
  );
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

    <div class="instant-card" :class="{ paused: !enabled }">
      <label class="pause-row">
        <span class="pause-label">
          <span>Worker enabled</span>
          <span class="muted small">Applies immediately — no save needed</span>
        </span>
        <span class="pause-control">
          <ToggleSwitch v-model="enabled" @update:modelValue="toggleEnabled" />
          <span class="muted small">{{
            enabled ? "Worker is running" : "Disabled — jobs continue to queue"
          }}</span>
        </span>
      </label>
      <p class="muted small">
        Master on/off switch. When turned off, any in-flight encode is cancelled and re-queued (no
        attempt counted), and no new encodes will start until you turn it back on. Webhooks and the
        queue keep working normally.
      </p>
    </div>

    <div class="form">
      <p class="muted small form-intro">
        Settings below are staged — click <strong>Save</strong> at the bottom to apply.
      </p>

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

      <div class="section-title">Output marker</div>

      <label class="pause-row">
        <span>Write marker</span>
        <span class="pause-control">
          <ToggleSwitch v-model="suffixEnabled" />
          <span class="muted small">{{
            suffixEnabled
              ? "A small marker file will be written next to each encoded file"
              : "No marker is written"
          }}</span>
        </span>
      </label>
      <p class="muted small">
        After every successful encode, Recodarr drops a tiny text file beside the media file with
        the same name but the extension below. The encoded file itself is not renamed. Future
        webhooks for any file with a matching marker are skipped so the same file can't accidentally
        be re-encoded a second time.
      </p>
      <label>
        <span>Marker extension</span>
        <InputText v-model="outputSuffix" :disabled="!suffixEnabled" placeholder="recodarr" />
      </label>
      <p class="muted small">
        Letters, digits, dashes, and underscores only — no dots or path separators. Preview:
        <code>{{ suffixPreview }}</code>
      </p>
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
.instant-card {
  border: 1px solid var(--app-border, rgba(255, 255, 255, 0.08));
  border-radius: 6px;
  padding: 0.85rem 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  background: var(--app-surface, transparent);
}
.instant-card.paused {
  border-color: var(--app-warn-fg, #d39e00);
}
.pause-label {
  display: flex;
  flex-direction: column;
}
.form-intro {
  margin: 0;
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
.pause-control {
  display: inline-flex;
  align-items: center;
  gap: 0.6rem;
}
</style>
