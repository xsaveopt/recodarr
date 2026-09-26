import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buttonByText,
  click,
  flush,
  mountAt,
  pickOption,
  settleTransitions,
  typeInto,
  type Mounted,
} from "@/testing/mount";
import type { HbCaps, Profile } from "@/types/api";

const { api, toastAdd, confirmRequire } = vi.hoisted(() => ({
  api: {
    list: vi.fn<() => Promise<Profile[]>>(),
    caps: vi.fn<() => Promise<HbCaps>>(),
    upsert: vi.fn<(p: Partial<Profile>) => Promise<Profile>>(),
    remove: vi.fn<(id: number) => Promise<void>>(),
  },
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    profiles: {
      list: () => api.list(),
      upsert: (p: Partial<Profile>) => api.upsert(p),
      remove: (id: number) => api.remove(id),
    },
    handbrake: { caps: () => api.caps() },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));
vi.mock("primevue/useconfirm", () => ({ useConfirm: () => ({ require: confirmRequire }) }));

import ProfilesPanel from "@/components/ProfilesPanel.vue";

const caps: HbCaps = {
  encoders: [
    {
      name: "x264",
      presets: ["fast", "medium", "slow"],
      profiles: ["main", "high"],
      tunes: ["film"],
      levels: ["auto", "4.1"],
    },
    {
      name: "x265",
      presets: ["fast", "medium"],
      profiles: ["main", "main10"],
      tunes: ["grain"],
      levels: ["auto"],
    },
    {
      name: "nvenc_h265",
      presets: ["fast", "medium"],
      profiles: ["auto", "main"],
      tunes: [],
      levels: ["auto"],
    },
  ],
};

function profile(over: Partial<Profile> = {}): Profile {
  return {
    id: 1,
    name: "HEVC",
    encoder: "x265",
    encoderPreset: "medium",
    encoderProfile: "main",
    encoderTune: "",
    encoderLevel: "auto",
    rateControl: "crf",
    quality: 24,
    videoBitrate: 0,
    maxWidth: 0,
    maxHeight: 0,
    audioEncoder: "copy",
    audioBitrate: 0,
    audioMixdown: "",
    audioBitratesByChannels: {},
    subtitleCopy: false,
    twoPass: false,
    containerFormat: "mkv",
    extraArgs: "",
    framerate: "",
    skipCodecs: "",
    skipBitrateMBPerHour: 0,
    skipBitrateUnit: "mb_per_hour",
    skipFileSizeMB: 0,
    skipDurationMinutes: 0,
    skipHeightPx: 0,
    skipHDR: false,
    bloatPolicy: "off",
    bloatRetryMax: 3,
    bloatRetryStep: 3,
    bloatMinSavingsPercent: 0,
    deleted: false,
    ...over,
  };
}

let view: Mounted;

function rows() {
  return [...view.root.querySelectorAll("tbody tr")];
}

function rowFor(name: string) {
  const row = rows().find((r) => r.querySelector("td")?.textContent?.trim() === name);
  if (!row) throw new Error(`no row for ${name}`);
  return row;
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

async function pick(label: string, option: string) {
  await pickOption(field(label).querySelector('[data-pc-name="select"]'), option);
}

function lastUpsert(): Partial<Profile> {
  const call = api.upsert.mock.calls.at(-1);
  if (!call) throw new Error("upsert was not called");
  return call[0];
}

async function save() {
  await click(buttonByText(dialog(), "Save profile"));
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  confirmRequire.mockReset();
  api.caps.mockResolvedValue(caps);
  api.upsert.mockImplementation(async (p) => ({ ...profile(), ...p }));
});

afterEach(() => {
  view.unmount();
});

describe("profile list", () => {
  it("summarises rate, resolution and options per row", async () => {
    api.list.mockResolvedValue([
      profile({ id: 1, name: "CRF", quality: 20 }),
      profile({
        id: 2,
        name: "ABR",
        rateControl: "abr",
        videoBitrate: 4000,
        maxWidth: 1920,
        subtitleCopy: true,
        twoPass: true,
        containerFormat: "mp4",
      }),
    ]);
    view = await mountAt(ProfilesPanel);
    const crf = rowFor("CRF").textContent ?? "";
    expect(crf).toContain("RF 20");
    expect(crf).toContain("Original");
    expect(crf).toContain("MKV");
    expect(crf).toContain("—");
    const abr = rowFor("ABR").textContent ?? "";
    expect(abr).toContain("4000 kbps");
    expect(abr).toContain("1920w");
    expect(abr).toContain("MP4");
    expect(abr).toMatch(/subs\s*2-pass/);
  });

  it("reports a failed load instead of throwing", async () => {
    api.list.mockRejectedValue(new Error("GET /profiles: 500"));
    view = await mountAt(ProfilesPanel);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load profiles", detail: "GET /profiles: 500" }),
    );
    expect(rows().map((r) => r.textContent)).not.toContain("HEVC");
  });
});

