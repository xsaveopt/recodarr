import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, defineComponent, type App } from "vue";

import { useEncodeProgress, type EncodeProgress } from "@/composables/useEncodeProgress";

type Listener = (ev: Event) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  closed = false;
  listeners: Record<string, Listener[]> = {};

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: Listener) {
    (this.listeners[type] ??= []).push(fn);
  }

  close() {
    this.closed = true;
  }

  emit(type: string, data?: unknown) {
    const ev = data === undefined ? new Event(type) : new MessageEvent(type, { data });
    for (const fn of this.listeners[type] ?? []) fn(ev);
  }
}

function latest(): FakeEventSource {
  const es = FakeEventSource.instances.at(-1);
  if (!es) throw new Error("no EventSource was opened");
  return es;
}

function progress(jobId: number, percent: number, fps = 10, eta = "00h01m00s"): string {
  const ev: EncodeProgress = { jobId, title: `Job ${jobId}`, percent, fps, eta };
  return JSON.stringify(ev);
}

let app: App | null = null;

function mount(opts?: { onComplete?: (jobId: number) => void }) {
  let result!: ReturnType<typeof useEncodeProgress>;
  app = createApp(
    defineComponent({
      setup() {
        result = useEncodeProgress(opts);
        return () => null;
      },
    }),
  );
  app.mount(document.createElement("div"));
  return result;
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.useFakeTimers();
});

afterEach(() => {
  app?.unmount();
  app = null;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("connecting", () => {
  it("opens the worker progress stream on setup", () => {
    mount();
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(latest().url).toBe("/api/worker/progress");
  });

  it("reports connected once the stream opens", () => {
    const { connected } = mount();
    expect(connected.value).toBe(false);
    latest().emit("open");
    expect(connected.value).toBe(true);
  });

  it("closes the stream on unmount", () => {
    mount();
    const es = latest();
    app?.unmount();
    app = null;
    expect(es.closed).toBe(true);
  });
});

describe("progress events", () => {
  it("records progress per job", () => {
    const { progressByJob } = mount();
    latest().emit("progress", progress(1, 25));
    latest().emit("progress", progress(2, 50));
    expect(progressByJob.value[1]?.percent).toBe(25);
    expect(progressByJob.value[2]?.percent).toBe(50);
  });

  it("replaces an older update for the same job", () => {
    const { progressByJob } = mount();
    latest().emit("progress", progress(1, 25));
    latest().emit("progress", progress(1, 75));
    expect(Object.keys(progressByJob.value)).toEqual(["1"]);
    expect(progressByJob.value[1]?.percent).toBe(75);
  });

  it("ignores events without a job id", () => {
    const { progressByJob } = mount();
    latest().emit("progress", progress(0, 25));
    expect(progressByJob.value).toEqual({});
  });

  it("ignores malformed payloads", () => {
    const { progressByJob } = mount();
    latest().emit("progress", "{not json");
    expect(progressByJob.value).toEqual({});
  });

  it("drops a job and fires onComplete on the all-zero sentinel", () => {
    const onComplete = vi.fn();
    const { progressByJob } = mount({ onComplete });
    latest().emit("progress", progress(1, 99));
    latest().emit("progress", JSON.stringify({ jobId: 1, title: "", percent: 0, fps: 0, eta: "" }));
    expect(progressByJob.value[1]).toBeUndefined();
    expect(onComplete).toHaveBeenCalledWith(1);
  });

  it("does not fire onComplete for a job it never saw", () => {
    const onComplete = vi.fn();
    mount({ onComplete });
    latest().emit("progress", JSON.stringify({ jobId: 7, title: "", percent: 0, fps: 0, eta: "" }));
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("clears everything on idle", () => {
    const { progressByJob } = mount();
    latest().emit("progress", progress(1, 10));
    latest().emit("progress", progress(2, 20));
    latest().emit("idle");
    expect(progressByJob.value).toEqual({});
  });
});

describe("prune", () => {
  it("keeps only the active jobs", () => {
    const { progressByJob, prune } = mount();
    latest().emit("progress", progress(1, 10));
    latest().emit("progress", progress(2, 20));
    latest().emit("progress", progress(3, 30));
    prune([2]);
    expect(Object.keys(progressByJob.value)).toEqual(["2"]);
  });

  it("leaves the object untouched when nothing is stale", () => {
    const { progressByJob, prune } = mount();
    latest().emit("progress", progress(1, 10));
    const before = progressByJob.value;
    prune([1, 2]);
    expect(progressByJob.value).toBe(before);
  });
});

describe("reconnecting", () => {
  it("closes and reconnects after an error with backoff", () => {
    const { connected } = mount();
    const first = latest();
    first.emit("open");
    first.emit("error");
    expect(connected.value).toBe(false);
    expect(first.closed).toBe(true);

    vi.advanceTimersByTime(1999);
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(2);

    latest().emit("error");
    vi.advanceTimersByTime(3999);
    expect(FakeEventSource.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(3);
  });

  it("resets the backoff after a successful open", () => {
    mount();
    latest().emit("error");
    vi.advanceTimersByTime(2000);
    latest().emit("error");
    vi.advanceTimersByTime(4000);
    latest().emit("open");
    latest().emit("error");
    vi.advanceTimersByTime(2000);
    expect(FakeEventSource.instances).toHaveLength(4);
  });

  it("caps the backoff at thirty seconds", () => {
    mount();
    for (let i = 0; i < 6; i++) {
      latest().emit("error");
      vi.advanceTimersByTime(30000);
    }
    const count = FakeEventSource.instances.length;
    latest().emit("error");
    vi.advanceTimersByTime(29999);
    expect(FakeEventSource.instances).toHaveLength(count);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(count + 1);
  });

  it("stops reconnecting after unmount", () => {
    mount();
    latest().emit("error");
    app?.unmount();
    app = null;
    vi.advanceTimersByTime(60000);
    expect(FakeEventSource.instances).toHaveLength(1);
  });
});
