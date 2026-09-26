import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, ref, type Ref } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import PrimeVue from "primevue/config";
import ConfirmationService from "primevue/confirmationservice";
import ToastService from "primevue/toastservice";

import { flush } from "@/testing/mount";
import type { EncodeProgress } from "@/composables/useEncodeProgress";
import type {
  HealthSnapshot,
  Job,
  JobListParams,
  JobStats,
  JobsPage,
  WorkerStatus,
} from "@/types/api";

const { api, toastAdd, hooks } = vi.hoisted(() => ({
  api: {
    stats: vi.fn<() => Promise<JobStats>>(),
    list: vi.fn<(p: JobListParams) => Promise<JobsPage>>(),
    health: vi.fn<() => Promise<HealthSnapshot>>(),
    worker: vi.fn<() => Promise<WorkerStatus | null>>(),
    prune: vi.fn<(ids: number[]) => void>(),
  },
  toastAdd: vi.fn(),
  hooks: {
    progress: {} as Record<number, EncodeProgress>,
    onComplete: undefined as ((jobId: number) => void) | undefined,
    status: undefined as unknown as Ref<WorkerStatus | null>,
  },
}));

vi.mock("@/api/client", () => ({
  api: {
    stats: { get: () => api.stats() },
    jobs: { list: (p: JobListParams) => api.list(p) },
    status: { get: () => api.health() },
  },
}));

vi.mock("@/composables/useEncodeProgress", () => ({
  useEncodeProgress: (opts?: { onComplete?: (jobId: number) => void }) => {
    hooks.onComplete = opts?.onComplete;
    return { progressByJob: ref(hooks.progress), prune: (ids: number[]) => api.prune(ids) };
  },
}));

vi.mock("@/composables/useWorkerStatus", () => ({
  useWorkerStatus: () => {
    hooks.status = ref<WorkerStatus | null>(null);
    return {
      status: hooks.status,
      refresh: async () => {
        hooks.status.value = await api.worker();
      },
    };
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import Dashboard from "@/views/Dashboard.vue";

function stats(over: Partial<JobStats> = {}): JobStats {
  return {
    waitingForSeed: 0,
    waitingForHardlink: 0,
    ready: 0,
    encoding: 0,
    done: 0,
    failed: 0,
    skipped: 0,
    totalSavedBytes: 0,
    ...over,
  };
}

function job(id: number, over: Partial<Job> = {}): Job {
  const now = new Date().toISOString();
  return {
    id,
    arrKind: "sonarr",
    arrInstanceId: 1,
    arrItemId: id,
    arrParentId: 1,
    title: `Show ${id}`,
    filePath: `/tv/show-${id}.mkv`,
    fileSize: 1024,
    downloadId: "",
    profileId: null,
    status: "done",
    attempts: 1,
    createdAt: now,
    updatedAt: now,
    source: "poll",
    ...over,
  };
}

function worker(over: Partial<WorkerStatus> = {}): WorkerStatus {
  return {
    isEncoding: false,
    encodingJobId: 0,
    encodingJobIds: [],
    progress: [],
    lastTickAt: null,
    window: { start: "", end: "", active: true, hasLimit: false },
    maxParallelEncodes: 1,
    paused: false,
    ...over,
  };
}

interface Data {
  stats?: JobStats | Error;
  recent?: Job[];
  upNext?: Job[];
  health?: HealthSnapshot | Error;
  worker?: WorkerStatus | null;
}

let root: HTMLElement;
let unmount: () => void = () => {};

async function show(data: Data = {}) {
  if (data.stats instanceof Error) api.stats.mockRejectedValue(data.stats);
  else api.stats.mockResolvedValue(data.stats ?? stats());
  api.list.mockImplementation(async (p) => {
    const jobs = p.status === "ready" ? (data.upNext ?? []) : (data.recent ?? []);
    return { jobs, total: jobs.length, limit: p.limit ?? 50, offset: 0 };
  });
  if (data.health instanceof Error) api.health.mockRejectedValue(data.health);
  else api.health.mockResolvedValue(data.health ?? { ok: true, issues: [], checkedAt: "" });
  api.worker.mockResolvedValue(data.worker === undefined ? worker() : data.worker);

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "dashboard", component: { render: () => null } },
      { path: "/jobs", name: "jobs", component: { render: () => null } },
      { path: "/settings", name: "settings", component: { render: () => null } },
    ],
  });
  await router.push("/");
  await router.isReady();
  root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp(Dashboard);
  app.use(router);
  app.use(PrimeVue, { unstyled: true });
  app.use(ToastService);
  app.use(ConfirmationService);
  app.mount(root);
  await flush();
  unmount = () => {
    app.unmount();
    root.remove();
    document.body.innerHTML = "";
  };
}

