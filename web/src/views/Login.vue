<script setup lang="ts">
import { ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Password from "primevue/password";
import Message from "primevue/message";
import { api } from "@/api/client";

const username = ref("");
const password = ref("");
const submitting = ref(false);
const error = ref("");
const route = useRoute();
const router = useRouter();

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    await api.auth.login(username.value, password.value);
    const next = (route.query.next as string) || "/";
    router.replace(next);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="auth-shell">
    <form class="auth-card" @submit.prevent="submit">
      <div class="brand"><span class="mark">◆</span> Recodarr</div>
      <h1>Sign in</h1>
      <p class="muted">Welcome back.</p>
      <label>
        <span>Username</span>
        <InputText v-model="username" autocomplete="username" autofocus required fluid />
      </label>
      <label>
        <span>Password</span>
        <Password
          v-model="password"
          :feedback="false"
          toggleMask
          autocomplete="current-password"
          required
          inputId="pw"
          fluid
        />
      </label>
      <Message v-if="error" severity="error" :closable="false">{{ error }}</Message>
      <Button type="submit" label="Sign in" :loading="submitting" />
    </form>
  </div>
</template>

<style scoped>
.auth-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background: var(--rc-bg);
  padding: 1rem;
}
.auth-card {
  width: min(360px, 100%);
  background: var(--rc-surface);
  color: var(--rc-fg);
  padding: 1.75rem;
  border-radius: var(--rc-r-lg);
  border: 1px solid var(--rc-border);
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
  box-shadow: var(--rc-shadow);
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-weight: 600;
  font-size: 0.85rem;
  color: var(--rc-fg);
  letter-spacing: -0.01em;
}
.mark {
  color: var(--rc-accent);
}
.auth-card h1 {
  margin: 0.25rem 0 0;
  font-size: 1.4rem;
  letter-spacing: -0.02em;
}
.auth-card label {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: 0.825rem;
  color: var(--rc-fg-2);
}
.muted {
  color: var(--rc-muted);
  margin: 0 0 0.5rem;
  font-size: 0.85rem;
}
</style>
