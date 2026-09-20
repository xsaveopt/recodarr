import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";

import type { Theme } from "@/composables/useTheme";

const listeners: Array<() => void> = [];
let prefersDark = true;

function installMatchMedia(supported = true) {
  if (!supported) {
    Object.defineProperty(window, "matchMedia", { value: undefined, configurable: true });
    return;
  }
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn(() => ({
      get matches() {
        return prefersDark;
      },
      addEventListener: (_: string, fn: () => void) => {
        listeners.push(fn);
      },
      removeEventListener: () => {},
    })),
  });
}

async function loadTheme(stored?: string) {
  localStorage.clear();
  if (stored !== undefined) localStorage.setItem("recodarr.theme", stored);
  document.documentElement.className = "";
  vi.resetModules();
  return import("@/composables/useTheme");
}

function rootClasses(): string[] {
  return [...document.documentElement.classList];
}

beforeEach(() => {
  listeners.length = 0;
  prefersDark = true;
  installMatchMedia();
});

afterEach(() => {
  localStorage.clear();
});

describe("stored preference", () => {
  it("defaults to system when nothing is stored", async () => {
    const { useTheme } = await loadTheme();
    expect(useTheme().theme.value).toBe("system");
  });

  it("defaults to system when the stored value is junk", async () => {
    for (const junk of ["", "purple", "SYSTEM", "null"]) {
      const { useTheme } = await loadTheme(junk);
      expect(useTheme().theme.value).toBe("system");
    }
  });

  it("restores each valid stored value", async () => {
    for (const t of ["light", "dark", "system"] as Theme[]) {
      const { useTheme } = await loadTheme(t);
      expect(useTheme().theme.value).toBe(t);
    }
  });
});

describe("applying the class", () => {
  it("puts the stored theme on the document root at import", async () => {
    await loadTheme("light");
    expect(rootClasses()).toContain("light");
    expect(rootClasses()).not.toContain("dark");
  });

  it("resolves system against the media query", async () => {
    prefersDark = true;
    await loadTheme("system");
    expect(rootClasses()).toContain("dark");

    prefersDark = false;
    await loadTheme("system");
    expect(rootClasses()).toContain("light");
  });

  it("assumes dark when the browser has no matchMedia", async () => {
    installMatchMedia(false);
    await loadTheme("system");
    expect(rootClasses()).toContain("dark");
  });

  it("never leaves both classes on the root", async () => {
    const { useTheme } = await loadTheme("light");
    const { set } = useTheme();
    for (const t of ["dark", "system", "light"] as Theme[]) {
      set(t);
      await nextTick();
      const classes = rootClasses().filter((c) => c === "light" || c === "dark");
      expect(classes).toHaveLength(1);
    }
  });
});

describe("changing the theme", () => {
  it("cycles light to dark to system and back", async () => {
    const { useTheme } = await loadTheme("light");
    const { theme, cycle } = useTheme();
    cycle();
    expect(theme.value).toBe("dark");
    cycle();
    expect(theme.value).toBe("system");
    cycle();
    expect(theme.value).toBe("light");
  });

  it("persists the choice", async () => {
    const { useTheme } = await loadTheme("light");
    const { set } = useTheme();
    set("dark");
    await nextTick();
    expect(localStorage.getItem("recodarr.theme")).toBe("dark");
  });

  it("applies the new class without a reload", async () => {
    const { useTheme } = await loadTheme("light");
    const { set } = useTheme();
    set("dark");
    await nextTick();
    expect(rootClasses()).toContain("dark");
    expect(rootClasses()).not.toContain("light");
  });

  it("shares one theme across every caller", async () => {
    const { useTheme } = await loadTheme("light");
    const a = useTheme();
    const b = useTheme();
    a.set("dark");
    expect(b.theme.value).toBe("dark");
  });
});

describe("following the system", () => {
  it("re-resolves when the system flips and the theme is system", async () => {
    prefersDark = false;
    await loadTheme("system");
    expect(rootClasses()).toContain("light");

    prefersDark = true;
    for (const fn of listeners) fn();
    expect(rootClasses()).toContain("dark");
  });

  it("ignores a system flip when the theme is pinned", async () => {
    prefersDark = false;
    const { useTheme } = await loadTheme("light");
    useTheme();
    await nextTick();

    prefersDark = true;
    for (const fn of listeners) fn();
    expect(rootClasses()).toContain("light");
  });
});