describe("creating a profile", () => {
  beforeEach(async () => {
    api.list.mockResolvedValue([]);
    view = await mountAt(ProfilesPanel);
    await click(buttonByText(view.root, "Add"));
  });

  it("requires a name", async () => {
    await save();
    expect(dialog().textContent).toContain("Name is required.");
    expect(api.upsert).not.toHaveBeenCalled();
  });

  it("starts from x265 with its HandBrake defaults", async () => {
    await typeInto(field("Profile name").querySelector("input"), "New");
    await save();
    expect(lastUpsert()).toMatchObject({
      name: "New",
      encoder: "x265",
      encoderPreset: "medium",
      encoderProfile: "main",
      encoderLevel: "auto",
      encoderTune: "",
      rateControl: "crf",
      quality: 24,
      audioEncoder: "copy",
      containerFormat: "mkv",
      bloatPolicy: "off",
    });
    expect(lastUpsert().id).toBeUndefined();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: 'Saved profile "New"' }),
    );
    expect(api.list).toHaveBeenCalledTimes(2);
    await settleTransitions();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
  });

  it("resets encoder options and quality when the encoder changes", async () => {
    await typeInto(field("Profile name").querySelector("input"), "Fast");
    await pick("Encoder", "x264");
    await save();
    expect(lastUpsert()).toMatchObject({
      encoder: "x264",
      encoderPreset: "medium",
      encoderProfile: "main",
      encoderLevel: "auto",
      quality: 22,
    });
  });

  it("drops defaults the binary does not offer", async () => {
    await typeInto(field("Profile name").querySelector("input"), "GPU");
    await pick("Encoder", "nvenc_h265");
    await save();
    expect(lastUpsert()).toMatchObject({
      encoder: "nvenc_h265",
      encoderPreset: "medium",
      encoderProfile: "auto",
      encoderLevel: "auto",
      twoPass: false,
    });
  });

  it("keeps the dialog open when the save fails", async () => {
    api.upsert.mockRejectedValue(new Error("POST /profiles: 400 bad preset"));
    await typeInto(field("Profile name").querySelector("input"), "Broken");
    await save();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't save profile" }),
    );
    await settleTransitions();
    expect(dialog()).toBeTruthy();
  });
});

describe("editing a profile", () => {
  it("fills in a bitrate when opening an ABR profile without one", async () => {
    api.list.mockResolvedValue([profile({ rateControl: "abr", videoBitrate: 0 })]);
    view = await mountAt(ProfilesPanel);
    await click(rowFor("HEVC").querySelector(".pi-pencil")?.closest("button") ?? null);
    await save();
    expect(lastUpsert()).toMatchObject({ id: 1, rateControl: "abr", videoBitrate: 2500 });
  });

  it("shows per-channel Opus defaults and does not persist them", async () => {
    api.list.mockResolvedValue([profile({ audioEncoder: "opus" })]);
    view = await mountAt(ProfilesPanel);
    await click(rowFor("HEVC").querySelector(".pi-pencil")?.closest("button") ?? null);
    expect(dialog().textContent).toContain("Opus defaults shown");
    expect((field("Stereo (2 ch)").querySelector("input") as HTMLInputElement).value).toBe("96");
    expect((field("5.1 (6 ch)").querySelector("input") as HTMLInputElement).value).toBe("256");
    await save();
    expect(lastUpsert().audioBitratesByChannels).toEqual({});
  });

  it("hides the per-channel table once a fixed mixdown is picked", async () => {
    api.list.mockResolvedValue([profile({ audioEncoder: "av_aac" })]);
    view = await mountAt(ProfilesPanel);
    await click(rowFor("HEVC").querySelector(".pi-pencil")?.closest("button") ?? null);
    expect(dialog().textContent).toContain("AAC defaults shown");
    await pick("Mixdown", "Stereo");
    expect(dialog().textContent).not.toContain("defaults shown");
    await save();
    expect(lastUpsert().audioMixdown).toBe("stereo");
  });
});

describe("duplicating a profile", () => {
  it("picks the first free copy name and drops the id", async () => {
    api.list.mockResolvedValue([
      profile({ id: 1, name: "HEVC" }),
      profile({ id: 2, name: "HEVC (copy)" }),
    ]);
    view = await mountAt(ProfilesPanel);
    await click(rowFor("HEVC").querySelector('button[title="Duplicate"]'));
    await save();
    expect(lastUpsert().name).toBe("HEVC (copy) 2");
    expect(lastUpsert().id).toBeUndefined();
  });
});

describe("deleting a profile", () => {
  it("removes it only after confirmation and reloads", async () => {
    api.list.mockResolvedValue([profile({ id: 7, name: "Old" })]);
    api.remove.mockResolvedValue(undefined);
    view = await mountAt(ProfilesPanel);
    await click(rowFor("Old").querySelector(".pi-trash")?.closest("button") ?? null);
    expect(api.remove).not.toHaveBeenCalled();
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts.message).toBe('Delete "Old"? This cannot be undone.');
    await opts.accept();
    await flush();
    expect(api.remove).toHaveBeenCalledWith(7);
    expect(api.list).toHaveBeenCalledTimes(2);
  });
});
