import { afterEach, describe, expect, it, vi } from "vitest";
import { h } from "vue";

import { click, mountAt, type Mounted } from "@/testing/mount";

function stub(name: string) {
  return { default: { render: () => h("div", { "data-panel": name }) } };
}

vi.mock("@/components/ArrInstancesPanel.vue", () => stub("arr"));
vi.mock("@/components/QbitPanel.vue", () => stub("qbit"));
vi.mock("@/components/ProfilesPanel.vue", () => stub("profiles"));
vi.mock("@/components/MappingsPanel.vue", () => stub("mappings"));
vi.mock("@/components/UnmappedTagsPanel.vue", () => stub("unmapped"));
vi.mock("@/components/WorkerPanel.vue", () => stub("worker"));
vi.mock("@/components/NotificationsPanel.vue", () => stub("notifications"));
vi.mock("@/components/LogsPanel.vue", () => stub("logs"));
vi.mock("@/components/AgentPanel.vue", () => stub("agent"));

import Settings from "@/views/Settings.vue";

let view: Mounted;

afterEach(() => {
  view.unmount();
});

function shownPanels() {
  return [...view.root.querySelectorAll<HTMLElement>("[data-panel]")]
    .filter((el) => el.closest<HTMLElement>('[role="tabpanel"]')?.style.display !== "none")
    .map((el) => el.dataset.panel);
}

function tab(label: string) {
  const found = [...view.root.querySelectorAll('[role="tab"]')].find(
    (t) => t.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no tab "${label}"`);
  return found;
}

describe("Settings tabs", () => {
  it("opens on the arr tab by default", async () => {
    view = await mountAt(Settings, "/settings");
    expect(shownPanels()).toEqual(["arr"]);
  });

  it("opens the tab named in the query", async () => {
    view = await mountAt(Settings, "/settings?tab=profiles");
    expect(shownPanels()).toEqual(["profiles"]);
  });

  it("falls back to arr for an unknown tab", async () => {
    view = await mountAt(Settings, "/settings?tab=nope");
    expect(shownPanels()).toEqual(["arr"]);
  });

  it("writes the picked tab into the query and keeps other params", async () => {
    view = await mountAt(Settings, "/settings?x=1");
    await click(tab("Logs"));
    expect(view.router.currentRoute.value.query).toEqual({ x: "1", tab: "logs" });
    expect(shownPanels()).toEqual(["logs"]);
  });

  it("drops the tab param when going back to arr", async () => {
    view = await mountAt(Settings, "/settings?tab=worker");
    await click(tab("Sonarr / Radarr"));
    expect(view.router.currentRoute.value.query.tab).toBeUndefined();
  });
});
