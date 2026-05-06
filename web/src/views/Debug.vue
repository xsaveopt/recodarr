<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { DebugInfo } from "@/types/api";

const notify = useNotify();
const info = ref<DebugInfo | null>(null);
const loading = ref(true);

async function load() {
  const res = await notify.tryRun(() => api.debug.info(), "Couldn't load debug info");
  if (res) info.value = res;
  loading.value = false;
}

onMounted(load);
</script>

<template>
  <div class="page">
    <h2 class="page-title">Debug</h2>
    <div v-if="loading" class="muted">Loading…</div>
    <div v-else-if="info" class="sections">
      <section>
        <h3>HandBrake</h3>
        <table>
          <tr>
            <th>Binary found</th>
            <td>
              <span :class="info.hbFound ? 'ok' : 'bad'">{{ info.hbFound ? "yes" : "no" }}</span>
            </td>
          </tr>
          <tr>
            <th>Version</th>
            <td><code>{{ info.hbVersion }}</code></td>
          </tr>
          <tr>
            <th>Detected encoders</th>
            <td>
              <span v-if="info.encoders.length">
                <code v-for="enc in info.encoders" :key="enc" class="enc-tag">{{ enc }}</code>
              </span>
              <span v-else class="muted">none</span>
            </td>
          </tr>
        </table>
      </section>

      <section>
        <h3>Hardware acceleration</h3>
        <table>
          <tr>
            <th>VAAPI (Linux DRI)</th>
            <td>
              <span :class="info.vaapiAvailable ? 'ok' : 'na'">
                {{ info.vaapiAvailable ? "available" : "not detected" }}
              </span>
            </td>
          </tr>
          <tr>
            <th>Intel QSV</th>
            <td>
              <span :class="info.qsvAvailable ? 'ok' : 'na'">
                {{ info.qsvAvailable ? "available" : "not detected" }}
              </span>
            </td>
          </tr>
          <tr>
            <th>NVIDIA NVENC</th>
            <td>
              <span :class="info.nvencAvailable ? 'ok' : 'na'">
                {{ info.nvencAvailable ? "available" : "not detected" }}
              </span>
            </td>
          </tr>
        </table>
        <p class="muted small">
          Detection is based on device nodes and kernel modules. Hardware encoders only work inside
          Docker when the device is passed through (<code>--device</code> or
          <code>devices:</code> in compose).
        </p>
      </section>

      <section>
        <h3>Runtime</h3>
        <table>
          <tr>
            <th>Platform</th>
            <td><code>{{ info.platform }}/{{ info.arch }}</code></td>
          </tr>
        </table>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
  max-width: 48rem;
}
.page-title {
  margin: 0;
  font-size: 1.1rem;
  font-weight: 600;
}
.sections {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
}
section {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
h3 {
  margin: 0;
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #888;
}
table {
  border-collapse: collapse;
  width: 100%;
}
th,
td {
  padding: 0.45rem 0.6rem;
  text-align: left;
  border-bottom: 1px solid var(--p-surface-200, #e5e5e5);
  font-size: 0.9rem;
}
th {
  width: 14rem;
  color: #555;
  font-weight: 500;
}
.ok {
  color: #1a7f37;
  font-weight: 500;
}
.bad {
  color: #c00;
  font-weight: 500;
}
.na {
  color: #999;
}
.muted {
  color: #888;
}
.small {
  font-size: 0.82rem;
  margin: 0.25rem 0 0;
}
.error {
  background: #fee;
  color: #900;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
}
.enc-tag {
  display: inline-block;
  background: var(--p-surface-100, #f3f3f3);
  border-radius: 3px;
  padding: 0.1rem 0.4rem;
  margin: 0.1rem 0.2rem 0.1rem 0;
  font-size: 0.8rem;
}
</style>