function stat(label: string) {
  const s = [...root.querySelectorAll(".stat")].find(
    (el) => el.querySelector(".stat-label")?.textContent?.trim() === label,
  );
  if (!s) throw new Error(`no stat "${label}"`);
  return s;
}

function statValue(label: string) {
  return stat(label).querySelector(".stat-value")?.textContent?.trim();
}

function strip() {
  return root.querySelector(".status-strip")?.textContent?.replace(/\s+/g, " ").trim() ?? "";
}

function recentRow(title: string) {
  const row = [...root.querySelectorAll(".block")]
    .find((b) => b.querySelector(".block-title")?.textContent?.trim() === "Recent activity")
    ?.querySelectorAll(".list-row");
  const found = [...(row ?? [])].find(
    (r) => r.querySelector(".row-title")?.textContent?.trim() === title,
  );
  if (!found) throw new Error(`no recent row "${title}"`);
  return found;
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  hooks.progress = {};
  hooks.onComplete = undefined;
});

afterEach(() => {
  unmount();
});

describe("Dashboard data loading", () => {
  it("asks for the recent jobs and the head of the queue", async () => {
    await show();
    expect(api.list).toHaveBeenCalledWith({ limit: 12, sort: "updated" });
    expect(api.list).toHaveBeenCalledWith({ status: "ready", order: "asc", limit: 8 });
  });

  it("toasts each failed source on the first load", async () => {
    await show({ stats: new Error("GET /stats: 500"), health: new Error("GET /status: 500") });
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load stats", detail: "GET /stats: 500" }),
    );
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load health" }),
    );
    expect(root.querySelector(".stats")).toBeNull();
  });

  it("prunes live progress against the encoding ids", async () => {
    await show({ worker: worker({ isEncoding: true, encodingJobIds: [4, 9] }) });
    expect(api.prune).toHaveBeenCalledWith([4, 9]);
  });
});

describe("Dashboard stats", () => {
  it("fills the tiles and links them to the filtered job list", async () => {
    await show({
      stats: stats({
        done: 12,
        encoding: 1,
        ready: 2,
        waitingForSeed: 3,
        waitingForHardlink: 4,
        failed: 5,
        skipped: 6,
        totalSavedBytes: 3 * 1024 ** 3,
      }),
    });
    expect(statValue("Total saved")).toBe("3.00 GB");
    expect(statValue("Done")).toBe("12");
    expect(statValue("Queued")).toBe("9");
    expect(statValue("Failed")).toBe("5");
    expect(statValue("Skipped")).toBe("6");
    expect(stat("Failed").querySelector(".stat-bad")).not.toBeNull();
    expect(stat("Encoding").querySelector(".stat-pulse")).not.toBeNull();
    expect(stat("Done").getAttribute("href")).toBe("/jobs?status=done");
    expect(stat("Queued").getAttribute("href")).toBe("/jobs?status=ready");
  });

  it("formats small savings in bytes", async () => {
    await show({ stats: stats({ done: 1, totalSavedBytes: 512 }) });
    expect(statValue("Total saved")).toBe("512 B");
    expect(stat("Failed").querySelector(".stat-bad")).toBeNull();
  });

  it("shows the welcome card only when every counter is zero", async () => {
    await show();
    expect(root.querySelector(".empty-card")).not.toBeNull();
    expect(root.querySelector(".empty-card a")?.getAttribute("href")).toBe("/settings");
    unmount();
    await show({ stats: stats({ skipped: 1 }) });
    expect(root.querySelector(".empty-card")).toBeNull();
  });
});

