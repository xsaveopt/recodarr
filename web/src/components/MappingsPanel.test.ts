import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buttonByText,
  click,
  flush,
  mountAt,
  pickOption,
  settleTransitions,
  type Mounted,
} from "@/testing/mount";
import type { InstanceTag, Profile, TagMapping } from "@/types/api";

const { api, toastAdd, confirmRequire } = vi.hoisted(() => ({
  api: {
    list: vi.fn<() => Promise<TagMapping[]>>(),
    create: vi.fn<(m: Omit<TagMapping, "id">) => Promise<TagMapping>>(),
    remove: vi.fn<(id: number) => Promise<void>>(),
    profiles: vi.fn<() => Promise<Profile[]>>(),
    allTags: vi.fn<() => Promise<InstanceTag[]>>(),
  },
  toastAdd: vi.fn(),
  confirmRequire: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    tagMappings: {
      list: () => api.list(),
      create: (m: Omit<TagMapping, "id">) => api.create(m),
      remove: (id: number) => api.remove(id),
    },
    profiles: { list: () => api.profiles() },
    arr: { allTags: () => api.allTags() },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));
vi.mock("primevue/useconfirm", () => ({ useConfirm: () => ({ require: confirmRequire }) }));

import MappingsPanel from "@/components/MappingsPanel.vue";

const tags: InstanceTag[] = [
  { instanceId: 1, instanceName: "Series", kind: "sonarr", tagId: 10, tagLabel: "anime" },
  { instanceId: 1, instanceName: "Series", kind: "sonarr", tagId: 11, tagLabel: "kids" },
  { instanceId: 2, instanceName: "Films", kind: "radarr", tagId: 10, tagLabel: "remux" },
];

function profile(id: number, name: string): Profile {
  return { id, name } as Profile;
}

let view: Mounted;

function rows() {
  return [...view.root.querySelectorAll("tbody tr")].map((r) =>
    [...r.querySelectorAll("td")].map((td) => td.textContent?.trim()),
  );
}

function select(cls: string) {
  const el = view.root.querySelector(`.${cls}`);
  if (!el) throw new Error(`no select .${cls}`);
  return el;
}

function error() {
  return view.root.querySelector(".error")?.textContent?.trim();
}

async function openOptions(cls: string) {
  await settleTransitions();
  await click(select(cls));
  const opts = [...document.body.querySelectorAll('[role="option"][aria-label]')].map((o) =>
    o.getAttribute("aria-label"),
  );
  await click(select(cls));
  await settleTransitions();
  return opts;
}

async function show(mappings: TagMapping[], path = "/") {
  api.list.mockResolvedValue(mappings);
  view = await mountAt(MappingsPanel, path);
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
  confirmRequire.mockReset();
  api.profiles.mockResolvedValue([profile(3, "HEVC"), profile(4, "AV1")]);
  api.allTags.mockResolvedValue(tags);
});

afterEach(() => {
  view.unmount();
});

describe("mapping list", () => {
  it("names each mapping's profile and falls back to its id", async () => {
    await show([
      { id: 1, arrKind: "sonarr", tagId: 10, tagLabel: "anime", profileId: 3 },
      { id: 2, arrKind: "both", tagId: 12, tagLabel: "legacy", profileId: 99 },
    ]);
    expect(rows()).toEqual([
      ["sonarr", "anime", "HEVC", ""],
      ["both", "legacy", "#99", ""],
    ]);
  });

  it("reports a failed load", async () => {
    api.list.mockRejectedValue(new Error("GET /tag-mappings: 500"));
    view = await mountAt(MappingsPanel);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load mappings" }),
    );
    expect(view.root.textContent).toContain("No mappings yet");
  });
});

