import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, flush, mountAt, typeInto, type Mounted } from "@/testing/mount";
import type { QbitInstance } from "@/types/api";

type TestResult = { ok: boolean; error?: string };

const { api, toastAdd, confirmRequire } = vi.hoisted(() => ({
  api: {
    list: vi.fn<() => Promise<QbitInstance[]>>(),
    upsert: vi.fn<(q: Partial<QbitInstance>) => Promise<QbitInstance>>(),
    remove: vi.fn<(id: number) => Promise<void>>(),
    testCredentials: vi.fn<(url: string, user: string, pw: string) => Promise<TestResult>>(),
    test: vi.fn<(id: number) => Promise<TestResult>>(),
  },
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    qbit: {
      list: () => api.list(),
      upsert: (q: Partial<QbitInstance>) => api.upsert(q),
      remove: (id: number) => api.remove(id),
      testCredentials: (u: string, n: string, p: string) => api.testCredentials(u, n, p),
      test: (id: number) => api.test(id),
    },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));
vi.mock("primevue/useconfirm", () => ({ useConfirm: () => ({ require: confirmRequire }) }));

import QbitPanel from "@/components/QbitPanel.vue";

function stored(over: Partial<QbitInstance> = {}): QbitInstance {
  return {
    id: 2,
    name: "qbit",
    url: "http://qbit.invalid:8080",
    username: "operator",
    hasPassword: true,
    ...over,
  };
}

let view: Mounted;

function input(label: string) {
  const found = [...view.root.querySelectorAll("label")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  const el = found?.querySelector("input");
  if (!el) throw new Error(`no field "${label}"`);
  return el;
}

function hasButton(text: string) {
  return [...view.root.querySelectorAll("button")].some((b) => b.textContent?.trim() === text);
}

function lastUpsert(): Partial<QbitInstance> {
  const call = api.upsert.mock.calls.at(-1);
  if (!call) throw new Error("upsert was not called");
  return call[0];
}

async function show(list: QbitInstance[]) {
  api.list.mockResolvedValue(list);
  view = await mountAt(QbitPanel);
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  confirmRequire.mockReset();
  api.upsert.mockImplementation(
    async (q) => ({ ...stored(), ...q, id: q.id || 2 }) as QbitInstance,
  );
});

afterEach(() => {
  view.unmount();
});

describe("loading", () => {
  it("shows the stored instance without its password", async () => {
    await show([stored({ password: "should-not-show" })]);
    expect(input("URL").value).toBe("http://qbit.invalid:8080");
    expect(input("Username").value).toBe("operator");
    expect(input("Password").value).toBe("");
    expect(input("Password").placeholder).toBe("(stored — leave blank to keep)");
    expect(hasButton("Delete")).toBe(true);
  });

  it("starts empty without a stored instance", async () => {
    await show([]);
    expect(input("Name").value).toBe("qbit");
    expect(input("URL").value).toBe("");
    expect(input("Password").placeholder).toBe("");
    expect(hasButton("Delete")).toBe(false);
  });

  it("reports a failed load", async () => {
    api.list.mockRejectedValue(new Error("GET /qbit-instances: 500"));
    view = await mountAt(QbitPanel);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load qBit" }),
    );
  });
});

