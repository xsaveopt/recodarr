import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AuthStatus } from "@/api/client";

const { authStatus } = vi.hoisted(() => ({ authStatus: vi.fn<() => Promise<AuthStatus>>() }));

vi.mock("@/api/client", () => ({
  api: { auth: { status: () => authStatus() } },
}));

vi.mock("@/views/Login.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Setup.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Dashboard.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Jobs.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Library.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Settings.vue", () => ({ default: { render: () => null } }));
vi.mock("@/views/Debug.vue", () => ({ default: { render: () => null } }));

function status(setup: boolean, authed: boolean): AuthStatus {
  return { setup, authed, username: authed ? "admin" : "" };
}

async function navigate(path: string) {
  window.history.replaceState({}, "", "/");
  vi.resetModules();
  const { router } = await import("@/router");
  await router.push(path);
  return router.currentRoute.value;
}

beforeEach(() => {
  authStatus.mockReset();
});

describe("before setup", () => {
  beforeEach(() => {
    authStatus.mockResolvedValue(status(false, false));
  });

  it("sends every route to setup", async () => {
    for (const path of ["/", "/jobs", "/login", "/settings"]) {
      expect((await navigate(path)).name).toBe("setup");
    }
  });

  it("lets setup itself through", async () => {
    expect((await navigate("/setup")).name).toBe("setup");
  });
});

describe("set up but signed out", () => {
  beforeEach(() => {
    authStatus.mockResolvedValue(status(true, false));
  });

  it("redirects a protected route to login with a next param", async () => {
    const route = await navigate("/jobs?status=failed");
    expect(route.name).toBe("login");
    expect(route.query.next).toBe("/jobs?status=failed");
  });

  it("lets login through", async () => {
    const route = await navigate("/login");
    expect(route.name).toBe("login");
    expect(route.query.next).toBeUndefined();
  });

  it("bounces setup to login once an admin exists", async () => {
    expect((await navigate("/setup")).name).toBe("login");
  });
});

describe("signed in", () => {
  beforeEach(() => {
    authStatus.mockResolvedValue(status(true, true));
  });

  it("allows protected routes", async () => {
    for (const [path, name] of [
      ["/", "dashboard"],
      ["/jobs", "jobs"],
      ["/library", "library"],
      ["/settings", "settings"],
      ["/debug", "debug"],
    ]) {
      expect((await navigate(path)).name).toBe(name);
    }
  });

  it("sends login and setup to the dashboard", async () => {
    expect((await navigate("/login")).name).toBe("dashboard");
    expect((await navigate("/setup")).name).toBe("dashboard");
  });
});

describe("when the status call fails", () => {
  it("lets the navigation through", async () => {
    authStatus.mockRejectedValue(new Error("offline"));
    expect((await navigate("/settings")).name).toBe("settings");
  });
});
