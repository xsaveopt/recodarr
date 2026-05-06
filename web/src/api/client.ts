import type {
  AppSettings,
  ArrInstance,
  DebugInfo,
  HbCaps,
  InstanceTag,
  Job,
  JobStats,
  Profile,
  QbitInstance,
  TagMapping,
  WorkerStatus,
} from "@/types/api";

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Recodarr": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(`/api${path}`, {
    method,
    credentials: "same-origin",
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401 && !path.startsWith("/auth/")) {
    // Session expired or never existed — bounce to login. Preserve current path so
    // the user can return after logging back in.
    const here = window.location.pathname + window.location.search;
    if (!window.location.pathname.startsWith("/login") && !window.location.pathname.startsWith("/setup")) {
      window.location.assign(`/login?next=${encodeURIComponent(here)}`);
    }
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${method} ${path}: ${res.status} ${text}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  handbrake: {
    caps: () => request<HbCaps>("GET", "/handbrake/caps"),
  },
  debug: {
    info: () => request<DebugInfo>("GET", "/debug"),
  },
  stats: {
    get: () => request<JobStats>("GET", "/stats"),
  },
  settings: {
    get: () => request<AppSettings>("GET", "/settings"),
    put: (s: AppSettings) => request<void>("PUT", "/settings", s),
  },
  arr: {
    list: () => request<ArrInstance[]>("GET", "/arr-instances"),
    create: (a: Omit<ArrInstance, "id">) => request<ArrInstance>("POST", "/arr-instances", a),
    update: (a: ArrInstance) => request<ArrInstance>("PUT", `/arr-instances/${a.id}`, a),
    remove: (id: number) => request<void>("DELETE", `/arr-instances/${id}`),
    allTags: () => request<InstanceTag[]>("GET", "/arr-instances/all-tags"),
    test: (id: number) => request<{ ok: boolean; error?: string }>("POST", `/arr-instances/${id}/test`),
  },
  tagMappings: {
    list: () => request<TagMapping[]>("GET", "/tag-mappings"),
    create: (m: Omit<TagMapping, "id">) => request<TagMapping>("POST", "/tag-mappings", m),
    remove: (id: number) => request<void>("DELETE", `/tag-mappings/${id}`),
  },
  qbit: {
    list: () => request<QbitInstance[]>("GET", "/qbit-instances"),
    upsert: (q: Partial<QbitInstance> & { name: string; url: string; username: string }) =>
      request<QbitInstance>("POST", "/qbit-instances", q),
    remove: (id: number) => request<void>("DELETE", `/qbit-instances/${id}`),
    testCredentials: (url: string, username: string, password: string) =>
      request<{ ok: boolean; error?: string }>("POST", "/qbit-instances/test", { url, username, password }),
    test: (id: number) => request<{ ok: boolean; error?: string }>("POST", `/qbit-instances/${id}/test`),
  },
  profiles: {
    list: () => request<Profile[]>("GET", "/profiles"),
    upsert: (p: Partial<Profile> & { name: string; encoder: string }) =>
      request<Profile>("POST", "/profiles", p),
    remove: (id: number) => request<void>("DELETE", `/profiles/${id}`),
  },
  jobs: {
    list: () => request<Job[]>("GET", "/jobs"),
    retry: (id: number) => request<Job>("POST", `/jobs/${id}/retry`),
    retryAllFailed: () => request<{ retried: number }>("POST", "/jobs/retry-failed"),
    cancel: (id: number) => request<{ status: string }>("POST", `/jobs/${id}/cancel`),
    remove: (id: number) => request<void>("DELETE", `/jobs/${id}`),
    clearTerminal: () => request<{ deleted: number }>("DELETE", "/jobs"),
  },
  worker: {
    status: () => request<WorkerStatus>("GET", "/worker/status"),
  },
  auth: {
    status: () => request<AuthStatus>("GET", "/auth/status"),
    setup: (username: string, password: string) =>
      request<{ username: string }>("POST", "/auth/setup", { username, password }),
    login: (username: string, password: string) =>
      request<{ username: string }>("POST", "/auth/login", { username, password }),
    logout: () => request<void>("POST", "/auth/logout"),
  },
};

export interface AuthStatus {
  setup: boolean;
  authed: boolean;
  username: string;
}
