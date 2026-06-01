import { ref, watchEffect } from "vue";

export type Theme = "light" | "dark" | "system";

const STORAGE_KEY = "recodarr.theme";

function readStored(): Theme {
  const v = localStorage.getItem(STORAGE_KEY);
  return v === "light" || v === "dark" || v === "system" ? v : "system";
}

function systemPrefersDark(): boolean {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? true;
}

function applyClass(t: Theme) {
  const root = document.documentElement;
  const effective = t === "system" ? (systemPrefersDark() ? "dark" : "light") : t;
  root.classList.remove("light", "dark");
  root.classList.add(effective);
}

const theme = ref<Theme>(readStored());

applyClass(theme.value);

const mql = window.matchMedia?.("(prefers-color-scheme: dark)");
mql?.addEventListener?.("change", () => {
  if (theme.value === "system") applyClass("system");
});

export function useTheme() {
  watchEffect(() => {
    applyClass(theme.value);
    localStorage.setItem(STORAGE_KEY, theme.value);
  });

  function cycle() {
    theme.value = theme.value === "light" ? "dark" : theme.value === "dark" ? "system" : "light";
  }

  function set(t: Theme) {
    theme.value = t;
  }

  return { theme, cycle, set };
}
