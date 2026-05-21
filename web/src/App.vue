<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import Toast from "primevue/toast";
import ConfirmDialog from "primevue/confirmdialog";
import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import { useTheme } from "@/composables/useTheme";
import { useWorkerStatus } from "@/composables/useWorkerStatus";

const route = useRoute();
const router = useRouter();
const username = ref("");
const userMenuOpen = ref(false);
const { theme, cycle } = useTheme();
const notify = useNotify();
const { status: workerStatus, refresh: refreshWorker } = useWorkerStatus();
const pauseBusy = ref(false);

const slotsLabel = computed(() => {
  const ws = workerStatus.value;
  if (!ws || !ws.maxParallelEncodes) return "";
  return `${ws.encodingJobIds.length}/${ws.maxParallelEncodes}`;
});

const windowPaused = computed(() => {
  const w = workerStatus.value?.window;
  return Boolean(w?.hasLimit && !w.active);
});

async function togglePause() {
  if (!workerStatus.value || pauseBusy.value) return;
  const next = !workerStatus.value.paused;
  pauseBusy.value = true;
  const res = await notify.tryRun(
    () => api.worker.setPaused(next),
    next ? "Couldn't pause" : "Couldn't resume",
  );
  pauseBusy.value = false;
  if (res !== undefined) {
    if (next) {
      notify.success(
        res.cancelled > 0
          ? `Encoding paused — ${res.cancelled} in-flight encode(s) re-queued`
          : "Encoding paused",
      );
    } else {
      notify.success("Encoding resumed");
    }
    await refreshWorker();
  }
}

async function refreshUser() {
  if (route.meta.public) {
    username.value = "";
    return;
  }
  try {
    const s = await api.auth.status();
    username.value = s.authed ? s.username : "";
  } catch {
    username.value = "";
  }
}

onMounted(refreshUser);
watch(() => route.fullPath, refreshUser);
watch(
  () => route.fullPath,
  () => (userMenuOpen.value = false),
);

async function logout() {
  try {
    await api.auth.logout();
  } finally {
    router.replace({ name: "login" });
  }
}

const themeIcon = computed(() =>
  theme.value === "dark" ? "pi-moon" : theme.value === "light" ? "pi-sun" : "pi-desktop",
);
const themeLabel = computed(() =>
  theme.value === "dark" ? "Dark" : theme.value === "light" ? "Light" : "System",
);

const navItems = [
  { to: "/", label: "Dashboard" },
  { to: "/jobs", label: "Jobs" },
  { to: "/library", label: "Library" },
  { to: "/settings", label: "Settings" },
  { to: "/debug", label: "Debug" },
];
</script>

<template>
  <div v-if="route.meta.public" class="auth-layout">
    <RouterView />
    <Toast position="bottom-right" />
  </div>
  <div v-else class="app">
    <header class="topbar">
      <div class="topbar-inner">
        <div class="brand-block">
          <RouterLink to="/" class="brand">
            <span class="brand-mark" aria-hidden="true">◆</span>
            <span class="brand-name">Recodarr</span>
          </RouterLink>
          <nav class="nav">
            <RouterLink
              v-for="item in navItems"
              :key="item.to"
              :to="item.to"
              class="nav-link"
              :class="{ active: route.path === item.to }"
            >
              {{ item.label }}
            </RouterLink>
          </nav>
        </div>
        <div class="actions">
          <span
            v-if="username && workerStatus && slotsLabel"
            class="slots-pill"
            :class="{ active: workerStatus.isEncoding && !workerStatus.paused }"
            :title="`${workerStatus.encodingJobIds.length} active encode(s) of ${workerStatus.maxParallelEncodes} slot(s)`"
          >
            <i class="pi pi-bolt"></i>
            <span>{{ slotsLabel }}</span>
          </span>
          <span
            v-if="username && windowPaused"
            class="window-pill"
            :title="`Encoding window ${workerStatus!.window.start}–${workerStatus!.window.end} — currently outside the window`"
          >
            <i class="pi pi-clock"></i>
            <span class="window-pill-label">outside window</span>
          </span>
          <button
            v-if="username && workerStatus"
            class="pause-btn"
            :class="{ 'pause-btn-paused': workerStatus.paused }"
            type="button"
            :disabled="pauseBusy"
            :title="workerStatus.paused ? 'Resume encoding' : 'Pause encoding'"
            @click="togglePause"
          >
            <i class="pi" :class="workerStatus.paused ? 'pi-play' : 'pi-pause'"></i>
            <span class="pause-btn-label">{{ workerStatus.paused ? "Resume" : "Pause" }}</span>
          </button>
          <button
            class="icon-btn"
            type="button"
            :title="`Theme: ${themeLabel} (click to cycle)`"
            @click="cycle"
          >
            <i class="pi" :class="themeIcon"></i>
          </button>
          <div v-if="username" class="user-wrap">
            <button
              class="user-btn"
              type="button"
              :aria-expanded="userMenuOpen"
              @click="userMenuOpen = !userMenuOpen"
            >
              <span class="avatar">{{ username.charAt(0).toUpperCase() }}</span>
              <span class="user-name">{{ username }}</span>
              <i class="pi pi-angle-down user-caret"></i>
            </button>
            <div v-if="userMenuOpen" class="user-menu" @click="userMenuOpen = false">
              <button class="menu-item" type="button" @click="logout">
                <i class="pi pi-sign-out"></i>
                <span>Sign out</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </header>
    <main class="content">
      <div class="content-inner">
        <RouterView />
      </div>
    </main>
    <Toast position="bottom-right" />
    <ConfirmDialog />
  </div>