describe("saving", () => {
  it("requires a URL and a username", async () => {
    await show([]);
    await click(buttonByText(view.root, "Save"));
    expect(view.root.querySelector(".error")?.textContent).toBe("URL is required.");
    await typeInto(input("URL"), "http://qbit.invalid:8080");
    await click(buttonByText(view.root, "Save"));
    expect(view.root.querySelector(".error")?.textContent).toBe("Username is required.");
    expect(api.upsert).not.toHaveBeenCalled();
  });

  it("sends a blank password to keep the stored one", async () => {
    await show([stored()]);
    await typeInto(input("URL"), "http://qbit.invalid:9090");
    await click(buttonByText(view.root, "Save"));
    expect(lastUpsert()).toMatchObject({
      id: 2,
      url: "http://qbit.invalid:9090",
      username: "operator",
      password: "",
    });
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Saved qBit credentials" }),
    );
  });

  it("sends a new password and clears the field after saving", async () => {
    await show([]);
    await typeInto(input("URL"), "http://qbit.invalid:8080");
    await typeInto(input("Username"), "operator");
    await typeInto(input("Password"), "fresh-secret");
    api.upsert.mockResolvedValue(stored({ password: "fresh-secret" }));
    await click(buttonByText(view.root, "Save"));
    expect(lastUpsert()).toMatchObject({ id: 0, password: "fresh-secret" });
    expect(input("Password").value).toBe("");
    expect(input("Password").placeholder).toBe("(stored — leave blank to keep)");
    expect(hasButton("Delete")).toBe(true);
  });

  it("reports a failed save", async () => {
    await show([stored()]);
    api.upsert.mockRejectedValue(new Error("POST /qbit-instances: 400"));
    await click(buttonByText(view.root, "Save"));
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't save qBit" }),
    );
  });
});

describe("testing", () => {
  it("needs a URL first", async () => {
    await show([]);
    await click(buttonByText(view.root, "Test"));
    expect(view.root.textContent).toContain("Connection failed: URL is required.");
    expect(api.test).not.toHaveBeenCalled();
    expect(api.testCredentials).not.toHaveBeenCalled();
  });

  it("tests the stored credentials when the password is blank", async () => {
    await show([stored()]);
    api.test.mockResolvedValue({ ok: true });
    await click(buttonByText(view.root, "Test"));
    expect(api.test).toHaveBeenCalledWith(2);
    expect(api.testCredentials).not.toHaveBeenCalled();
    expect(view.root.querySelector(".ok")?.textContent?.trim()).toBe("Connection successful.");
  });

  it("tests typed credentials when a password is entered", async () => {
    await show([stored()]);
    api.testCredentials.mockResolvedValue({ ok: false, error: "403 banned" });
    await typeInto(input("Password"), "typed");
    await click(buttonByText(view.root, "Test"));
    expect(api.testCredentials).toHaveBeenCalledWith(
      "http://qbit.invalid:8080",
      "operator",
      "typed",
    );
    expect(view.root.textContent).toContain("Connection failed: 403 banned");
  });

  it("tests an unsaved instance with its typed credentials", async () => {
    await show([]);
    api.testCredentials.mockResolvedValue({ ok: true });
    await typeInto(input("URL"), "http://qbit.invalid:8080");
    await typeInto(input("Username"), "operator");
    await click(buttonByText(view.root, "Test"));
    expect(api.testCredentials).toHaveBeenCalledWith("http://qbit.invalid:8080", "operator", "");
  });

  it("shows a thrown error", async () => {
    await show([stored()]);
    api.test.mockRejectedValue(new Error("POST /qbit-instances/2/test: 502"));
    await click(buttonByText(view.root, "Test"));
    expect(view.root.textContent).toContain("Connection failed: POST /qbit-instances/2/test: 502");
  });
});

describe("deleting", () => {
  it("resets the form after a confirmed delete", async () => {
    await show([stored()]);
    api.remove.mockResolvedValue(undefined);
    await click(buttonByText(view.root, "Delete"));
    expect(api.remove).not.toHaveBeenCalled();
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts.message).toBe('Delete "qBit credentials"? This cannot be undone.');
    await opts.accept();
    await flush();
    expect(api.remove).toHaveBeenCalledWith(2);
    expect(input("URL").value).toBe("");
    expect(input("Username").value).toBe("");
    expect(hasButton("Delete")).toBe(false);
  });

  it("keeps the form when the delete fails", async () => {
    await show([stored()]);
    api.remove.mockRejectedValue(new Error("DELETE /qbit-instances/2: 500"));
    await click(buttonByText(view.root, "Delete"));
    await confirmRequire.mock.calls[0]?.[0].accept();
    await flush();
    expect(toastAdd).toHaveBeenCalledWith(expect.objectContaining({ summary: "Couldn't delete" }));
    expect(input("URL").value).toBe("http://qbit.invalid:8080");
  });
});
