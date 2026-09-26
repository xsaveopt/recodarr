import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";

import type { AuthStatus } from "@/api/client";
import { click, flush, mountAt, type Mounted } from "@/testing/mount";
import type { WorkerStatus } from "@/types/api";

const { api, toastAdd, worker } = vi.hoisted(() => ({
  api: {
    status: vi.fn<() => Promise<AuthStatus>>(),
    logout: vi.fn<() => Promise<void>>(),
    setPaused: vi.fn<(p: boolean) => Promise<{ paused: boolean; cancelled: number }>>(),
  },
  toastAdd: vi.fn(),
  worker: {
    status: null as WorkerStatus | null,
    refresh: vi.fn<() => Promise<void>>(),
  },
}));

vi.mock("@/api/client", () => ({
  api: {
    auth: { status: () => api.status(), logout: () => api.logout() },
    worker: { setPaused: (p: boolean) => api.setPaused(p) },
  },
}));

vi.mock("@/composables/useWorkerStatus", () => ({
  useWorkerStatus: () => ({ status: ref(worker.status), refresh: worker.refresh }),
}));

vi.mock("@/composables/useTheme", () => ({
  useTheme: () => ({ theme: ref("system"), cycle: () => {} }),
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import App from "@/App.vue";

function workerStatus(over: Partial<WorkerStatus> = {}): WorkerStatus {
  return {
    isEncoding: true,
    encodingJobId: 3,
    encodingJobIds: [3],
    progress: [],
    lastTickAt: null,
    window: { start: "", end: "", active: true, hasLimit: false },
    maxParallelEncodes: 2,
    paused: false,
    ...over,
  };
}

let view: Mounted;

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  worker.refresh.mockReset();
  worker.refresh.mockResolvedValue(undefined);
  toastAdd.mockReset();
  worker.status = workerStatus();
  api.status.mockResolvedValue({ setup: true, authed: true, username: "admin" });
});

afterEach(() => {
  view.unmount();
});

describe("shell", () => {
  it("renders public pages bare and skips the session lookup", async () => {
    view = await mountAt(App, "/login");
    expect(view.root.querySelector(".topbar")).toBeNull();
    expect(api.status).not.toHaveBeenCalled();
  });

  it("shows the user and the encode slots when signed in", async () => {
    view = await mountAt(App, "/");
    expect(view.root.querySelector(".user-name")?.textContent).toBe("admin");
    expect(view.root.querySelector(".slots-pill")?.textContent?.trim()).toBe("1/2");
    expect(view.root.querySelector(".window-pill")).toBeNull();
  });

  it("flags when the worker is outside its encoding window", async () => {
    worker.status = workerStatus({
      window: { start: "01:00", end: "06:00", active: false, hasLimit: true },
    });
    view = await mountAt(App, "/");
    expect(view.root.querySelector(".window-pill")?.textContent).toContain("outside window");
  });

  it("hides the user controls when the session lookup fails", async () => {
    api.status.mockRejectedValue(new Error("offline"));
    view = await mountAt(App, "/");
    expect(view.root.querySelector(".user-wrap")).toBeNull();
    expect(view.root.querySelector(".pause-btn")).toBeNull();
  });
});

describe("pause toggle", () => {
  it("pauses and reports re-queued encodes", async () => {
    api.setPaused.mockResolvedValue({ paused: true, cancelled: 2 });
    view = await mountAt(App, "/");
    await click(view.root.querySelector(".pause-btn"));
    expect(api.setPaused).toHaveBeenCalledWith(true);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ detail: "Encoding paused — 2 in-flight encode(s) re-queued" }),
    );
    expect(worker.refresh).toHaveBeenCalled();
  });

  it("resumes a paused worker", async () => {
    worker.status = workerStatus({ paused: true });
    api.setPaused.mockResolvedValue({ paused: false, cancelled: 0 });
    view = await mountAt(App, "/");
    expect(view.root.querySelector(".pause-btn")?.textContent?.trim()).toBe("Resume");
    await click(view.root.querySelector(".pause-btn"));
    expect(api.setPaused).toHaveBeenCalledWith(false);
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ detail: "Encoding resumed" }));
  });

  it("does not refresh when the toggle fails", async () => {
    api.setPaused.mockRejectedValue(new Error("POST /worker/pause: 500"));
    view = await mountAt(App, "/");
    await click(view.root.querySelector(".pause-btn"));
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ summary: "Couldn't pause" }));
    expect(worker.refresh).not.toHaveBeenCalled();
  });
});

describe("sign out", () => {
  it("ends the session and goes to login", async () => {
    api.logout.mockResolvedValue(undefined);
    view = await mountAt(App, "/");
    await click(view.root.querySelector(".user-btn"));
    await click(view.root.querySelector(".user-menu .menu-item"));
    await flush();
    expect(api.logout).toHaveBeenCalled();
    expect(view.router.currentRoute.value.name).toBe("login");
  });
});
