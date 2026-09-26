import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, defineComponent, type App } from "vue";

import type { WorkerStatus } from "@/types/api";

const { statusMock } = vi.hoisted(() => ({ statusMock: vi.fn<() => Promise<WorkerStatus>>() }));

vi.mock("@/api/client", () => ({
  api: { worker: { status: () => statusMock() } },
}));

let hidden = false;
const apps: App[] = [];

function sample(paused = false): WorkerStatus {
  return {
    isEncoding: false,
    encodingJobId: 0,
    encodingJobIds: [],
    progress: [],
    lastTickAt: null,
    window: { start: "", end: "", active: true, hasLimit: false },
    maxParallelEncodes: 1,
    paused,
  } as WorkerStatus;
}

async function load() {
  vi.resetModules();
  return import("@/composables/useWorkerStatus");
}

function mount(use: () => ReturnType<Awaited<ReturnType<typeof load>>["useWorkerStatus"]>) {
  let result!: ReturnType<typeof use>;
  const app = createApp(
    defineComponent({
      setup() {
        result = use();
        return () => null;
      },
    }),
  );
  app.mount(document.createElement("div"));
  apps.push(app);
  return { app, result };
}

function setHidden(v: boolean) {
  hidden = v;
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  hidden = false;
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  statusMock.mockReset();
  statusMock.mockResolvedValue(sample());
  vi.useFakeTimers();
});

afterEach(() => {
  for (const app of apps.splice(0)) app.unmount();
  vi.useRealTimers();
});

describe("polling", () => {
  it("fetches once on mount and exposes the status", async () => {
    const { useWorkerStatus } = await load();
    const { result } = mount(useWorkerStatus);
    await vi.waitFor(() => expect(result.status.value).not.toBeNull());
    expect(statusMock).toHaveBeenCalledTimes(1);
  });

  it("polls every ten seconds while visible", async () => {
    const { useWorkerStatus } = await load();
    mount(useWorkerStatus);
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusMock).toHaveBeenCalledTimes(3);
  });

  it("does not start the timer when mounted hidden", async () => {
    hidden = true;
    const { useWorkerStatus } = await load();
    mount(useWorkerStatus);
    await vi.advanceTimersByTimeAsync(30000);
    expect(statusMock).toHaveBeenCalledTimes(1);
  });

  it("stops polling once the last consumer unmounts", async () => {
    const { useWorkerStatus } = await load();
    const a = mount(useWorkerStatus);
    const b = mount(useWorkerStatus);
    await vi.advanceTimersByTimeAsync(0);
    const afterMount = statusMock.mock.calls.length;

    a.app.unmount();
    apps.splice(apps.indexOf(a.app), 1);
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusMock.mock.calls.length).toBe(afterMount + 1);

    b.app.unmount();
    apps.splice(apps.indexOf(b.app), 1);
    await vi.advanceTimersByTimeAsync(30000);
    expect(statusMock.mock.calls.length).toBe(afterMount + 1);
  });

  it("shares one status across consumers", async () => {
    const { useWorkerStatus } = await load();
    const a = mount(useWorkerStatus);
    const b = mount(useWorkerStatus);
    expect(a.result.status).toBe(b.result.status);
  });
});

describe("refresh", () => {
  it("coalesces concurrent calls into one request", async () => {
    let resolve!: (s: WorkerStatus) => void;
    statusMock.mockReturnValue(new Promise((r) => (resolve = r)));
    const { useWorkerStatus } = await load();
    const { result } = mount(useWorkerStatus);
    const p1 = result.refresh();
    const p2 = result.refresh();
    expect(statusMock).toHaveBeenCalledTimes(1);
    resolve(sample(true));
    await Promise.all([p1, p2]);
    expect(result.status.value?.paused).toBe(true);
  });

  it("keeps the previous status when a request fails", async () => {
    const { useWorkerStatus } = await load();
    const { result } = mount(useWorkerStatus);
    await vi.waitFor(() => expect(result.status.value).not.toBeNull());
    const before = result.status.value;
    statusMock.mockRejectedValueOnce(new Error("down"));
    await result.refresh();
    expect(result.status.value).toBe(before);
  });
});

describe("visibility", () => {
  it("pauses while hidden and refreshes when shown again", async () => {
    const { useWorkerStatus } = await load();
    mount(useWorkerStatus);
    await vi.advanceTimersByTimeAsync(0);
    expect(statusMock).toHaveBeenCalledTimes(1);

    setHidden(true);
    await vi.advanceTimersByTimeAsync(30000);
    expect(statusMock).toHaveBeenCalledTimes(1);

    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(statusMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusMock).toHaveBeenCalledTimes(3);
  });

  it("ignores visibility changes with no consumers", async () => {
    const { useWorkerStatus } = await load();
    const { app } = mount(useWorkerStatus);
    await vi.advanceTimersByTimeAsync(0);
    app.unmount();
    apps.splice(apps.indexOf(app), 1);

    setHidden(true);
    setHidden(false);
    await vi.advanceTimersByTimeAsync(30000);
    expect(statusMock).toHaveBeenCalledTimes(1);
  });
});