describe("adding a mapping", () => {
  it("validates target, tag and profile in order", async () => {
    await show([]);
    await click(buttonByText(view.root, "Add"));
    expect(error()).toBe("Select a target (Sonarr / Radarr / Both).");
    await pickOption(select("sel-kind"), "Sonarr");
    await click(buttonByText(view.root, "Add"));
    expect(error()).toBe("Select a tag.");
    await pickOption(select("sel-tag"), "Series / anime");
    await click(buttonByText(view.root, "Add"));
    expect(error()).toBe("Select a profile.");
    expect(api.create).not.toHaveBeenCalled();
  });

  it("offers only the tags of the picked target", async () => {
    await show([]);
    expect(await openOptions("sel-tag")).toEqual([
      "Series / anime",
      "Series / kids",
      "Films / remux",
    ]);
    await pickOption(select("sel-kind"), "Radarr");
    expect(await openOptions("sel-tag")).toEqual(["Films / remux"]);
    await pickOption(select("sel-kind"), "Both");
    expect(await openOptions("sel-tag")).toHaveLength(3);
  });

  it("creates the mapping, appends it and resets the form", async () => {
    await show([]);
    api.create.mockImplementation(async (m) => ({ ...m, id: 8 }));
    await pickOption(select("sel-kind"), "Sonarr");
    await pickOption(select("sel-tag"), "Series / kids");
    await pickOption(select("sel-profile"), "AV1");
    await click(buttonByText(view.root, "Add"));
    expect(api.create).toHaveBeenCalledWith({
      arrKind: "sonarr",
      tagId: 11,
      tagLabel: "kids",
      profileId: 4,
    });
    expect(rows()).toEqual([["sonarr", "kids", "AV1", ""]]);
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "success", detail: "Added mapping for kids" }),
    );
    expect(error()).toBeUndefined();
    await click(buttonByText(view.root, "Add"));
    expect(error()).toBe("Select a target (Sonarr / Radarr / Both).");
  });

  it("drops a picked tag that no longer matches a changed target", async () => {
    await show([]);
    api.create.mockImplementation(async (m) => ({ ...m, id: 8 }));
    await pickOption(select("sel-kind"), "Sonarr");
    await pickOption(select("sel-tag"), "Series / anime");
    await settleTransitions();
    await pickOption(select("sel-kind"), "Radarr");
    await settleTransitions();
    expect(select("sel-kind").textContent).toContain("Radarr");
    await pickOption(select("sel-profile"), "HEVC");
    await click(buttonByText(view.root, "Add"));
    expect(api.create).not.toHaveBeenCalled();
    expect(error()).toBe("Select a tag.");
  });

  it("keeps the form when the create fails", async () => {
    await show([]);
    api.create.mockRejectedValue(new Error("POST /tag-mappings: 409"));
    await pickOption(select("sel-kind"), "Sonarr");
    await pickOption(select("sel-tag"), "Series / kids");
    await pickOption(select("sel-profile"), "AV1");
    await click(buttonByText(view.root, "Add"));
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't add mapping" }),
    );
    expect(rows()).toEqual([[expect.stringContaining("No mappings yet")]]);
    expect(select("sel-tag").textContent).toContain("Series / kids");
  });

  it("copes with the tag lookup failing", async () => {
    api.allTags.mockRejectedValue(new Error("GET /arr-instances/all-tags: 502"));
    await show([]);
    expect(await openOptions("sel-tag")).toEqual([]);
    expect(toastAdd).not.toHaveBeenCalled();
  });

  it("reloads tags on demand", async () => {
    await show([]);
    api.allTags.mockResolvedValue([tags[2]!]);
    await click(view.root.querySelector('button[title="Reload tags from *arr"]'));
    expect(api.allTags).toHaveBeenCalledTimes(2);
    expect(await openOptions("sel-tag")).toEqual(["Films / remux"]);
  });
});

describe("prefill from the unmapped-tags link", () => {
  it("selects the target and tag, then strips the prefill params", async () => {
    await show([], "/?tab=mappings&mapKind=radarr&mapInstance=2&mapTag=10");
    await flush();
    expect(select("sel-kind").textContent).toContain("Radarr");
    expect(select("sel-tag").textContent).toContain("Films / remux");
    expect(view.router.currentRoute.value.query).toEqual({ tab: "mappings" });
  });

  it("does nothing without a complete set of params", async () => {
    await show([], "/?tab=mappings&mapKind=radarr");
    await flush();
    expect(select("sel-tag").textContent).not.toContain("remux");
    expect(view.router.currentRoute.value.query).toEqual({ tab: "mappings", mapKind: "radarr" });
  });
});

describe("deleting a mapping", () => {
  it("removes it locally after confirmation", async () => {
    await show([
      { id: 1, arrKind: "sonarr", tagId: 10, tagLabel: "anime", profileId: 3 },
      { id: 2, arrKind: "radarr", tagId: 10, tagLabel: "remux", profileId: 4 },
    ]);
    api.remove.mockResolvedValue(undefined);
    await click(view.root.querySelector("tbody tr .pi-trash")?.closest("button") ?? null);
    expect(api.remove).not.toHaveBeenCalled();
    const opts = confirmRequire.mock.calls[0]?.[0];
    expect(opts.message).toBe('Delete "anime → HEVC"? This cannot be undone.');
    await opts.accept();
    await flush();
    expect(api.remove).toHaveBeenCalledWith(1);
    expect(rows()).toEqual([["radarr", "remux", "AV1", ""]]);
    expect(api.list).toHaveBeenCalledTimes(1);
  });
});
