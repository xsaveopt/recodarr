import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buttonByText, click, flush, mountAt, typeInto, type Mounted } from "@/testing/mount";
import type { LibraryFile, LibraryInstance, LibraryItem, LibraryScan } from "@/types/api";

type Kind = "sonarr" | "radarr";

const { api, toastAdd } = vi.hoisted(() => ({
  api: {
    scan: vi.fn<(k: Kind, o: { deep?: boolean; refresh?: boolean }) => Promise<LibraryScan>>(),
    files: vi.fn<(k: Kind, inst: number, item: number) => Promise<LibraryFile[]>>(),
    addTag:
      vi.fn<
        (
          k: Kind,
          inst: number,
          item: number,
          tag: number,
        ) => Promise<{ tagId: number; tagLabel: string; profileId: number }>
      >(),
  },
  toastAdd: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: {
    library: {
      scan: (k: Kind, o: { deep?: boolean; refresh?: boolean }) => api.scan(k, o),
      files: (k: Kind, i: number, it: number) => api.files(k, i, it),
      addTag: (k: Kind, i: number, it: number, t: number) => api.addTag(k, i, it, t),
    },
  },
}));

vi.mock("primevue/usetoast", () => ({ useToast: () => ({ add: toastAdd }) }));

import LibraryPanel from "@/components/LibraryPanel.vue";

function item(itemId: number, over: Partial<LibraryItem> = {}): LibraryItem {
  return {
    instanceId: 1,
    instanceName: "Sonarr",
    itemId,
    title: `Show ${itemId}`,
    year: 2020,
    path: `/tv/${itemId}`,
    fileCount: 1,
    totalSize: 1024 ** 3,
    runtimeSeconds: 3600,
    bitrateBps: 2_000_000,
    bitrateExact: false,
    videoCodec: "h264",
    resolution: "1080p",
    tagLabels: [],
    mapped: false,
    mappedTags: [],
    ...over,
  };
}

function instance(over: Partial<LibraryInstance> = {}): LibraryInstance {
  return {
    id: 1,
    name: "Sonarr",
    tagCount: 2,
    mappableTags: [{ tagId: 7, tagLabel: "hevc", profileId: 3, profileName: "HEVC 1080p" }],
    ...over,
  };
}

function scan(items: LibraryItem[], instances = [instance()]): LibraryScan {
  return {
    kind: "sonarr",
    deep: false,
    scannedAt: new Date().toISOString(),
    instances,
    items,
  };
}

let view: Mounted;

function stat(label: string) {
  const s = [...view.root.querySelectorAll(".stat")].find(
    (el) => el.querySelector(".stat-label")?.textContent?.trim() === label,
  );
  return s?.querySelector(".stat-value")?.textContent?.trim();
}

function titles() {
  return [...view.root.querySelectorAll(".title-cell > span:first-child")].map((s) =>
    s.textContent?.trim(),
  );
}

function rowFor(title: string) {
  const row = [...view.root.querySelectorAll("tbody tr")].find(
    (r) => r.querySelector(".title-cell > span")?.textContent?.trim() === title,
  );
  if (!row) throw new Error(`no row for ${title}`);
  return row;
}

function checkbox(label: string) {
  const l = [...view.root.querySelectorAll("label.check")].find(
    (el) => el.textContent?.trim() === label,
  );
  return l?.querySelector("input") ?? null;
}

async function show(data: LibraryScan, kind: Kind = "sonarr") {
  api.scan.mockResolvedValue(data);
  view = await mountAt(LibraryPanel, "/", { kind });
}

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  toastAdd.mockReset();
});

afterEach(() => {
  view.unmount();
});

describe("scanning", () => {
  it("scans the given kind on mount and reports per-instance errors", async () => {
    await show(scan([], [instance({ name: "Radarr 4K", error: "401 unauthorized" })]), "radarr");
    expect(api.scan).toHaveBeenCalledWith("radarr", { deep: false, refresh: false });
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "error", detail: "Radarr 4K: 401 unauthorized" }),
    );
    expect(stat("movies")).toBe("0");
  });

  it("forces a refresh on rescan", async () => {
    await show(scan([]));
    await click(buttonByText(view.root, "Rescan"));
    expect(api.scan).toHaveBeenLastCalledWith("sonarr", { deep: false, refresh: true });
  });

  it("rescans with exact bitrates when asked", async () => {
    await show(scan([]));
    await click(checkbox("Exact bitrate (slower)"));
    expect(api.scan).toHaveBeenLastCalledWith("sonarr", { deep: true, refresh: false });
    expect(view.root.textContent).not.toContain("Bitrates are estimated");
  });
});

