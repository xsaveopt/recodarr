import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";

import { buttonByText, click, flush, mountAt, typeInto, type Mounted } from "@/testing/mount";
import type { AppSettings, WorkerStatus } from "@/types/api";

const { api, toastAdd, worker } = vi.hoisted(() => ({
  api: {
    get: vi.fn<() => Promise<AppSettings>>(),
    put: vi.fn<(s: AppSettings) => Promise<void>>(),
    setPaused: vi.fn<(p: boolean) => Promise<{ paused: boolean; cancelled: number }>>(),
    refresh: vi.fn<() => Promise<void>>(),
  },
  toastAdd: vi.fn(),
  worker: { initial: null as WorkerStatus | null },
}));

vi.mock("@/api/client", () => ({
  api: {
    settings: {
      get: () => api.get(),
      put: (s: AppSettings) => api.put(s),
    },
    worker: { setPaused: (p: boolean) => api.setPaused(p) },
  },
}));

vi.mock("@/composables/useWorkerStatus", () => ({
  useWorkerStatus: () => ({ status: ref(worker.initial), refresh: () => api.refresh() }),
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import WorkerPanel from "@/components/WorkerPanel.vue";

function status(over: Partial<WorkerStatus> = {}): WorkerStatus {
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

let view: Mounted;

function field(label: string) {
  const found = [...view.root.querySelectorAll("label")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no field "${label}"`);
  return found;
}

function input(label: string) {
  return field(label).querySelector("input") as HTMLInputElement;
}

function pauseSwitch() {
  return view.root.querySelector(".instant-card input");
}

function lastPut(): AppSettings {
  const call = api.put.mock.calls.at(-1);
  if (!call) throw new Error("settings were not saved");
  return call[0];
}

async function show(settings: AppSettings, ws: WorkerStatus | null = null) {
  worker.initial = ws;
  api.get.mockResolvedValue(settings);
  view = await mountAt(WorkerPanel);
}

async function save() {
  await click(buttonByText(view.root, "Save"));
}

function errorToast(detail: string) {
  return expect.objectContaining({ severity: "error", detail });
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  api.put.mockResolvedValue(undefined);
  api.refresh.mockResolvedValue(undefined);
  toastAdd.mockReset();
  worker.initial = null;
});

afterEach(() => {
  view.unmount();
});

describe("WorkerPanel settings", () => {
  it("saves the defaults when nothing is stored", async () => {
    await show({});
    await save();
    expect(lastPut()).toEqual({
      worker_interval_seconds: "30",
      reconcile_interval_seconds: "300",
      max_parallel_encodes: "1",
      encoding_window_start: "",
      encoding_window_end: "",
      output_suffix_enabled: "false",
      output_suffix: "recodarr",
    });
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Worker settings saved" }),
    );
  });

  it("loads stored values and round-trips them", async () => {
    await show({
      worker_interval_seconds: "45",
      reconcile_interval_seconds: "900",
      max_parallel_encodes: "2",
      encoding_window_start: "22:00",
      encoding_window_end: "06:00",
      output_suffix_enabled: "true",
      output_suffix: " done ",
    });
    expect(input("Start (HH:MM)").value).toBe("22:00");
    expect(view.root.textContent).toContain("Movie (2024).done");
    await save();
    expect(lastPut()).toEqual({
      worker_interval_seconds: "45",
      reconcile_interval_seconds: "900",
      max_parallel_encodes: "2",
      encoding_window_start: "22:00",
      encoding_window_end: "06:00",
      output_suffix_enabled: "true",
      output_suffix: "done",
    });
  });

  it("trims the window before saving", async () => {
    await show({});
    await typeInto(input("Start (HH:MM)"), " 23:30 ");
    await typeInto(input("End (HH:MM)"), "05:00 ");
    await save();
    expect(lastPut()).toMatchObject({
      encoding_window_start: "23:30",
      encoding_window_end: "05:00",
    });
  });

  it("clears the window", async () => {
    await show({ encoding_window_start: "22:00", encoding_window_end: "06:00" });
    await click(buttonByText(view.root, "Clear window (always encode)"));
    await save();
    expect(lastPut()).toMatchObject({ encoding_window_start: "", encoding_window_end: "" });
  });

  it("disables the marker extension while the marker is off", async () => {
    await show({ output_suffix_enabled: "false" });
    expect(input("Marker extension").disabled).toBe(true);
    await click(field("Write marker").querySelector("input"));
    expect(input("Marker extension").disabled).toBe(false);
    await save();
    expect(lastPut().output_suffix_enabled).toBe("true");
  });

  it.each([
    [{ worker_interval_seconds: "3" }, "Interval must be at least 5 seconds"],
    [{ reconcile_interval_seconds: "30" }, "Library poll interval must be at least 60 seconds"],
    [{ max_parallel_encodes: "17" }, "Parallel encodes must be 1..16"],
    [
      { output_suffix: "bad.ext" },
      "Output suffix: 1–32 chars, letters/digits/dash/underscore only",
    ],
  ] as [AppSettings, string][])("refuses to save %o", async (settings, message) => {
    await show(settings);
    await save();
    expect(api.put).not.toHaveBeenCalled();
    expect(toastAdd).toHaveBeenCalledWith(errorToast(message));
  });

  it("refuses a marker extension with a path separator typed in", async () => {
    await show({ output_suffix_enabled: "true" });
    await typeInto(input("Marker extension"), "../x");
    await save();
    expect(api.put).not.toHaveBeenCalled();
  });

  it("toasts a failed save", async () => {
    await show({});
    api.put.mockRejectedValue(new Error("PUT /settings: 500"));
    await save();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't save settings", detail: "PUT /settings: 500" }),
    );
  });
});

describe("WorkerPanel pause switch", () => {
  it("takes the paused state from the stored setting before the status loads", async () => {
    await show({ encoding_paused: "true" });
    expect((pauseSwitch() as HTMLInputElement).checked).toBe(false);
    expect(view.root.textContent).toContain("Disabled — jobs continue to queue");
  });

  it("prefers the live worker status over the stored setting", async () => {
    await show({ encoding_paused: "true" }, status({ paused: false }));
    expect((pauseSwitch() as HTMLInputElement).checked).toBe(true);
    expect(view.root.textContent).toContain("Worker is running");
  });

  it("pauses and reports re-queued encodes", async () => {
    api.setPaused.mockResolvedValue({ paused: true, cancelled: 2 });
    await show({}, status());
    await click(pauseSwitch());
    expect(api.setPaused).toHaveBeenCalledWith(true);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({
        severity: "success",
        detail: "Worker disabled — 2 in-flight encode(s) re-queued",
      }),
    );
    expect(api.refresh).toHaveBeenCalled();
    expect(view.root.textContent).toContain("Disabled — jobs continue to queue");
  });

  it("pauses quietly when nothing was running", async () => {
    api.setPaused.mockResolvedValue({ paused: true, cancelled: 0 });
    await show({}, status());
    await click(pauseSwitch());
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ detail: "Worker disabled" }));
  });

  it("resumes a paused worker", async () => {
    api.setPaused.mockResolvedValue({ paused: false, cancelled: 0 });
    await show({}, status({ paused: true }));
    await click(pauseSwitch());
    expect(api.setPaused).toHaveBeenCalledWith(false);
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ detail: "Worker enabled" }));
  });

  it("flips the switch back when the pause call fails", async () => {
    api.setPaused.mockRejectedValue(new Error("POST /worker/pause: 500"));
    await show({}, status());
    await click(pauseSwitch());
    await flush();
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ summary: "Couldn't pause" }));
    expect((pauseSwitch() as HTMLInputElement).checked).toBe(true);
    expect(view.root.textContent).toContain("Worker is running");
    expect(api.refresh).not.toHaveBeenCalled();
  });
});
