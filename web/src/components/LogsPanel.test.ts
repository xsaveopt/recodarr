import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, mountAt, pickOption, type Mounted } from "@/testing/mount";
import type { AppSettings } from "@/types/api";

const { api, toastAdd } = vi.hoisted(() => ({
  api: {
    get: vi.fn<() => Promise<AppSettings>>(),
    put: vi.fn<(s: AppSettings) => Promise<void>>(),
  },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    settings: {
      get: () => api.get(),
      put: (s: AppSettings) => api.put(s),
    },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import LogsPanel from "@/components/LogsPanel.vue";

let view: Mounted;

function row(label: string) {
  const found = [...view.root.querySelectorAll("label.row")].find(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no row "${label}"`);
  return found;
}

function hasRow(label: string) {
  return [...view.root.querySelectorAll("label.row")].some(
    (l) => l.querySelector("span")?.textContent?.trim() === label,
  );
}

function lastPut(): AppSettings {
  const call = api.put.mock.calls.at(-1);
  if (!call) throw new Error("settings were not saved");
  return call[0];
}

async function show(settings: AppSettings) {
  api.get.mockResolvedValue(settings);
  view = await mountAt(LogsPanel);
}

async function save() {
  await click(buttonByText(view.root, "Save"));
}

beforeEach(() => {
  api.get.mockReset();
  api.put.mockReset();
  api.put.mockResolvedValue(undefined);
  toastAdd.mockReset();
});

afterEach(() => {
  view.unmount();
});

describe("LogsPanel", () => {
  it("saves the defaults when nothing is stored", async () => {
    await show({});
    await save();
    expect(lastPut()).toEqual({
      log_app_level: "INFO",
      log_rotate_enabled: "true",
      log_max_size_mb: "50",
      log_max_age_days: "30",
      log_max_backups: "5",
      log_compress: "false",
    });
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Log settings saved" }),
    );
  });

  it("loads stored values and round-trips them", async () => {
    await show({
      log_app_level: "debug",
      log_rotate_enabled: "true",
      log_max_size_mb: "200",
      log_max_age_days: "0",
      log_max_backups: "12",
      log_compress: "true",
    });
    expect(row("App log level").textContent).toContain("DEBUG — everything");
    await save();
    expect(lastPut()).toEqual({
      log_app_level: "DEBUG",
      log_rotate_enabled: "true",
      log_max_size_mb: "200",
      log_max_age_days: "0",
      log_max_backups: "12",
      log_compress: "true",
    });
  });

  it("falls back to defaults for blank or unparsable numbers", async () => {
    await show({ log_max_size_mb: "", log_max_age_days: "soon", log_max_backups: "many" });
    await save();
    expect(lastPut()).toMatchObject({
      log_max_size_mb: "50",
      log_max_age_days: "30",
      log_max_backups: "5",
    });
  });

  it("hides the rotation form and warns when rotation is off", async () => {
    await show({ log_rotate_enabled: "false" });
    expect(view.root.textContent).toContain("Rotation is off");
    expect(hasRow("Max file size (MB)")).toBe(false);
    await save();
    expect(lastPut().log_rotate_enabled).toBe("false");
  });

  it("turns rotation off from the switch", async () => {
    await show({});
    expect(hasRow("Max file size (MB)")).toBe(true);
    await click(row("Enable rotation").querySelector("input"));
    expect(hasRow("Max file size (MB)")).toBe(false);
    await save();
    expect(lastPut().log_rotate_enabled).toBe("false");
  });

  it("saves a newly picked level", async () => {
    await show({});
    await pickOption(
      row("App log level").querySelector('[data-pc-name="select"]'),
      "WARN — warnings and errors only",
    );
    await save();
    expect(lastPut().log_app_level).toBe("WARN");
  });

  it("refuses a max file size under 1 MB", async () => {
    await show({ log_max_size_mb: "0" });
    await save();
    expect(api.put).not.toHaveBeenCalled();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "error", detail: "Max file size must be ≥ 1 MB" }),
    );
  });

  it("toasts a failed save", async () => {
    await show({});
    api.put.mockRejectedValue(new Error("PUT /settings: 500"));
    await save();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't save settings", detail: "PUT /settings: 500" }),
    );
  });

  it("toasts a failed load and keeps the defaults", async () => {
    api.get.mockRejectedValue(new Error("GET /settings: 500"));
    view = await mountAt(LogsPanel);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load settings" }),
    );
    expect(hasRow("Max file size (MB)")).toBe(true);
  });
});
