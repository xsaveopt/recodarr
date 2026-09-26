import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { flush, mountAt, type Mounted } from "@/testing/mount";
import type { DebugInfo } from "@/types/api";

const { api, toastAdd } = vi.hoisted(() => ({
  api: { info: vi.fn<() => Promise<DebugInfo>>() },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: { debug: { info: () => api.info() } },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import Debug from "@/views/Debug.vue";

function info(over: Partial<DebugInfo> = {}): DebugInfo {
  return {
    hbVersion: "HandBrake 1.9.0",
    hbFound: true,
    encoders: ["x264", "x265"],
    vaapiAvailable: false,
    qsvAvailable: false,
    nvencAvailable: false,
    platform: "linux",
    arch: "amd64",
    ...over,
  };
}

let view: Mounted;

function cell(label: string) {
  const row = [...view.root.querySelectorAll("tr")].find(
    (r) => r.querySelector("th")?.textContent?.trim() === label,
  );
  if (!row) throw new Error(`no row "${label}"`);
  return row.querySelector("td");
}

beforeEach(() => {
  api.info.mockReset();
  toastAdd.mockReset();
});

afterEach(() => {
  view.unmount();
});

describe("Debug view", () => {
  it("shows a loading line until the info arrives", async () => {
    let resolve: (v: DebugInfo) => void = () => {};
    api.info.mockReturnValue(new Promise((r) => (resolve = r)));
    view = await mountAt(Debug);
    expect(view.root.textContent).toContain("Loading…");
    resolve(info());
    await flush();
    expect(view.root.textContent).not.toContain("Loading…");
    expect(cell("Version")?.textContent?.trim()).toBe("HandBrake 1.9.0");
  });

  it("lists the detected encoders and the runtime", async () => {
    api.info.mockResolvedValue(info());
    view = await mountAt(Debug);
    const encoders = [...view.root.querySelectorAll(".enc-tag")].map((e) => e.textContent);
    expect(encoders).toEqual(["x264", "x265"]);
    expect(cell("Binary found")?.querySelector(".ok")?.textContent?.trim()).toBe("yes");
    expect(cell("Platform")?.textContent?.trim()).toBe("linux/amd64");
  });

  it("flags a missing binary and an empty encoder list", async () => {
    api.info.mockResolvedValue(info({ hbFound: false, encoders: [] }));
    view = await mountAt(Debug);
    expect(cell("Binary found")?.querySelector(".bad")?.textContent?.trim()).toBe("no");
    expect(cell("Detected encoders")?.textContent?.trim()).toBe("none");
  });

  it("reports each hardware path on its own", async () => {
    api.info.mockResolvedValue(
      info({ vaapiAvailable: true, qsvAvailable: false, nvencAvailable: true }),
    );
    view = await mountAt(Debug);
    expect(cell("VAAPI (Linux DRI)")?.textContent?.trim()).toBe("available");
    expect(cell("Intel QSV")?.textContent?.trim()).toBe("not detected");
    expect(cell("NVIDIA NVENC")?.textContent?.trim()).toBe("available");
    expect(cell("VAAPI (Linux DRI)")?.querySelector(".ok")).not.toBeNull();
    expect(cell("Intel QSV")?.querySelector(".na")).not.toBeNull();
  });

  it("toasts a failed load and stops loading", async () => {
    api.info.mockRejectedValue(new Error("GET /debug: 500"));
    view = await mountAt(Debug);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load debug info", detail: "GET /debug: 500" }),
    );
    expect(view.root.textContent).not.toContain("Loading…");
    expect(view.root.querySelector(".sections")).toBeNull();
  });
});