</template>

<style scoped>
.app {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.topbar {
  position: sticky;
  top: 0;
  z-index: 20;
  background: var(--rc-overlay);
  backdrop-filter: saturate(180%) blur(12px);
  -webkit-backdrop-filter: saturate(180%) blur(12px);
  border-bottom: 1px solid var(--rc-border);
}
.topbar-inner {
  max-width: 1400px;
  margin: 0 auto;
  height: 48px;
  padding: 0 1.25rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
.brand-block {
  display: flex;
  align-items: center;
  gap: 1.5rem;
  min-width: 0;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  color: var(--rc-fg);
  font-weight: 600;
  letter-spacing: -0.01em;
  font-size: 0.95rem;
  text-decoration: none;
}
.brand:hover {
  text-decoration: none;
}
.brand-mark {
  color: var(--rc-accent);
  font-size: 0.9rem;
}
.brand-name {
  letter-spacing: -0.015em;
}
.nav {
  display: flex;
  align-items: center;
  gap: 0.125rem;
}
.nav-link {
  color: var(--rc-muted);
  text-decoration: none;
  padding: 0.35rem 0.65rem;
  border-radius: var(--rc-r-sm);
  font-size: 0.825rem;
  font-weight: 500;
  transition:
    color 0.08s ease,
    background 0.08s ease;
}
.nav-link:hover {
  color: var(--rc-fg);
  background: var(--rc-surface-2);
  text-decoration: none;
}
.nav-link.active {
  color: var(--rc-fg);
  background: var(--rc-surface-2);
}
.actions {
  display: flex;
  align-items: center;
  gap: 0.25rem;
}
.icon-btn {
  background: transparent;
  border: none;
  color: var(--rc-muted);
  width: 28px;
  height: 28px;
  border-radius: var(--rc-r-sm);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition:
    color 0.08s ease,
    background 0.08s ease;
}
.icon-btn:hover {
  color: var(--rc-fg);
  background: var(--rc-surface-2);
}
.icon-btn .pi {
  font-size: 0.85rem;
}

.slots-pill,
.window-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  height: 24px;
  padding: 0 0.5rem;
  border-radius: 999px;
  font-size: 0.72rem;
  font-weight: 500;
  color: var(--rc-muted);
  background: var(--rc-surface-2);
  margin-right: 0.4rem;
  user-select: none;
}
.slots-pill .pi,
.window-pill .pi {
  font-size: 0.7rem;
}
.slots-pill.active {
  color: var(--rc-accent);
}
.window-pill {
  color: var(--rc-warn, #d39e00);
}
@media (max-width: 640px) {
  .slots-pill,
  .window-pill {
    display: none;
  }
}

.pause-btn {
  background: transparent;
  border: 1px solid var(--rc-border);
  color: var(--rc-fg-2);
  height: 28px;
  padding: 0 0.6rem;
  border-radius: var(--rc-r-sm);
  font-size: 0.8rem;
  font-weight: 500;
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  cursor: pointer;
  margin-right: 0.4rem;
  transition:
    color 0.08s ease,
    background 0.08s ease,
    border-color 0.08s ease;
}
.pause-btn:hover:not(:disabled) {
  color: var(--rc-fg);
  background: var(--rc-surface-2);
}
.pause-btn:disabled {
  opacity: 0.6;
  cursor: progress;
}
.pause-btn .pi {
  font-size: 0.75rem;
}
.pause-btn-paused {
  color: var(--rc-accent);
  border-color: var(--rc-accent);
}
.pause-btn-paused:hover:not(:disabled) {
  color: var(--rc-accent);
}
@media (max-width: 640px) {
  .pause-btn-label {
    display: none;
  }
  .pause-btn {
    padding: 0 0.5rem;
  }
}

.user-wrap {
  position: relative;
}
.user-btn {
  background: transparent;
  border: none;
  color: var(--rc-fg);
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.25rem 0.5rem 0.25rem 0.3rem;
  border-radius: var(--rc-r-sm);
  cursor: pointer;
  font-size: 0.825rem;
  transition: background 0.08s ease;
}
.user-btn:hover {
  background: var(--rc-surface-2);
}
.avatar {
  width: 22px;
  height: 22px;
  border-radius: 999px;
  background: var(--rc-accent);
  color: var(--rc-accent-fg);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 0.7rem;
  font-weight: 600;
}
.user-caret {
  font-size: 0.7rem;
  color: var(--rc-muted);
}
.user-name {
  color: var(--rc-fg-2);
}
.user-menu {
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  min-width: 160px;
  background: var(--rc-surface);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  box-shadow: var(--rc-shadow);
  padding: 4px;
  z-index: 30;
}
.menu-item {
  width: 100%;
  background: transparent;
  border: none;
  color: var(--rc-fg);
  font: inherit;
  text-align: left;
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.45rem 0.6rem;
  border-radius: var(--rc-r-sm);
  cursor: pointer;
}
.menu-item:hover {
  background: var(--rc-surface-2);
}
.menu-item .pi {
  font-size: 0.8rem;
  color: var(--rc-muted);
}

.content {
  flex: 1;
}
.content-inner {
  max-width: 1400px;
  margin: 0 auto;
  padding: 1.25rem;
}
</style>