describe("Dashboard status strip", () => {
  it("shows an idle worker and a never-ticked worker", async () => {
    await show();
    expect(strip()).toContain("Idle");
    expect(strip()).toContain("last tick never");
    expect(root.querySelector(".dot-idle")).not.toBeNull();
  });

  it("shows slot usage while encoding", async () => {
    await show({
      worker: worker({ isEncoding: true, encodingJobIds: [3], maxParallelEncodes: 2 }),
    });
    expect(strip()).toContain("Encoding · 1 of 2 slots");
    expect(root.querySelector(".dot-active")).not.toBeNull();
  });

  it("shows a paused worker even while an encode winds down", async () => {
    await show({ worker: worker({ paused: true, isEncoding: true, encodingJobIds: [3] }) });
    expect(strip()).toMatch(/^Paused\s*· jobs continue to queue/);
    expect(strip()).not.toContain("slots");
  });

  it("shows the encoding window when one is set", async () => {
    await show({
      worker: worker({ window: { start: "22:00", end: "06:00", active: false, hasLimit: true } }),
    });
    expect(strip()).toContain("Window 22:00–06:00 · paused");
    expect(root.querySelector(".pill-warn")).not.toBeNull();
  });

  it("formats the last tick relative to now", async () => {
    const at = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    await show({ worker: worker({ lastTickAt: at }) });
    expect(strip()).toContain("last tick 5m ago");
  });

  it("hides the strip until the worker status is known", async () => {
    await show({ worker: null });
    expect(root.querySelector(".status-strip")).toBeNull();
  });
});

describe("Dashboard health", () => {
  it("lists each issue and counts them in the strip", async () => {
    await show({
      health: {
        ok: false,
        checkedAt: "",
        issues: [
          { level: "error", source: "qbit", title: "qBittorrent unreachable", detail: "timeout" },
          { level: "warn", source: "disk", title: "Low disk space" },
        ],
      },
    });
    const issues = [...root.querySelectorAll(".issue")];
    expect(issues.map((i) => i.querySelector(".issue-title")?.textContent)).toEqual([
      "qBittorrent unreachable",
      "Low disk space",
    ]);
    expect(issues[0]?.classList.contains("issue-error")).toBe(true);
    expect(issues[0]?.querySelector(".issue-detail")?.textContent).toBe("timeout");
    expect(issues[1]?.querySelector(".issue-detail")).toBeNull();
    expect(strip()).toContain("2 issues");
  });

  it("uses the singular for one issue", async () => {
    await show({
      health: { ok: false, checkedAt: "", issues: [{ level: "warn", source: "x", title: "t" }] },
    });
    expect(strip()).toContain("1 issue");
    expect(strip()).not.toContain("1 issues");
  });

  it("shows a healthy pill when all checks pass", async () => {
    await show();
    expect(strip()).toContain("Healthy");
    expect(root.querySelector(".issues")).toBeNull();
  });
});

