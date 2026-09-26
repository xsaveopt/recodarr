import { beforeEach, describe, expect, it, vi } from "vitest";

const { toastAdd, confirmRequire } = vi.hoisted(() => ({
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
}));

vi.mock("primevue/usetoast", () => ({
  useToast: () => ({ add: toastAdd }),
}));

vi.mock("primevue/useconfirm", () => ({
  useConfirm: () => ({ require: confirmRequire }),
}));

import { useNotify } from "@/composables/useNotify";

function lastToast() {
  const call = toastAdd.mock.calls.at(-1);
  if (!call) throw new Error("no toast was shown");
  return call[0];
}

beforeEach(() => {
  toastAdd.mockReset();
  confirmRequire.mockReset();
});

describe("toasts", () => {
  it("shows a short success toast", () => {
    useNotify().success("Saved");
    expect(lastToast()).toEqual({
      severity: "success",
      summary: "Done",
      detail: "Saved",
      life: 3000,
    });
  });

  it("shows an info toast with a custom summary", () => {
    useNotify().info("Queued", "Heads up");
    expect(lastToast()).toEqual({
      severity: "info",
      summary: "Heads up",
      detail: "Queued",
      life: 3000,
    });
  });

  it("uses an Error's message for the detail", () => {
    useNotify().error(new Error("boom"));
    expect(lastToast()).toEqual({
      severity: "error",
      summary: "Something went wrong",
      detail: "boom",
      life: 6000,
    });
  });

  it("stringifies a non-Error detail", () => {
    useNotify().error(42, "Oops");
    expect(lastToast()).toMatchObject({ summary: "Oops", detail: "42" });
  });
});

describe("tryRun", () => {
  it("returns the result and stays quiet on success", async () => {
    const got = await useNotify().tryRun(async () => 7);
    expect(got).toBe(7);
    expect(toastAdd).not.toHaveBeenCalled();
  });

  it("returns undefined and shows an error on failure", async () => {
    const got = await useNotify().tryRun(async () => {
      throw new Error("nope");
    }, "Load failed");
    expect(got).toBeUndefined();
    expect(lastToast()).toMatchObject({
      severity: "error",
      summary: "Load failed",
      detail: "nope",
    });
  });
});

describe("tryAct", () => {
  it("shows the success message and returns true", async () => {
    const ok = await useNotify().tryAct(async () => {}, "Deleted");
    expect(ok).toBe(true);
    expect(lastToast()).toMatchObject({ severity: "success", detail: "Deleted" });
  });

  it("shows the error and returns false", async () => {
    const ok = await useNotify().tryAct(
      async () => {
        throw new Error("locked");
      },
      "Deleted",
      "Delete failed",
    );
    expect(ok).toBe(false);
    expect(toastAdd).toHaveBeenCalledTimes(1);
    expect(lastToast()).toMatchObject({
      severity: "error",
      summary: "Delete failed",
      detail: "locked",
    });
  });
});

describe("confirmDelete", () => {
  it("builds a default delete confirmation", () => {
    useNotify().confirmDelete({ name: "Profile A", onAccept: () => {} });
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts).toMatchObject({
      message: 'Delete "Profile A"? This cannot be undone.',
      header: "Delete confirmation",
      acceptLabel: "Delete",
      rejectLabel: "Cancel",
      acceptClass: "p-button-danger",
    });
  });

  it("honours custom wording", () => {
    useNotify().confirmDelete({
      name: "x",
      message: "Really?",
      header: "Purge",
      acceptLabel: "Purge",
      onAccept: () => {},
    });
    expect(confirmRequire.mock.calls[0]?.[0]).toMatchObject({
      message: "Really?",
      header: "Purge",
      acceptLabel: "Purge",
    });
  });

  it("runs onAccept when the user accepts", async () => {
    const onAccept = vi.fn(async () => {});
    useNotify().confirmDelete({ name: "x", onAccept });
    expect(onAccept).not.toHaveBeenCalled();
    await confirmRequire.mock.calls[0]?.[0].accept();
    expect(onAccept).toHaveBeenCalledTimes(1);
  });
});
