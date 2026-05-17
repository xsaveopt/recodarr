<script setup lang="ts">
import { onMounted, ref } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Password from "primevue/password";
import ToggleSwitch from "primevue/toggleswitch";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { AppSettings } from "@/types/api";

const notify = useNotify();
const enabled = ref<boolean>(false);
const url = ref<string>("");
const token = ref<string>("");
const tokenStored = ref<boolean>(false);
const fallbackLocal = ref<boolean>(true);
const testing = ref(false);
const agentSlots = ref<number | null>(null);
const agentActive = ref<number | null>(null);
const serverMaxParallel = ref<number>(1);

async function load() {
  const s = await notify.tryRun(() => api.settings.get(), "Couldn't load settings");
  if (s) {
    enabled.value = s.agent_enabled === "true";
    url.value = s.agent_url ?? "";
    tokenStored.value = s.hasAgentToken === "true";
    token.value = "";
    fallbackLocal.value = s.agent_fallback_local !== "false";
    serverMaxParallel.value = parseInt(s.max_parallel_encodes ?? "1") || 1;
  }
  if (enabled.value && url.value) {
    // Best-effort probe so the panel can show actual agent capacity without
    // the user clicking Test first.
    try {
      const r = await api.agent.test({ url: url.value, token: "" });
      if (r.ok) {
        agentSlots.value = r.slots ?? null;
        agentActive.value = r.active ?? null;
      }
    } catch {
      /* ignore — Test button still works */
    }
  }
}

async function save() {
  if (enabled.value && !url.value.trim()) {
    notify.error("URL is required when the agent is enabled");
    return;
  }
  if (enabled.value && !token.value && !tokenStored.value) {
    notify.error("Token is required when the agent is enabled");
    return;
  }
  const updates: AppSettings = {
    agent_enabled: enabled.value ? "true" : "false",
    agent_url: url.value.trim(),
    agent_fallback_local: fallbackLocal.value ? "true" : "false",
  };
  if (token.value.trim() !== "") {
    updates.agent_token = token.value.trim();
  }
  const ok = await notify.tryAct(
    () => api.settings.put(updates),
    "Remote agent settings saved",
    "Couldn't save settings",
  );
  if (ok) {
    token.value = "";
    await load();
  }
}

async function test() {
  testing.value = true;
  try {
    const res = await api.agent.test({
      url: url.value.trim(),
      token: token.value.trim(),
    });
    if (res.ok) {
      agentSlots.value = res.slots ?? null;
      agentActive.value = res.active ?? null;
      notify.success(
        `Agent reachable — ${res.slots ?? "?"} slot(s), ${res.active ?? 0} active. ${res.hb ?? ""}`,
      );
    } else {
      agentSlots.value = null;
      agentActive.value = null;
      notify.error(`Agent unreachable: ${res.error}`);
    }
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
      Run a second Recodarr container with <code>RECODARR_MODE=agent</code> on a host with a
      better GPU. When enabled here, every encode is sent to the agent over HTTP and the result
      is streamed back to replace the original file in place. Cancel and progress work
      unchanged. See <code>docs/remote-agent.md</code> for setup.
    </p>

    <div class="form">
      <label class="row">
        <span>Use remote agent</span>
        <ToggleSwitch v-model="enabled" />
      </label>

      <label class="row">
        <span>Agent URL</span>
        <InputText v-model="url" placeholder="http://gpu-host:8090" />
      </label>

      <label class="row">
        <span>Agent token</span>
        <Password
          v-model="token"
          :feedback="false"
          toggleMask
          :placeholder="tokenStored ? '(stored, leave blank to keep)' : 'paste the agent\'s token'"
        />
      </label>

      <label class="row">
        <span>Fall back to local if agent is down</span>
        <ToggleSwitch v-model="fallbackLocal" />
      </label>
    </div>

    <div v-if="agentSlots !== null" class="capacity-card">
      <div class="capacity-row">
        <span>Agent capacity</span>
        <strong>{{ agentActive ?? 0 }} / {{ agentSlots }} slot(s)</strong>
      </div>
      <p v-if="agentSlots < serverMaxParallel" class="muted small warn">
        ⚠ The agent only accepts <strong>{{ agentSlots }}</strong> concurrent encode(s) but the
        server is configured for <strong>{{ serverMaxParallel }}</strong>. Raise
        <code>RECODARR_AGENT_MAX_PARALLEL</code> on the agent container and restart it to use the
        full server concurrency.
      </p>
      <p v-else-if="agentSlots > serverMaxParallel" class="muted small">
        The agent can take up to {{ agentSlots }} encodes at once, but the server only dispatches
        {{ serverMaxParallel }}. Raise <strong>Parallel encodes</strong> on the Worker tab to use
        the full agent capacity.
      </p>
    </div>

    <p class="muted small">
      Concurrency is governed by two settings: the <strong>Parallel encodes</strong> value on the
      Worker tab caps how many jobs the server dispatches at once, and
      <code>RECODARR_AGENT_MAX_PARALLEL</code> on the agent container caps how many it will accept.
      Set both to the same value, matching what your GPU can actually run in parallel.
    </p>

    <p class="muted small">
      Bandwidth: each encode uploads the full source and downloads the encoded result. On
      gigabit LAN expect ~1 GB/min each way; pick the agent over local when its GPU is fast
      enough to make up the round-trip.
    </p>

    <div class="actions">
      <Button label="Save" icon="pi pi-check" @click="save" />
      <Button text icon="pi pi-bolt" label="Test connection" :loading="testing" @click="test" />
    </div>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  max-width: 42rem;
}
.muted {
  margin: 0;
  color: var(--rc-muted);
  font-size: 0.85rem;
}
.small {
  font-size: 0.78rem;
}
.form {
  display: flex;
  flex-direction: column;
  gap: 0.55rem;
}
.row {
  display: grid;
  grid-template-columns: 16rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.actions {
  display: flex;
  gap: 0.5rem;
}
:deep(.p-password),
:deep(.p-password-input) {
  width: 100%;
}
.capacity-card {
  border: 1px solid var(--app-border, rgba(255, 255, 255, 0.08));
  border-radius: 6px;
  padding: 0.75rem 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
.capacity-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.warn {
  color: var(--app-warn-fg, #d39e00);
}
</style>
