import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";

import {
  buttonByText,
  click,
  flush,
  mountAt,
  settleTransitions,
  typeInto,
  type Mounted,
} from "@/testing/mount";
import type { EncodeProgress } from "@/composables/useEncodeProgress";
import type { Job, JobDebug, JobListParams, JobsPage, Profile } from "@/types/api";

const { api, toastAdd, confirmRequire, progress } = vi.hoisted(() => ({
  api: {
    list: vi.fn<(p: JobListParams) => Promise<JobsPage>>(),
    profiles: vi.fn<() => Promise<Partial<Profile>[]>>(),
    retry: vi.fn<(id: number) => Promise<Job>>(),
    retryAllFailed: vi.fn<() => Promise<{ retried: number }>>(),
    cancel: vi.fn<(id: number) => Promise<{ status: string }>>(),
    remove: vi.fn<(id: number) => Promise<void>>(),
    clearTerminal: vi.fn<(s?: string[]) => Promise<{ deleted: number }>>(),
    bulkRetry: vi.fn<(ids: number[]) => Promise<{ retried: number }>>(),
    bulkDelete: vi.fn<(ids: number[]) => Promise<{ deleted: number }>>(),
    debug: vi.fn<(id: number) => Promise<JobDebug>>(),
  },
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
  progress: { value: {} as Record<number, EncodeProgress> },
}));

vi.mock("@/api/client", () => ({
  api: {
    jobs: {
      list: (p: JobListParams) => api.list(p),
      retry: (id: number) => api.retry(id),
      retryAllFailed: () => api.retryAllFailed(),
      cancel: (id: number) => api.cancel(id),
      remove: (id: number) => api.remove(id),
      clearTerminal: (s?: string[]) => api.clearTerminal(s),
      bulkRetry: (ids: number[]) => api.bulkRetry(ids),
      bulkDelete: (ids: number[]) => api.bulkDelete(ids),
      debug: (id: number) => api.debug(id),
    },
    profiles: { list: () => api.profiles() },
  },
}));

vi.mock("@/composables/useEncodeProgress", () => ({
  useEncodeProgress: () => ({ progressByJob: ref(progress.value) }),
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));
vi.mock("primevue/useconfirm", () => ({ useConfirm: () => ({ require: confirmRequire }) }));

import Jobs from "@/views/Jobs.vue";

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

function page(jobs: Job[], total = jobs.length): JobsPage {
  return { jobs, total, limit: 50, offset: 0 };
}

let view: Mounted;

function lastList(): JobListParams {
  const call = api.list.mock.calls.at(-1);
  if (!call) throw new Error("jobs were never listed");
  return call[0];
}

function rowFor(id: number) {
  const row = [...view.root.querySelectorAll("tbody tr")].find(
    (r) => r.querySelectorAll("td")[1]?.textContent?.trim() === String(id),
  );
  if (!row) throw new Error(`no row for job ${id}`);
  return row;
}

function iconButton(row: Element, icon: string) {
  return row.querySelector(`.${icon}`)?.closest("button") ?? null;
}

function rowCheckbox(id: number) {
  return rowFor(id).querySelector('input[type="checkbox"]');
}

async function show(jobs: Job[], path = "/jobs") {
  api.list.mockResolvedValue(page(jobs));
  view = await mountAt(Jobs, path);
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  confirmRequire.mockReset();
  progress.value = {};
  api.profiles.mockResolvedValue([]);
});

afterEach(() => {
  view.unmount();
  vi.useRealTimers();
});

describe("filters from the URL", () => {
  it("lists everything with default paging and leaves the URL clean", async () => {
    await show([]);
    expect(lastList()).toEqual({
      status: undefined,
      kind: undefined,
      profileId: undefined,
      q: undefined,
      limit: 50,
      offset: 0,
    });
    expect(view.router.currentRoute.value.fullPath).toBe("/jobs");
  });

  it("passes the query filters through and drops unknown statuses", async () => {
    await show(
      [],
      "/jobs?status=failed,bogus,done&kind=radarr&q=dune&profileId=4&offset=50&size=25",
    );
    expect(lastList()).toEqual({
      status: "failed,done",
      kind: "radarr",
      profileId: 4,
      q: "dune",
      limit: 25,
      offset: 50,
    });
  });

  it("clamps the page size", async () => {
    await show([], "/jobs?size=5");
    expect(lastList().limit).toBe(10);
    view.unmount();
    await show([], "/jobs?size=9000");
    expect(lastList().limit).toBe(500);
  });

  it("debounces the title search, resets the page and records it in the URL", async () => {
    await show([], "/jobs?offset=100");
    const before = api.list.mock.calls.length;
    await typeInto(view.root.querySelector('input[placeholder="Search title…"]'), "  dune ");
    expect(api.list.mock.calls.length).toBe(before);
    await new Promise((r) => setTimeout(r, 300));
    await flush();
    expect(lastList()).toMatchObject({ q: "dune", offset: 0 });
    expect(view.router.currentRoute.value.query).toMatchObject({ q: "  dune " });
    expect(view.router.currentRoute.value.query.offset).toBeUndefined();
  });
});

