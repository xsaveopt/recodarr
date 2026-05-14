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
      <h1>Recodarr</h1>
      <p class="muted">Sign in to continue.</p>
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
  background: #0e1116;
}
.auth-card {
  width: min(360px, 90vw);
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
.auth-card label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
/* Auth card is always dark, regardless of OS theme — pin a light muted color
   so it stays readable on the dark surface. */
.auth-card .muted {
  color: #9aa3ad;
  margin: 0;
}
</style>