describe("summary and filters", () => {
  const items = () => [
    item(1, { title: "Alpha", totalSize: 2 * 1024 ** 3, runtimeSeconds: 7200 }),
    item(2, {
      title: "Beta",
      totalSize: 1024 ** 3,
      runtimeSeconds: 3600,
      mapped: true,
      tagLabels: ["hevc"],
      mappedTags: ["hevc"],
    }),
  ];

  it("totals size, bitrate and unmapped size", async () => {
    await show(scan(items()));
    expect(stat("series")).toBe("2");
    expect(stat("total size")).toBe("3.00 GB");
    expect(stat("not covered by a mapping")).toBe("2.00 GB");
    expect(stat("average bitrate")).toBe(`${((3 * 1024 ** 3 * 8) / 10800 / 1e6).toFixed(2)} Mbps`);
  });

  it("filters by title or tag, case-insensitively", async () => {
    await show(scan(items()));
    await typeInto(view.root.querySelector("input.search"), "ALPHA");
    expect(titles()).toEqual(["Alpha"]);
    await typeInto(view.root.querySelector("input.search"), "hev");
    expect(titles()).toEqual(["Beta"]);
    expect(stat("series")).toBe("1");
  });

  it("can hide mapped items", async () => {
    await show(scan(items()));
    await click(checkbox("Unmapped only"));
    expect(titles()).toEqual(["Alpha"]);
    expect(stat("not covered by a mapping")).toBe(stat("total size"));
  });
});

describe("tagging", () => {
  it("explains when the instance has no tags at all", async () => {
    await show(scan([item(1)], [instance({ tagCount: 0, mappableTags: [] })]));
    await click(rowFor("Show 1").querySelector('button[aria-label="Add to Recodarr"]'));
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({
        detail: expect.stringContaining("Sonarr has no tags. Create a tag in Sonarr first"),
      }),
    );
  });

  it("explains when no tag is mapped to a profile", async () => {
    await show(scan([item(1)], [instance({ mappableTags: [] })]));
    await click(rowFor("Show 1").querySelector('button[aria-label="Add to Recodarr"]'));
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({
        detail: expect.stringContaining("None of Sonarr's tags are mapped"),
      }),
    );
  });

  it("tags the item and marks it mapped", async () => {
    api.addTag.mockResolvedValue({ tagId: 7, tagLabel: "hevc", profileId: 3 });
    await show(scan([item(1)]));
    await click(rowFor("Show 1").querySelector('button[aria-label="Add to Recodarr"]'));
    const entry = [...document.body.querySelectorAll('[role="menuitem"]')].find(
      (m) => m.textContent?.trim() === "hevc",
    );
    await click(entry?.querySelector("a, div") ?? entry ?? null);
    expect(api.addTag).toHaveBeenCalledWith("sonarr", 1, 1, 7);
    const row = rowFor("Show 1");
    expect(row.textContent).toContain("mapped");
    expect(row.querySelector(".tag-chip.mappedTag")?.textContent).toBe("hevc");
    expect(row.querySelector('button[aria-label="Add another tag"]')).not.toBeNull();
    expect(toastAdd).toHaveBeenCalledWith(
      expect.objectContaining({
        severity: "success",
        detail: expect.stringContaining("encode it with HEVC 1080p"),
      }),
    );
  });
});

describe("expanding a row", () => {
  function expander(title: string) {
    return rowFor(title).querySelector('button[data-pc-section="rowtogglebutton"]');
  }

  it("loads the files once", async () => {
    api.files.mockResolvedValue([
      {
        fileId: 1,
        path: "/tv/1/e1.mkv",
        relativePath: "Season 1/e1.mkv",
        size: 1024 ** 3,
        runtimeSeconds: 2700,
        bitrateBps: 3_000_000,
        bitrateExact: true,
        videoBitrate: 2_500_000,
        audioBitrate: 500_000,
        videoCodec: "h264",
        audioCodec: "aac",
        resolution: "1920x1080",
        quality: "WEBDL-1080p",
      },
    ]);
    await show(scan([item(1)]));
    await click(expander("Show 1"));
    expect(api.files).toHaveBeenCalledWith("sonarr", 1, 1);
    expect(view.root.textContent).toContain("Season 1/e1.mkv");
    expect(view.root.textContent).toContain("2.50 Mbps");
    await click(expander("Show 1"));
    await click(expander("Show 1"));
    expect(api.files).toHaveBeenCalledTimes(1);
  });

  it("shows a load error and retries on the next expand", async () => {
    api.files.mockRejectedValueOnce(new Error("boom"));
    api.files.mockResolvedValueOnce([]);
    await show(scan([item(1)]));
    await click(expander("Show 1"));
    expect(view.root.textContent).toContain("Couldn't load files for this series.");
    await click(expander("Show 1"));
    await click(expander("Show 1"));
    await flush();
    expect(api.files).toHaveBeenCalledTimes(2);
    expect(view.root.textContent).toContain("No files.");
  });
});
