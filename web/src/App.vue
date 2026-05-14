<script setup lang="ts">
import { ref, onMounted, watch } from "vue";
import { RouterLink, RouterView, useRoute, useRouter } from "vue-router";
import Toast from "primevue/toast";
import ConfirmDialog from "primevue/confirmdialog";
import Button from "primevue/button";
import { api } from "@/api/client";

const route = useRoute();
const router = useRouter();
const username = ref("");

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

async function logout() {
  try {
    await api.auth.logout();
  } finally {
    router.replace({ name: "login" });
  }
}
</script>

<template>
  <div v-if="route.meta.public" class="auth-layout">
    <RouterView />
    <Toast position="bottom-right" />
  </div>
  <div v-else class="app">
    <aside class="sidebar">
      <h1 class="brand">Recodarr</h1>
      <nav>
        <RouterLink to="/">Dashboard</RouterLink>
        <RouterLink to="/jobs">Jobs</RouterLink>
        <RouterLink to="/settings">Settings</RouterLink>
        <RouterLink to="/debug">Debug</RouterLink>
      </nav>
      <div class="user">
        <span v-if="username" class="who">{{ username }}</span>
        <Button label="Sign out" size="small" severity="secondary" text @click="logout" />
      </div>
    </aside>
    <main class="content">
      <RouterView />
    </main>
    <Toast position="bottom-right" />
    <ConfirmDialog />
  </div>
</template>

<style scoped>
.app {
  display: grid;
  grid-template-columns: 220px 1fr;
  min-height: 100vh;
}
.sidebar {
  background: var(--p-surface-100, #f1f3f5);
  color: var(--app-fg);
  border-right: 1px solid var(--app-panel-border);
  padding: 1.25rem 1rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.brand {
  margin: 0;
  font-size: 1.25rem;
  letter-spacing: 0.04em;
}
nav {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
nav a {
  color: var(--app-fg);
  text-decoration: none;
  padding: 0.5rem 0.75rem;
  border-radius: 6px;
}
nav a:hover {
  background: var(--app-row-alt);
}
nav a.router-link-active {
  background: var(--p-primary-color, #3b82f6);
  color: var(--p-primary-contrast-color, #fff);
}
.content {
  padding: 1.5rem 2rem;
}
.user {
  margin-top: auto;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  font-size: 0.85rem;
}
.who {
  color: var(--app-muted);
  padding: 0 0.75rem;
}
</style>
