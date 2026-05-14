<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import Toast from "primevue/toast";
import ConfirmDialog from "primevue/confirmdialog";
import { api } from "@/api/client";
import { useTheme } from "@/composables/useTheme";

const route = useRoute();
const router = useRouter();
const username = ref("");
const userMenuOpen = ref(false);
const { theme, cycle } = useTheme();

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
