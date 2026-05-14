<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Password from "primevue/password";
import Message from "primevue/message";
import { api } from "@/api/client";

const username = ref("admin");
const password = ref("");
const confirm = ref("");
const submitting = ref(false);
const error = ref("");
const router = useRouter();

async function submit() {
  error.value = "";
  if (password.value !== confirm.value) {
    error.value = "Passwords do not match.";
    return;
  }
  submitting.value = true;
  try {
    await api.auth.setup(username.value, password.value);
    router.replace("/");
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
      <h1>First-run setup</h1>
      <p class="muted">Create your admin account to get started.</p>
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
          autocomplete="new-password"
          required
          inputId="pw1"
          fluid
        />
      </label>
      <label>
        <span>Confirm password</span>
        <Password
          v-model="confirm"
          :feedback="false"
          toggleMask
          autocomplete="new-password"
          required
          inputId="pw2"
          fluid
        />
      </label>
      <Message v-if="error" severity="error" :closable="false">{{ error }}</Message>
      <Button type="submit" label="Create admin" :loading="submitting" />
      <p class="muted small">
        Forgot your password later? Run <code>recodarr reset-admin</code> in the container shell.
      </p>
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
  width: min(420px, 100%);
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
.small {
  font-size: 0.78rem;
  margin-top: 0.25rem;
}
.auth-card code {
  background: var(--rc-code-bg);
  color: var(--rc-fg);
  padding: 0.1rem 0.35rem;
  border-radius: var(--rc-r-sm);
  font-family: var(--rc-font-mono);
  font-size: 0.78rem;
}
</style>
