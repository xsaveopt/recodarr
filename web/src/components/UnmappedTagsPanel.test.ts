import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, flush, mountAt, type Mounted } from "@/testing/mount";
import type { UnmappedTag } from "@/types/api";

const { api, toastAdd } = vi.hoisted(() => ({
  api: { unmapped: vi.fn<() => Promise<UnmappedTag[]>>() },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: { arr: { unmappedTags: () => api.unmapped() } },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import UnmappedTagsPanel from "@/components/UnmappedTagsPanel.vue";

const tags: UnmappedTag[] = [
  {
    instanceId: 1,
    instanceName: "Series",
    kind: "sonarr",
    tagId: 10,
    tagLabel: "anime",
    itemCount: 4,
  },
  {
    instanceId: 2,
    instanceName: "Films",
    kind: "radarr",
    tagId: 3,
    tagLabel: "remux",
    itemCount: 1,
  },
];

let view: Mounted;

function rows() {
  return [...view.root.querySelectorAll("tbody tr")].map((r) =>
    [...r.querySelectorAll("td")].map((td) => td.textContent?.trim()),
  );
}

function rowFor(label: string) {
  const row = [...view.root.querySelectorAll("tbody tr")].find((r) =>
    [...r.querySelectorAll("td")].some((td) => td.textContent?.trim() === label),
  );
  if (!row) throw new Error(`no row for ${label}`);
  return row;
}

beforeEach(() => {
  api.unmapped.mockReset();
  toastAdd.mockReset();
  api.unmapped.mockResolvedValue(tags);
});

afterEach(() => {
  view.unmount();
});

describe("loading", () => {
  it("does not query the libraries until its tab is shown", async () => {
    view = await mountAt(UnmappedTagsPanel, "/?tab=mappings");
    expect(api.unmapped).not.toHaveBeenCalled();
    await view.router.replace({ query: { tab: "unmapped" } });
    await flush();
    expect(api.unmapped).toHaveBeenCalledTimes(1);
    expect(rows()).toEqual([
      ["sonarr", "anime", "Series", "4", "Map"],
      ["radarr", "remux", "Films", "1", "Map"],
    ]);
  });

  it("loads only once across tab switches", async () => {
    view = await mountAt(UnmappedTagsPanel, "/?tab=unmapped");
    await view.router.replace({ query: { tab: "arr" } });
    await flush();
    await view.router.replace({ query: { tab: "unmapped" } });
    await flush();
    expect(api.unmapped).toHaveBeenCalledTimes(1);
  });

  it("says so when every tag is mapped", async () => {
    api.unmapped.mockResolvedValue([]);
    view = await mountAt(UnmappedTagsPanel, "/?tab=unmapped");
    expect(view.root.textContent).toContain("Every tag applied to your library is mapped.");
  });

  it("does not claim everything is mapped when the check failed", async () => {
    api.unmapped.mockRejectedValue(new Error("GET /arr-instances/unmapped-tags: 502"));
    view = await mountAt(UnmappedTagsPanel, "/?tab=unmapped");
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ summary: "Couldn't load unmapped tags" }),
    );
    expect(view.root.textContent).not.toContain("Every tag applied to your library is mapped.");
  });

  it("re-checks on demand", async () => {
    view = await mountAt(UnmappedTagsPanel, "/?tab=unmapped");
    api.unmapped.mockResolvedValue([tags[1]!]);
    await click(view.root.querySelector('button[title="Re-check *arr libraries"]'));
    expect(api.unmapped).toHaveBeenCalledTimes(2);
    expect(rows()).toEqual([["radarr", "remux", "Films", "1", "Map"]]);
  });
});

describe("mapping a tag", () => {
  it("switches to the mappings tab with the prefill params and keeps the rest", async () => {
    view = await mountAt(UnmappedTagsPanel, "/?tab=unmapped&x=1");
    await click(buttonByText(rowFor("anime"), "Map"));
    expect(view.router.currentRoute.value.query).toEqual({
      x: "1",
      tab: "mappings",
      mapKind: "sonarr",
      mapInstance: "1",
      mapTag: "10",
    });
  });
});
