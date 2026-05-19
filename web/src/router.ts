import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { api } from "@/api/client";

const routes: RouteRecordRaw[] = [
  {
    path: "/login",
    name: "login",
    component: () => import("./views/Login.vue"),
    meta: { public: true },
  },
  {
    path: "/setup",
    name: "setup",
    component: () => import("./views/Setup.vue"),
    meta: { public: true },
  },
  { path: "/", name: "dashboard", component: () => import("./views/Dashboard.vue") },
  { path: "/jobs", name: "jobs", component: () => import("./views/Jobs.vue") },
  { path: "/library", name: "library", component: () => import("./views/Library.vue") },
  { path: "/settings", name: "settings", component: () => import("./views/Settings.vue") },
  { path: "/debug", name: "debug", component: () => import("./views/Debug.vue") },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

// Auth guard: hits /api/auth/status on every navigation. Cheap (single SQLite COUNT +
// session lookup), and avoids stale client-side auth state.
router.beforeEach(async (to) => {
  let status;
  try {
    status = await api.auth.status();
  } catch {
    return true; // network blip — let the page render so the failure is visible
  }
  if (!status.setup) {
    return to.name === "setup" ? true : { name: "setup" };
  }
  if (!status.authed) {
    if (to.meta.public) return to.name === "setup" ? { name: "login" } : true;
    return { name: "login", query: { next: to.fullPath } };
  }
  if (to.name === "login" || to.name === "setup") return { name: "dashboard" };
  return true;
});
