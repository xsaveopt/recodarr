import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { mountAt, submit, typeInto, type Mounted } from "@/testing/mount";

const { setup } = vi.hoisted(() => ({
  setup: vi.fn<(u: string, p: string) => Promise<{ username: string }>>(),
}));

vi.mock("@/api/client", () => ({
  api: { auth: { setup: (u: string, p: string) => setup(u, p) } },
}));

import Setup from "@/views/Setup.vue";

let view: Mounted;

function usernameInput() {
  return view.root.querySelector("form input:not(#pw1):not(#pw2)");
}

async function fill(pw: string, confirm: string) {
  await typeInto(view.root.querySelector("#pw1"), pw);
  await typeInto(view.root.querySelector("#pw2"), confirm);
  await submit(view.root.querySelector("form"));
}

beforeEach(async () => {
  setup.mockReset();
  view = await mountAt(Setup, "/setup");
});

afterEach(() => {
  view.unmount();
});

describe("Setup", () => {
  it("prefills the username with admin", () => {
    expect((usernameInput() as HTMLInputElement).value).toBe("admin");
  });

  it("refuses mismatched passwords without calling the API", async () => {
    await fill("secret-one", "secret-two");
    expect(view.root.textContent).toContain("Passwords do not match.");
    expect(setup).not.toHaveBeenCalled();
  });

  it("creates the admin and goes to the dashboard", async () => {
    setup.mockResolvedValue({ username: "root" });
    await typeInto(usernameInput(), "root");
    await fill("same-secret", "same-secret");
    expect(setup).toHaveBeenCalledWith("root", "same-secret");
    expect(view.router.currentRoute.value.fullPath).toBe("/");
  });

  it("shows the server error and stays on setup", async () => {
    setup.mockRejectedValue(new Error("POST /auth/setup: 409 already set up"));
    await fill("same-secret", "same-secret");
    expect(view.root.textContent).toContain("409 already set up");
    expect(view.router.currentRoute.value.path).toBe("/setup");
  });

  it("drops the mismatch error once the passwords agree", async () => {
    setup.mockResolvedValue({ username: "admin" });
    await fill("a", "b");
    await fill("same", "same");
    expect(view.root.textContent).not.toContain("Passwords do not match.");
  });
});
