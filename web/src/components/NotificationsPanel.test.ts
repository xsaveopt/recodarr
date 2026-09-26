import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, mountAt, typeInto, type Mounted } from "@/testing/mount";
import type { AppSettings } from "@/types/api";

const { api, toastAdd } = vi.hoisted(() => ({
  api: {
    get: vi.fn<() => Promise<AppSettings>>(),
    put: vi.fn<(s: AppSettings) => Promise<void>>(),
  },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: { settings: { get: () => api.get(), put: (s: AppSettings) => api.put(s) } },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import NotificationsPanel from "@/components/NotificationsPanel.vue";

let view: Mounted;

function input(label: string) {
  const found = [...view.root.querySelectorAll("label")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  const el = found?.querySelector("input");
  if (!el) throw new Error(`no field "${label}"`);
  return el;
}

function toasts(severity: string) {
  return toastAdd.mock.calls.map((c) => c[0]).filter((t) => t.severity === severity);
}

async function show(settings: AppSettings) {
  api.get.mockResolvedValue(settings);
  view = await mountAt(NotificationsPanel);
}

beforeEach(() => {
  api.get.mockReset();
  api.put.mockReset();
  toastAdd.mockReset();
  api.put.mockResolvedValue(undefined);
});

afterEach(() => {
  view.unmount();
});

describe("loading", () => {
  it("reads the stored webhook and toggles", async () => {
    await show({
      notify_url: "https://hooks.invalid/topic",
      notify_on_done: "false",
      notify_on_fail: "true",
      notify_on_health: "false",
    });
    expect(input("Webhook URL").value).toBe("https://hooks.invalid/topic");
    expect(input("Notify on done").checked).toBe(false);
    expect(input("Notify on fail").checked).toBe(true);
    expect(input("Notify on health issues").checked).toBe(false);
  });

  it("defaults every toggle to on", async () => {
    await show({});
    expect(input("Webhook URL").value).toBe("");
    expect(input("Notify on done").checked).toBe(true);
    expect(input("Notify on fail").checked).toBe(true);
    expect(input("Notify on health issues").checked).toBe(true);
  });

  it("reports a failed load", async () => {
    api.get.mockRejectedValue(new Error("GET /settings: 500"));
    view = await mountAt(NotificationsPanel);
    expect(toasts("error")[0]).toMatchObject({ summary: "Couldn't load settings" });
  });
});

describe("saving", () => {
  it("saves the trimmed URL and every toggle", async () => {
    await show({});
    await typeInto(input("Webhook URL"), "  https://hooks.invalid/topic  ");
    await click(input("Notify on done"));
    await click(buttonByText(view.root, "Save"));
    expect(api.put).toHaveBeenCalledWith({
      notify_url: "https://hooks.invalid/topic",
      notify_on_done: "false",
      notify_on_fail: "true",
      notify_on_health: "true",
    });
    expect(toasts("success")[0]?.detail).toBe("Notification settings saved");
  });

  it("reports a failed save", async () => {
    await show({});
    api.put.mockRejectedValue(new Error("PUT /settings: 400"));
    await click(buttonByText(view.root, "Save"));
    expect(toasts("error")[0]).toMatchObject({
      summary: "Couldn't save settings",
      detail: "PUT /settings: 400",
    });
  });
});

describe("sending a test", () => {
  it("refuses without a URL", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    await show({ notify_url: "   " });
    await click(buttonByText(view.root, "Send test"));
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(toasts("error")[0]?.detail).toBe("No webhook URL configured");
  });

  it("posts a JSON test message to the webhook", async () => {
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 204 }));
    await show({ notify_url: " https://hooks.invalid/topic " });
    await click(buttonByText(view.root, "Send test"));
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0]!;
    expect(url).toBe("https://hooks.invalid/topic");
    expect(init?.method).toBe("POST");
    expect(init?.headers).toEqual({ "Content-Type": "application/json" });
    expect(JSON.parse(String(init?.body))).toEqual({
      title: "Recodarr",
      message: "Test notification from Recodarr.",
      status: "test",
    });
    expect(toasts("success")[0]?.detail).toBe("Test sent (HTTP 204)");
  });

  it("reports a non-2xx reply", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("nope", { status: 403 }));
    await show({ notify_url: "https://hooks.invalid/topic" });
    await click(buttonByText(view.root, "Send test"));
    expect(toasts("error")[0]?.detail).toBe("Server responded with HTTP 403");
  });

  it("reports a network error", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));
    await show({ notify_url: "https://hooks.invalid/topic" });
    await click(buttonByText(view.root, "Send test"));
    expect(toasts("error")[0]?.detail).toBe("Failed to fetch");
  });
});
