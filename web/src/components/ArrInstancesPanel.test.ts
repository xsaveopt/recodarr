import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buttonByText,
  click,
  flush,
  mountAt,
  settleTransitions,
  typeInto,
  type Mounted,
} from "@/testing/mount";
import type { ArrInstance } from "@/types/api";

const { api, toastAdd, confirmRequire } = vi.hoisted(() => ({
  api: {
    list: vi.fn<() => Promise<ArrInstance[]>>(),
    create: vi.fn<(a: Omit<ArrInstance, "id">) => Promise<ArrInstance>>(),
    update: vi.fn<(a: ArrInstance) => Promise<ArrInstance>>(),
    remove: vi.fn<(id: number) => Promise<void>>(),
    test: vi.fn<(id: number) => Promise<{ ok: boolean; error?: string }>>(),
  },
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    arr: {
      list: () => api.list(),
      create: (a: Omit<ArrInstance, "id">) => api.create(a),
      update: (a: ArrInstance) => api.update(a),
      remove: (id: number) => api.remove(id),
      test: (id: number) => api.test(id),
    },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));
vi.mock("primevue/useconfirm", () => ({ useConfirm: () => ({ require: confirmRequire }) }));

import ArrInstancesPanel from "@/components/ArrInstancesPanel.vue";

const LS_KEY = "recodarr.arrTestResults";

function instance(over: Partial<ArrInstance> = {}): ArrInstance {
  return {
    id: 1,
    kind: "sonarr",
    name: "series",
    url: "http://sonarr.invalid:8989",
    enabled: true,
    hasApiKey: true,
    deleted: false,
    ...over,
  };
}

let view: Mounted;

function rowFor(name: string) {
  const row = [...view.root.querySelectorAll("tbody tr")].find((r) =>
    [...r.querySelectorAll("td")].some((td) => td.textContent?.trim() === name),
  );
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

function rowButton(name: string, icon: string) {
  return rowFor(name).querySelector(`.${icon}`)?.closest("button") ?? null;
}

function dialog() {
  const d = document.body.querySelector('[role="dialog"]');
  if (!d) throw new Error("dialog is not open");
  return d;
}

function field(label: string) {
  const found = [...dialog().querySelectorAll("label")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no field "${label}"`);
  return found;
}

function input(label: string) {
  const el = field(label).querySelector("input");
  if (!el) throw new Error(`no input for "${label}"`);
  return el;
}

async function save() {
  await click(buttonByText(dialog(), "Save"));
}

async function show(items: ArrInstance[]) {
  api.list.mockResolvedValue(items);
  view = await mountAt(ArrInstancesPanel);
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  confirmRequire.mockReset();
  localStorage.clear();
});

afterEach(() => {
  view.unmount();
  localStorage.clear();
});

describe("instance list", () => {
  it("shows each instance and its cached test result", async () => {
    localStorage.setItem(
      LS_KEY,
      JSON.stringify({ 2: { ok: false, error: "401", at: Date.now() - 120_000 } }),
    );
    await show([
      instance({ id: 1, name: "series" }),
      instance({ id: 2, kind: "radarr", name: "films", enabled: false }),
    ]);
    const series = rowFor("series").textContent ?? "";
    expect(series).toContain("sonarr");
    expect(series).toContain("yes");
    expect(series).toContain("never tested");
    const films = rowFor("films").textContent ?? "";
    expect(films).toContain("no");
    expect(films).toContain("Fail");
    expect(films).toContain("2m ago");
    expect(rowFor("films").querySelector(".test-fail")?.getAttribute("title")).toBe("401");
  });

  it("ignores a corrupt cache", async () => {
    localStorage.setItem(LS_KEY, "{not json");
    await show([instance()]);
    expect(rowFor("series").textContent).toContain("never tested");
  });

  it("reports a failed load", async () => {
    api.list.mockRejectedValue(new Error("GET /arr-instances: 500"));
    view = await mountAt(ArrInstancesPanel);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load *arr instances" }),
    );
    expect(view.root.textContent).toContain("No instances yet");
  });
});

describe("adding an instance", () => {
  beforeEach(async () => {
    await show([]);
    await click(buttonByText(view.root, "Add"));
  });

  it("validates name, URL and API key in order", async () => {
    await save();
    expect(dialog().textContent).toContain("Name is required.");
    await typeInto(input("Name"), "series");
    await save();
    expect(dialog().textContent).toContain("URL is required.");
    await typeInto(input("URL"), "http://sonarr.invalid:8989");
    await save();
    expect(dialog().textContent).toContain("API key is required.");
    await typeInto(input("API key"), "   ");
    await save();
    expect(dialog().textContent).toContain("API key is required.");
    expect(api.create).not.toHaveBeenCalled();
  });

  it("creates the instance and reloads", async () => {
    api.create.mockImplementation(async (a) => ({ ...a, id: 5 }) as ArrInstance);
    await typeInto(input("Name"), "series");
    await typeInto(input("URL"), "http://sonarr.invalid:8989");
    await typeInto(input("API key"), "key-1");
    await save();
    expect(api.create).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "sonarr",
        name: "series",
        url: "http://sonarr.invalid:8989",
        apiKey: "key-1",
        enabled: true,
      }),
    );
    expect(api.create.mock.calls[0]?.[0]).not.toHaveProperty("id");
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Saved series" }),
    );
    expect(api.list).toHaveBeenCalledTimes(2);
    await settleTransitions();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
  });

  it("keeps the dialog open when the save fails", async () => {
    api.create.mockRejectedValue(new Error("POST /arr-instances: 400"));
    await typeInto(input("Name"), "series");
    await typeInto(input("URL"), "http://sonarr.invalid:8989");
    await typeInto(input("API key"), "key-1");
    await save();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't save instance" }),
    );
    await settleTransitions();
    expect(dialog()).toBeTruthy();
  });
});

describe("editing an instance", () => {
  it("never pre-fills the stored key and keeps it when left blank", async () => {
    await show([instance({ id: 3, name: "series" })]);
    api.update.mockImplementation(async (a) => a);
    await click(rowButton("series", "pi-pencil"));
    expect(input("API key").value).toBe("");
    expect(input("API key").placeholder).toBe("(stored — leave blank to keep)");
    expect(field("Kind").querySelector('[aria-disabled="true"]')).not.toBeNull();
    await typeInto(input("Name"), "renamed");
    await save();
    expect(api.update).toHaveBeenCalledWith(
      expect.objectContaining({ id: 3, name: "renamed", apiKey: "" }),
    );
    expect(api.create).not.toHaveBeenCalled();
  });

  it("sends a replacement key", async () => {
    await show([instance({ id: 3 })]);
    api.update.mockImplementation(async (a) => a);
    await click(rowButton("series", "pi-pencil"));
    await typeInto(input("API key"), "rotated");
    await save();
    expect(api.update.mock.calls[0]?.[0].apiKey).toBe("rotated");
  });
});

describe("testing a connection", () => {
  it("records a passing test and caches it", async () => {
    await show([instance({ id: 4 })]);
    api.test.mockResolvedValue({ ok: true });
    await click(rowButton("series", "pi-wifi"));
    expect(api.test).toHaveBeenCalledWith(4);
    expect(rowFor("series").querySelector(".test-ok")?.textContent).toContain("OK");
    const cached = JSON.parse(localStorage.getItem(LS_KEY) ?? "{}");
    expect(cached[4]).toMatchObject({ ok: true });
  });

  it("records a thrown error as a failure", async () => {
    await show([instance({ id: 4 })]);
    api.test.mockRejectedValue(new Error("POST /arr-instances/4/test: 502"));
    await click(rowButton("series", "pi-wifi"));
    const fail = rowFor("series").querySelector(".test-fail");
    expect(fail?.textContent).toContain("Fail");
    expect(fail?.getAttribute("title")).toBe("POST /arr-instances/4/test: 502");
  });
});

describe("deleting an instance", () => {
  it("removes it after confirmation and forgets its test result", async () => {
    localStorage.setItem(LS_KEY, JSON.stringify({ 9: { ok: true, at: Date.now() } }));
    await show([instance({ id: 9, name: "old" })]);
    api.remove.mockResolvedValue(undefined);
    await click(rowButton("old", "pi-trash"));
    expect(api.remove).not.toHaveBeenCalled();
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts.message).toBe('Delete "old"? This cannot be undone.');
    api.list.mockResolvedValue([]);
    await opts.accept();
    await flush();
    expect(api.remove).toHaveBeenCalledWith(9);
    expect(JSON.parse(localStorage.getItem(LS_KEY) ?? "{}")).toEqual({});
    expect(view.root.textContent).toContain("No instances yet");
  });

  it("keeps the cached result when the delete fails", async () => {
    localStorage.setItem(LS_KEY, JSON.stringify({ 9: { ok: true, at: Date.now() } }));
    await show([instance({ id: 9, name: "old" })]);
    api.remove.mockRejectedValue(new Error("DELETE /arr-instances/9: 500"));
    await click(rowButton("old", "pi-trash"));
    await confirmRequire.mock.calls[0]?.[0].accept();
    await flush();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't delete instance" }),
    );
    expect(JSON.parse(localStorage.getItem(LS_KEY) ?? "{}")).toHaveProperty("9");
  });
});
