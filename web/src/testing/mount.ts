import { createApp, nextTick, type Component } from "vue";
import { createMemoryHistory, createRouter, type Router } from "vue-router";
import PrimeVue from "primevue/config";
import ConfirmationService from "primevue/confirmationservice";
import ToastService from "primevue/toastservice";

export interface Mounted {
  root: HTMLElement;
  router: Router;
  unmount: () => void;
}

class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

if (!("ResizeObserver" in globalThis)) {
  Object.assign(globalThis, { ResizeObserver: NoopResizeObserver });
}

if (typeof window.matchMedia !== "function") {
  Object.assign(window, {
    matchMedia: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener() {},
      removeListener() {},
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent: () => false,
    }),
  });
}

export async function mountAt(
  component: Component,
  path = "/",
  props: Record<string, unknown> = {},
): Promise<Mounted> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { render: () => null } },
      { path: "/login", name: "login", meta: { public: true }, component: { render: () => null } },
      { path: "/:rest(.*)*", name: "other", component: { render: () => null } },
    ],
  });
  await router.push(path);
  await router.isReady();
  const root = document.createElement("div");
  document.body.appendChild(root);
  const app = createApp(component, props);
  app.use(router);
  app.use(PrimeVue, { unstyled: true });
  app.use(ToastService);
  app.use(ConfirmationService);
  app.mount(root);
  await flush();
  return {
    root,
    router,
    unmount: () => {
      app.unmount();
      root.remove();
      document.body.innerHTML = "";
    },
  };
}

export async function flush(times = 5) {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    await nextTick();
  }
  await new Promise((resolve) => setTimeout(resolve, 0));
  await nextTick();
}

export async function typeInto(el: Element | null, value: string) {
  if (!(el instanceof HTMLInputElement) && !(el instanceof HTMLTextAreaElement)) {
    throw new Error("not an input");
  }
  el.value = value;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  await flush();
}

export function buttonByText(root: ParentNode, text: string): HTMLButtonElement {
  const found = [...root.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === text || b.getAttribute("aria-label") === text,
  );
  if (!found) throw new Error(`no button labelled "${text}"`);
  return found;
}

export async function click(el: Element | null) {
  if (!(el instanceof HTMLElement)) throw new Error("nothing to click");
  el.click();
  await flush();
}

export async function submit(form: Element | null) {
  if (!(form instanceof HTMLFormElement)) throw new Error("not a form");
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await flush();
}

export async function pickOption(select: Element | null, option: string) {
  await click(select);
  const opt = [...document.body.querySelectorAll('[role="option"]')].find(
    (o) => o.getAttribute("aria-label") === option || o.textContent?.trim() === option,
  );
  if (!opt) throw new Error(`no option "${option}"`);
  opt.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  await flush();
}

export async function settleTransitions() {
  await new Promise((resolve) => setTimeout(resolve, 50));
  await flush();
}
