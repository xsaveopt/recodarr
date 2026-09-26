import { afterEach, describe, expect, it, vi } from "vitest";
import { h } from "vue";

import { click, mountAt, type Mounted } from "@/testing/mount";

vi.mock("@/components/LibraryPanel.vue", () => ({
  default: {
    props: ["kind"],
    setup(props: { kind: string }) {
      return () => h("div", { "data-kind": props.kind });
    },
  },
}));

import Library from "@/views/Library.vue";

let view: Mounted;

afterEach(() => {
  view.unmount();
});

function shownKinds() {
  return [...view.root.querySelectorAll<HTMLElement>("[data-kind]")]
    .filter((el) => el.closest<HTMLElement>('[role="tabpanel"]')?.style.display !== "none")
    .map((el) => el.dataset.kind);
}

function tab(label: string) {
  const found = [...view.root.querySelectorAll('[role="tab"]')].find(
    (t) => t.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no tab "${label}"`);
  return found;
}

describe("Library tabs", () => {
  it("opens on sonarr by default", async () => {
    view = await mountAt(Library, "/library");
    expect(shownKinds()).toEqual(["sonarr"]);
  });

  it("opens radarr when the query asks for it", async () => {
    view = await mountAt(Library, "/library?tab=radarr");
    expect(shownKinds()).toEqual(["radarr"]);
  });

  it("falls back to sonarr for an unknown tab", async () => {
    view = await mountAt(Library, "/library?tab=lidarr");
    expect(shownKinds()).toEqual(["sonarr"]);
  });

  it("writes the picked tab into the query and keeps other params", async () => {
    view = await mountAt(Library, "/library?x=1");
    await click(tab("Radarr"));
    expect(view.router.currentRoute.value.query).toEqual({ x: "1", tab: "radarr" });
    expect(shownKinds()).toEqual(["radarr"]);
  });

  it("drops the tab param when going back to sonarr", async () => {
    view = await mountAt(Library, "/library?tab=radarr&x=1");
    await click(tab("Sonarr"));
    expect(view.router.currentRoute.value.query).toEqual({ x: "1" });
  });
});
