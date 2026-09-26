import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { mountAt, submit, typeInto, type Mounted } from "@/testing/mount";

const { login } = vi.hoisted(() => ({
  login: vi.fn<(u: string, p: string) => Promise<{ username: string }>>(),
}));

vi.mock("@/api/client", () => ({
  api: { auth: { login: (u: string, p: string) => login(u, p) } },
}));

import Login from "@/views/Login.vue";

let view: Mounted;

async function fillAndSubmit(user: string, pass: string) {
  await typeInto(view.root.querySelector("form input:not(#pw)"), user);
  await typeInto(view.root.querySelector("#pw"), pass);
  await submit(view.root.querySelector("form"));
}

beforeEach(() => {
  login.mockReset();
});

afterEach(() => {
  view.unmount();
});

describe("Login", () => {
  it("sends the credentials and goes to the dashboard", async () => {
    login.mockResolvedValue({ username: "admin" });
    view = await mountAt(Login, "/login");
    await fillAndSubmit("admin", "hunter22");
    expect(login).toHaveBeenCalledWith("admin", "hunter22");
    expect(view.router.currentRoute.value.fullPath).toBe("/");
  });

  it("returns to the page named in next", async () => {
    login.mockResolvedValue({ username: "admin" });
    view = await mountAt(Login, "/login?next=%2Fjobs%3Fstatus%3Dfailed");
    await fillAndSubmit("admin", "pw");
    expect(view.router.currentRoute.value.fullPath).toBe("/jobs?status=failed");
  });

  it("shows the error and stays put when login fails", async () => {
    login.mockRejectedValue(new Error("invalid credentials"));
    view = await mountAt(Login, "/login");
    await fillAndSubmit("admin", "wrong");
    expect(view.root.textContent).toContain("invalid credentials");
    expect(view.router.currentRoute.value.path).toBe("/login");
  });

  it("clears an old error on the next attempt", async () => {
    login.mockRejectedValueOnce(new Error("invalid credentials"));
    login.mockResolvedValueOnce({ username: "admin" });
    view = await mountAt(Login, "/login");
    await fillAndSubmit("admin", "wrong");
    await fillAndSubmit("admin", "right");
    expect(view.root.textContent).not.toContain("invalid credentials");
  });
});