describe("rows", () => {
  it("shows sizes, savings and the profile name, marking deleted profiles", async () => {
    api.profiles.mockResolvedValue([
      { id: 2, name: "HEVC", encoder: "x265", deleted: false },
      { id: 3, name: "Old", encoder: "x264", deleted: true },
    ]);
    await show([
      job(1, { profileId: 2, originalSize: 4 * 1024 ** 3, finalSize: 1024 ** 3 }),
      job(2, { profileId: 3 }),
    ]);
    const first = rowFor(1).textContent ?? "";
    expect(first).toContain("HEVC");
    expect(first).toContain("4.00 GB");
    expect(first).toContain("1.00 GB");
    expect(first).toContain("75%");
    expect(rowFor(2).textContent).toContain("Old (deleted)");
  });

  it("offers actions that match the job status", async () => {
    await show([
      job(1, { status: "encoding" }),
      job(2, { status: "failed", error: "boom" }),
      job(3, { status: "ready" }),
    ]);
    expect(iconButton(rowFor(1), "pi-stop-circle")).not.toBeNull();
    expect(iconButton(rowFor(1), "pi-refresh")).toBeNull();
    expect(iconButton(rowFor(1), "pi-trash")).toBeNull();
    expect(iconButton(rowFor(2), "pi-refresh")).not.toBeNull();
    expect(iconButton(rowFor(2), "pi-trash")).not.toBeNull();
    expect(iconButton(rowFor(3), "pi-refresh")).toBeNull();
    expect(iconButton(rowFor(3), "pi-trash")).toBeNull();
  });

  it("shows live progress on an encoding row", async () => {
    progress.value = { 5: { jobId: 5, title: "x", percent: 42.6, fps: 30, eta: "00h12m" } };
    await show([job(5, { status: "encoding" })]);
    const text = rowFor(5).textContent ?? "";
    expect(text).toContain("43%");
    expect(text).toContain("00h12m");
  });

  it("flags a done job whose arr refresh failed", async () => {
    await show([job(1, { refreshError: "sonarr down" })]);
    expect(rowFor(1).textContent).toContain("done, but *arr refresh failed");
  });

  it("opens the encode log from the error cell", async () => {
    await show([job(9, { status: "failed", error: "exit 3", encodeLog: "x265 [error] nope" })]);
    await click(rowFor(9).querySelector("button.err-msg"));
    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain("Job #9 — Show 9");
    expect(dialog?.textContent).toContain("x265 [error] nope");
  });
});

describe("row actions", () => {
  it("retries a job and reloads", async () => {
    api.retry.mockResolvedValue(job(2, { status: "ready" }));
    await show([job(2, { status: "failed" })]);
    const loads = api.list.mock.calls.length;
    await click(iconButton(rowFor(2), "pi-refresh"));
    expect(api.retry).toHaveBeenCalledWith(2);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Re-queued job #2" }),
    );
    expect(api.list.mock.calls.length).toBe(loads + 1);
  });

  it("cancels an encoding job", async () => {
    api.cancel.mockResolvedValue({ status: "failed" });
    await show([job(4, { status: "encoding" })]);
    await click(iconButton(rowFor(4), "pi-stop-circle"));
    expect(api.cancel).toHaveBeenCalledWith(4);
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ detail: "Cancelled job #4" }));
  });

  it("removes a job only after confirmation, without reloading", async () => {
    api.remove.mockResolvedValue(undefined);
    await show([job(1), job(2)]);
    const loads = api.list.mock.calls.length;
    await click(iconButton(rowFor(2), "pi-trash"));
    expect(api.remove).not.toHaveBeenCalled();
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts.header).toBe("Remove job from history?");
    await opts.accept();
    await flush();
    expect(api.remove).toHaveBeenCalledWith(2);
    expect(() => rowFor(2)).toThrow();
    expect(api.list.mock.calls.length).toBe(loads);
  });

  it("loads diagnostics for a job", async () => {
    api.debug.mockResolvedValue({
      jobId: 3,
      status: "waiting_for_seed",
      downloadId: "abc",
      downloadIdLength: 3,
      filePath: "/tv/a.mkv",
      qbit: { configured: true, url: "http://qbit", reachable: true, lookup: { found: false } },
      attempts: 0,
      waitingForSeedCount: 80,
      seedCheckBatchLimit: 50,
      stalledReason: "torrent still seeding",
    });
    await show([job(3, { status: "waiting_for_seed" })]);
    await click(iconButton(rowFor(3), "pi-info-circle"));
    expect(api.debug).toHaveBeenCalledWith(3);
    const text = document.body.querySelector('[role="dialog"]')?.textContent ?? "";
    expect(text).toContain("torrent still seeding");
    expect(text).toContain("80 jobs waiting, only 50 checked per tick");
    expect(text).toContain("qBit does not have this hash.");
  });

  it("shows a diagnostics error in the dialog", async () => {
    api.debug.mockRejectedValue(new Error("GET /jobs/3/debug: 500"));
    await show([job(3)]);
    await click(iconButton(rowFor(3), "pi-info-circle"));
    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain(
      "GET /jobs/3/debug: 500",
    );
  });
});