describe("Dashboard active encodes", () => {
  it("prefers live progress, then the status snapshot, then a placeholder", async () => {
    hooks.progress = { 1: { jobId: 1, title: "Live", percent: 42.25, fps: 30, eta: "0h05m" } };
    await show({
      worker: worker({
        isEncoding: true,
        encodingJobIds: [1, 2, 3],
        maxParallelEncodes: 3,
        progress: [
          { jobId: 1, title: "Stale", percent: 1, fps: 1, eta: "" },
          { jobId: 2, title: "Snapshot", percent: 10, fps: 0, eta: "" },
        ],
      }),
    });
    const encodes = [...root.querySelectorAll(".encode")];
    expect(encodes.map((e) => e.querySelector(".encode-title")?.textContent)).toEqual([
      "Live",
      "Snapshot",
      "job #3",
    ]);
    expect(encodes[0]?.querySelector(".encode-pct")?.textContent).toBe("42.3%");
    expect(encodes[0]?.textContent).toContain("30.0 fps");
    expect(encodes[0]?.textContent).toContain("ETA 0h05m");
    expect(encodes[1]?.textContent).not.toContain("fps");
    expect(root.querySelector(".block-title")?.textContent).toBe("Active encodes");
    expect(root.querySelector(".block-meta")?.textContent).toBe("3 / 3");
  });

  it("caps the bar at full width", async () => {
    hooks.progress = { 1: { jobId: 1, title: "Over", percent: 130, fps: 1, eta: "" } };
    await show({ worker: worker({ isEncoding: true, encodingJobIds: [1] }) });
    expect((root.querySelector(".bar-fill") as HTMLElement).style.width).toBe("100%");
    expect(root.querySelector(".block-title")?.textContent).toBe("Active encode");
  });

  it("drops a finished encode and reloads quietly", async () => {
    await show({
      worker: worker({
        isEncoding: true,
        encodingJobIds: [1],
        progress: [{ jobId: 1, title: "Ending", percent: 99, fps: 1, eta: "" }],
      }),
    });
    expect(root.querySelectorAll(".encode")).toHaveLength(1);
    const statsCalls = api.stats.mock.calls.length;
    api.stats.mockRejectedValue(new Error("GET /stats: 500"));
    api.worker.mockImplementation(async () => hooks.status.value);
    hooks.onComplete?.(1);
    await flush();
    expect(root.querySelectorAll(".encode")).toHaveLength(0);
    expect(hooks.status.value).toMatchObject({
      encodingJobIds: [],
      encodingJobId: 0,
      isEncoding: false,
      progress: [],
    });
    expect(api.stats.mock.calls.length).toBe(statsCalls + 1);
    expect(toastAdd).not.toHaveBeenCalled();
  });
});

describe("Dashboard queue and activity", () => {
  it("lists the next jobs in order and counts the rest", async () => {
    await show({
      stats: stats({ ready: 5, waitingForSeed: 2, waitingForHardlink: 1 }),
      upNext: [job(1, { status: "ready" }), job(2, { status: "ready" })],
    });
    const rows = [...root.querySelectorAll("ol.list .list-row")].map((r) => [
      r.querySelector(".row-pos")?.textContent,
      r.querySelector(".row-title")?.textContent,
    ]);
    expect(rows).toEqual([
      ["1", "Show 1"],
      ["2", "Show 2"],
    ]);
    const foot = root.querySelector(".queue-foot")?.textContent ?? "";
    expect(foot).toContain("+3 more ready");
    expect(foot).toContain("3 waiting on seeding");
  });

  it("says when nothing is ready", async () => {
    await show();
    expect(root.textContent).toContain("Nothing ready to encode.");
    expect(root.querySelector(".queue-foot")).toBeNull();
  });

  it("shows savings for finished jobs and a status for the rest", async () => {
    await show({
      recent: [
        job(1, { title: "Smaller", originalSize: 1000, finalSize: 400 }),
        job(2, { title: "Held", status: "waiting_for_hardlink" }),
        job(3, { title: "Seeding", status: "waiting_for_seed" }),
        job(4, { title: "Broken", status: "failed" }),
      ],
    });
    expect(recentRow("Smaller").querySelector(".row-saved")?.textContent?.trim()).toBe("−60%");
    expect(recentRow("Held").querySelector(".row-saved")?.textContent?.trim()).toBe(
      "seeding (hardlink)",
    );
    expect(recentRow("Seeding").querySelector(".row-saved")?.textContent?.trim()).toBe("seeding");
    expect(recentRow("Broken").querySelector(".row-saved")?.textContent?.trim()).toBe("failed");
    expect(recentRow("Broken").querySelector(".marker-failed")).not.toBeNull();
  });

  it("shows growth for an encode that ended up larger", async () => {
    await show({ recent: [job(1, { title: "Bigger", originalSize: 1000, finalSize: 1500 })] });
    expect(recentRow("Bigger").querySelector(".row-saved")?.textContent?.trim()).toBe("+50%");
  });

  it("formats recent times in hours once past an hour", async () => {
    const at = new Date(Date.now() - 3 * 3600 * 1000).toISOString();
    await show({ recent: [job(1, { title: "Old", updatedAt: at })] });
    expect(recentRow("Old").querySelector(".row-time")?.textContent?.trim()).toBe("3h ago");
  });
});
