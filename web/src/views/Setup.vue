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
      <h1>First-run setup</h1>
      <label>
        <span>Username</span>
        <InputText v-model="username" autocomplete="username" autofocus required fluid />
      </label>
      <label>
        <span>Password</span>
        <Password v-model="password" :feedback="false" toggleMask autocomplete="new-password" required inputId="pw1" fluid />
      </label>
      <label>
        <span>Confirm password</span>
        <Password v-model="confirm" :feedback="false" toggleMask autocomplete="new-password" required inputId="pw2" fluid />
      </label>
      <Message v-if="error" severity="error" :closable="false">{{ error }}</Message>
      <Button type="submit" label="Create admin" :loading="submitting" />
      <p class="muted small">Forgot your password later? Run <code>recodarr reset-admin</code> in the container shell.</p>
    </form>
  </div>
</template>

<style scoped>
.auth-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background: #0e1116;
}
.auth-card {
  width: min(420px, 90vw);
  background: #161b23;
  color: #eee;
  padding: 2rem;
  border-radius: 12px;
  display: flex;
  flex-direction: column;
  gap: 1rem;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.4);
}
.auth-card h1 {
  margin: 0;
}
/* Auth card is always dark — pin readable muted color and code bg. */
.auth-card .muted {
  color: #9aa3ad;
  margin: 0;
}
.small {
  font-size: 0.85rem;
}
.auth-card code {
  background: #232a36;
  color: #eaeaea;
  padding: 0.1rem 0.35rem;
  border-radius: 4px;
}
</style>