describe("page actions", () => {
  it("says so when there is nothing to retry", async () => {
    api.retryAllFailed.mockResolvedValue({ retried: 0 });
    await show([]);
    await click(buttonByText(view.root, "Retry all failed"));
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "info", detail: "No failed jobs to retry" }),
    );
  });

  it("clears the terminal statuses by default and closes the dialog", async () => {
    api.clearTerminal.mockResolvedValue({ deleted: 4 });
    await show([]);
    await click(buttonByText(view.root, "Clear history…"));
    await click(buttonByText(document.body, "Clear selected statuses"));
    expect(api.clearTerminal).toHaveBeenCalledWith(["done", "failed", "skipped"]);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ detail: "Cleared 4 entry/entries from history" }),
    );
    await settleTransitions();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
  });
});

describe("bulk selection", () => {
  const mixed = () => [
    job(1, { status: "failed" }),
    job(2, { status: "encoding" }),
    job(3, { status: "ready" }),
    job(4, { status: "done" }),
  ];

  it("counts what each bulk action would touch", async () => {
    await show(mixed());
    for (const id of [1, 2, 3]) await click(rowCheckbox(id));
    const bar = view.root.querySelector(".bulk-bar");
    expect(bar?.textContent).toContain("3 selected");
    expect(bar?.textContent).toContain("1 encoding (skipped on delete)");
    expect(buttonByText(view.root, "Retry 1")).toBeTruthy();
    expect(buttonByText(view.root, "Delete 2")).toBeTruthy();
  });

  it("retries the selected ids and clears the selection", async () => {
    api.bulkRetry.mockResolvedValue({ retried: 2 });
    await show(mixed());
    await click(rowCheckbox(1));
    await click(rowCheckbox(4));
    await click(buttonByText(view.root, "Retry 2"));
    expect(api.bulkRetry).toHaveBeenCalledWith([1, 4]);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ detail: "Re-queued 2 job(s)" }),
    );
    expect(view.root.querySelector(".bulk-bar")).toBeNull();
  });

  it("shift-clicking a checkbox selects the whole range from the anchor", async () => {
    await show(mixed());
    await click(rowCheckbox(1));
    rowCheckbox(4)?.dispatchEvent(
      new MouseEvent("click", { bubbles: true, cancelable: true, shiftKey: true }),
    );
    await flush();
    expect(view.root.querySelector(".bulk-bar")?.textContent).toContain("4 selected");

    rowCheckbox(2)?.dispatchEvent(
      new MouseEvent("click", { bubbles: true, cancelable: true, shiftKey: true }),
    );
    await flush();
    expect(view.root.querySelector(".bulk-bar")?.textContent).toContain("2 selected");
  });

  it("deletes the selected ids after confirmation", async () => {
    api.bulkDelete.mockResolvedValue({ deleted: 1 });
    await show(mixed());
    await click(rowCheckbox(3));
    await click(buttonByText(view.root, "Delete 1"));
    expect(api.bulkDelete).not.toHaveBeenCalled();
    await confirmRequire.mock.calls[0]?.[0].accept();
    await flush();
    expect(api.bulkDelete).toHaveBeenCalledWith([3]);
  });
});

describe("background polling", () => {
  it("refreshes every 15s but not while rows are selected", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    await show([job(1, { status: "failed" })]);
    const start = api.list.mock.calls.length;
    vi.advanceTimersByTime(15000);
    await flush();
    expect(api.list.mock.calls.length).toBe(start + 1);
    await click(rowCheckbox(1));
    vi.advanceTimersByTime(30000);
    await flush();
    expect(api.list.mock.calls.length).toBe(start + 1);
  });
});
