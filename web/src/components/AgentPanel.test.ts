import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, mountAt, typeInto, type Mounted } from "@/testing/mount";
import type { AppSettings } from "@/types/api";

type AgentTest = {
  ok: boolean;
  error?: string;
  version?: string;
  hb?: string;
  slots?: number;
  active?: number;
  localFs?: boolean;
};

const { api, toastAdd } = vi.hoisted(() => ({
  api: {
    get: vi.fn<() => Promise<AppSettings>>(),
    put: vi.fn<(s: AppSettings) => Promise<void>>(),
    test: vi.fn<(b: { url?: string; token?: string }) => Promise<AgentTest>>(),
  },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    settings: { get: () => api.get(), put: (s: AppSettings) => api.put(s) },
    agent: { test: (b: { url?: string; token?: string }) => api.test(b) },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import AgentPanel from "@/components/AgentPanel.vue";

let view: Mounted;

function field(label: string) {
  const found = [...view.root.querySelectorAll("label")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  const input = found?.querySelector("input");
  if (!input) throw new Error(`no field "${label}"`);
  return input;
}

function toasts(severity: string) {
  return toastAdd.mock.calls.map((c) => c[0]).filter((t) => t.severity === severity);
}

function lastPut(): AppSettings {
  const call = api.put.mock.calls.at(-1);
  if (!call) throw new Error("settings were not saved");
  return call[0];
}

async function show(settings: AppSettings) {
  api.get.mockResolvedValue(settings);
  view = await mountAt(AgentPanel);
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  api.put.mockResolvedValue(undefined);
});

afterEach(() => {
  view.unmount();
});

describe("loading", () => {
  it("fills the form and keeps the stored token out of the input", async () => {
    api.test.mockResolvedValue({ ok: true, slots: 1, active: 0 });
    await show({
      agent_enabled: "true",
      agent_url: "http://agent.invalid:8090",
      hasAgentToken: "true",
      agent_fallback_local: "false",
      max_parallel_encodes: "1",
    });
    expect(field("Use remote agent").checked).toBe(true);
    expect(field("Agent URL").value).toBe("http://agent.invalid:8090");
    expect(field("Agent token").value).toBe("");
    expect(field("Agent token").placeholder).toBe("(stored, leave blank to keep)");
    expect(field("Fall back to local if agent is down").checked).toBe(false);
  });

  it("probes the agent with the stored token and warns when it has fewer slots", async () => {
    api.test.mockResolvedValue({ ok: true, slots: 1, active: 1 });
    await show({
      agent_enabled: "true",
      agent_url: "http://agent.invalid:8090",
      hasAgentToken: "true",
      max_parallel_encodes: "3",
    });
    expect(api.test).toHaveBeenCalledWith({ url: "http://agent.invalid:8090", token: "" });
    const card = view.root.querySelector(".capacity-card")?.textContent ?? "";
    expect(card).toMatch(/1 \/ 1 slot\(s\)/);
    expect(card).toContain("only accepts");
  });

  it("points at the worker setting when the agent has spare slots", async () => {
    api.test.mockResolvedValue({ ok: true, slots: 4, active: 0 });
    await show({ agent_enabled: "true", agent_url: "http://agent.invalid:8090" });
    const card = view.root.querySelector(".capacity-card")?.textContent ?? "";
    expect(card).toContain("can take up to 4 encodes");
  });

  it("does not probe a disabled agent and defaults fallback to on", async () => {
    await show({ agent_url: "http://agent.invalid:8090" });
    expect(api.test).not.toHaveBeenCalled();
    expect(field("Use remote agent").checked).toBe(false);
    expect(field("Fall back to local if agent is down").checked).toBe(true);
    expect(field("Agent token").placeholder).toBe("paste the agent's token");
    expect(view.root.querySelector(".capacity-card")).toBeNull();
  });

  it("stays usable when the probe throws", async () => {
    api.test.mockRejectedValue(new Error("offline"));
    await show({ agent_enabled: "true", agent_url: "http://agent.invalid:8090" });
    expect(view.root.querySelector(".capacity-card")).toBeNull();
    expect(toasts("error")).toEqual([]);
  });
});

describe("saving", () => {
  it("requires a URL when enabled", async () => {
    await show({});
    await click(field("Use remote agent"));
    await click(buttonByText(view.root, "Save"));
    expect(api.put).not.toHaveBeenCalled();
    expect(toasts("error")[0]?.detail).toBe("URL is required when the agent is enabled");
  });

  it("requires a token when enabled and none is stored", async () => {
    await show({});
    await click(field("Use remote agent"));
    await typeInto(field("Agent URL"), "http://agent.invalid:8090");
    await click(buttonByText(view.root, "Save"));
    expect(api.put).not.toHaveBeenCalled();
    expect(toasts("error")[0]?.detail).toBe("Token is required when the agent is enabled");
  });

  it("treats a whitespace-only token as missing", async () => {
    await show({});
    await click(field("Use remote agent"));
    await typeInto(field("Agent URL"), "http://agent.invalid:8090");
    await typeInto(field("Agent token"), "   ");
    await click(buttonByText(view.root, "Save"));
    expect(api.put).not.toHaveBeenCalled();
    expect(toasts("error")[0]?.detail).toBe("Token is required when the agent is enabled");
  });

  it("keeps the stored token when the field is left blank", async () => {
    api.test.mockResolvedValue({ ok: false });
    await show({
      agent_enabled: "true",
      agent_url: "http://agent.invalid:8090",
      hasAgentToken: "true",
    });
    await typeInto(field("Agent URL"), "  http://other.invalid:8090  ");
    await click(buttonByText(view.root, "Save"));
    expect(lastPut()).toEqual({
      agent_enabled: "true",
      agent_url: "http://other.invalid:8090",
      agent_fallback_local: "true",
    });
    expect("agent_token" in lastPut()).toBe(false);
    expect(toasts("success")[0]?.detail).toBe("Remote agent settings saved");
    expect(api.get).toHaveBeenCalledTimes(2);
  });

  it("sends a new token trimmed and clears the field afterwards", async () => {
    await show({});
    await typeInto(field("Agent token"), " new-token ");
    await click(buttonByText(view.root, "Save"));
    expect(lastPut()).toMatchObject({ agent_enabled: "false", agent_token: "new-token" });
    expect(field("Agent token").value).toBe("");
  });

  it("reports a failed save and keeps the typed token", async () => {
    await show({});
    api.put.mockRejectedValue(new Error("PUT /settings: 500"));
    await typeInto(field("Agent token"), "new-token");
    await click(buttonByText(view.root, "Save"));
    expect(toasts("error")[0]).toMatchObject({
      summary: "Couldn't save settings",
      detail: "PUT /settings: 500",
    });
    expect(field("Agent token").value).toBe("new-token");
  });
});

describe("testing the connection", () => {
  it("reports capacity on success", async () => {
    await show({});
    await typeInto(field("Agent URL"), " http://agent.invalid:8090 ");
    await typeInto(field("Agent token"), " tok ");
    api.test.mockResolvedValue({ ok: true, slots: 2, active: 1, localFs: true, hb: "HB 1.0" });
    await click(buttonByText(view.root, "Test connection"));
    expect(api.test).toHaveBeenCalledWith({ url: "http://agent.invalid:8090", token: "tok" });
    expect(toasts("success")[0]?.detail).toBe(
      "Agent reachable — 2 slot(s), 1 active, in-place encoding enabled. HB 1.0",
    );
    expect(view.root.querySelector(".capacity-card")?.textContent).toMatch(/1 \/ 2 slot\(s\)/);
  });

  it("clears the capacity card when the agent is unreachable", async () => {
    api.test.mockResolvedValueOnce({ ok: true, slots: 2, active: 0 });
    await show({ agent_enabled: "true", agent_url: "http://agent.invalid:8090" });
    expect(view.root.querySelector(".capacity-card")).not.toBeNull();
    api.test.mockResolvedValueOnce({ ok: false, error: "401 bad token" });
    await click(buttonByText(view.root, "Test connection"));
    expect(toasts("error")[0]?.detail).toBe("Agent unreachable: 401 bad token");
    expect(view.root.querySelector(".capacity-card")).toBeNull();
  });

  it("reports a thrown error", async () => {
    await show({});
    api.test.mockRejectedValue(new Error("POST /agent/test: 502"));
    await click(buttonByText(view.root, "Test connection"));
    expect(toasts("error")[0]?.detail).toBe("POST /agent/test: 502");
  });
});
