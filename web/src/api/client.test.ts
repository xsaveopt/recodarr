import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "@/api/client";

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: string | undefined;
}

let calls: Call[] = [];

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function errorResponse(status: number, text: string): Response {
  return {
    ok: false,
    status,
    statusText: "Error",
    json: async () => ({}),
    text: async () => text,
  } as unknown as Response;
}

function mockFetch(responder: (call: Call) => Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init: RequestInit) => {
      const call: Call = {
        url,
        method: init.method ?? "GET",
        headers: init.headers as Record<string, string>,
        body: init.body as string | undefined,
      };
      calls.push(call);
      return Promise.resolve(responder(call));
    }),
  );
}

function lastCall(): Call {
  const c = calls.at(-1);
  if (!c) throw new Error("no fetch call was made");
  return c;
}

beforeEach(() => {
  calls = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("request", () => {
  it("sends the CSRF header on every call", async () => {
    mockFetch(() => jsonResponse({}));
    await api.stats.get();
    expect(lastCall().headers["X-Recodarr"]).toBe("1");
  });

  it("sends same-origin credentials", async () => {
    const init: RequestInit[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, i: RequestInit) => {
        init.push(i);
        return Promise.resolve(jsonResponse({}));
      }),
    );
    await api.stats.get();
    expect(init[0]?.credentials).toBe("same-origin");
  });

  it("prefixes every path with /api", async () => {
    mockFetch(() => jsonResponse({}));
    await api.stats.get();
    expect(lastCall().url).toBe("/api/stats");
  });

  it("omits the body and content type on a bodyless call", async () => {
    mockFetch(() => jsonResponse({}));
    await api.stats.get();
    expect(lastCall().body).toBeUndefined();
    expect(lastCall().headers["Content-Type"]).toBeUndefined();
  });

  it("serializes a body as JSON and sets the content type", async () => {
    mockFetch(() => jsonResponse({}));
    await api.tagMappings.create({ arrKind: "sonarr", tagId: 7, tagLabel: "recode", profileId: 3 });
    const call = lastCall();
    expect(call.method).toBe("POST");
    expect(call.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(call.body ?? "")).toEqual({
      arrKind: "sonarr",
      tagId: 7,
      tagLabel: "recode",
      profileId: 3,
    });
  });

  it("returns undefined for a 204", async () => {
    mockFetch(
      () =>
        ({
          ok: true,
          status: 204,
          json: async () => {
            throw new Error("a 204 body must not be parsed");
          },
          text: async () => "",
        }) as unknown as Response,
    );
    await expect(api.auth.logout()).resolves.toBeUndefined();
  });

  it("throws with the method, path, status and body on a failure", async () => {
    mockFetch(() => errorResponse(500, "boom"));
    await expect(api.stats.get()).rejects.toThrow(/GET \/stats: 500 boom/);
  });

  it("falls back to the status text when the body cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve({
          ok: false,
          status: 502,
          statusText: "Bad Gateway",
          text: () => Promise.reject(new Error("no body")),
        } as unknown as Response),
      ),
    );
    await expect(api.stats.get()).rejects.toThrow(/502 Bad Gateway/);
  });
});

describe("401 handling", () => {
  const assign = vi.fn();

  beforeEach(() => {
    assign.mockClear();
    vi.stubGlobal("window", {
      location: { pathname: "/jobs", search: "?status=failed", assign },
    });
  });

  it("redirects to login and preserves where the user was", async () => {
    mockFetch(() => errorResponse(401, ""));
    await expect(api.stats.get()).rejects.toThrow("unauthorized");
    expect(assign).toHaveBeenCalledWith(`/login?next=${encodeURIComponent("/jobs?status=failed")}`);
  });

  it("does not redirect for an auth route", async () => {
    mockFetch(() => errorResponse(401, ""));
    await expect(api.auth.login("admin", "wrong")).rejects.toThrow();
    expect(assign).not.toHaveBeenCalled();
  });

  it("does not redirect when already on login", async () => {
    vi.stubGlobal("window", { location: { pathname: "/login", search: "", assign } });
    mockFetch(() => errorResponse(401, ""));
    await expect(api.stats.get()).rejects.toThrow("unauthorized");
    expect(assign).not.toHaveBeenCalled();
  });

  it("does not redirect when already on setup", async () => {
    vi.stubGlobal("window", { location: { pathname: "/setup", search: "", assign } });
    mockFetch(() => errorResponse(401, ""));
    await expect(api.stats.get()).rejects.toThrow("unauthorized");
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("query building", () => {
  beforeEach(() => {
    mockFetch(() => jsonResponse({}));
  });

  it("omits the query string when no job filters are set", async () => {
    await api.jobs.list();
    expect(lastCall().url).toBe("/api/jobs");
  });

  it("includes only the job filters that were given", async () => {
    await api.jobs.list({ status: "failed", limit: 25, offset: 50, sort: "updated", order: "asc" });
    const url = new URL(lastCall().url, "http://x");
    expect(url.pathname).toBe("/api/jobs");
    expect(Object.fromEntries(url.searchParams)).toEqual({
      status: "failed",
      limit: "25",
      offset: "50",
      sort: "updated",
      order: "asc",
    });
  });

  it("keeps a zero offset rather than dropping it", async () => {
    await api.jobs.list({ offset: 0, limit: 0 });
    const url = new URL(lastCall().url, "http://x");
    expect(url.searchParams.get("offset")).toBe("0");
    expect(url.searchParams.get("limit")).toBe("0");
  });

  it("drops a zero profile id, which means no filter", async () => {
    await api.jobs.list({ profileId: 0 });
    expect(lastCall().url).toBe("/api/jobs");
  });

  it("builds the library scan query from its options", async () => {
    await api.library.scan("sonarr");
    expect(lastCall().url).toBe("/api/library/sonarr");

    await api.library.scan("radarr", { deep: true, refresh: true });
    const url = new URL(lastCall().url, "http://x");
    expect(url.pathname).toBe("/api/library/radarr");
    expect(url.searchParams.get("deep")).toBe("true");
    expect(url.searchParams.get("refresh")).toBe("true");

    await api.library.scan("sonarr", { deep: false, refresh: false });
    expect(lastCall().url).toBe("/api/library/sonarr");
  });

  it("asks for deleted profiles only when requested", async () => {
    await api.profiles.list();
    expect(lastCall().url).toBe("/api/profiles");
    await api.profiles.list({ includeDeleted: true });
    expect(lastCall().url).toBe("/api/profiles?includeDeleted=true");
  });

  it("encodes the status list when clearing terminal jobs", async () => {
    await api.jobs.clearTerminal(["done", "failed"]);
    expect(lastCall().url).toBe(`/api/jobs?status=${encodeURIComponent("done,failed")}`);
    expect(lastCall().method).toBe("DELETE");

    await api.jobs.clearTerminal([]);
    expect(lastCall().url).toBe("/api/jobs");
    await api.jobs.clearTerminal();
    expect(lastCall().url).toBe("/api/jobs");
  });

  it("puts resource ids in the path", async () => {
    await api.jobs.retry(42);
    expect(lastCall().url).toBe("/api/jobs/42/retry");
    await api.arr.remove(7);
    expect(lastCall().url).toBe("/api/arr-instances/7");
    expect(lastCall().method).toBe("DELETE");
    await api.library.files("radarr", 2, 99);
    expect(lastCall().url).toBe("/api/library/radarr/2/99/files");
  });

  it("sends the tag id in the body when tagging a library item", async () => {
    await api.library.addTag("sonarr", 1, 5, 12);
    expect(lastCall().url).toBe("/api/library/sonarr/1/5/tags");
    expect(JSON.parse(lastCall().body ?? "")).toEqual({ tagId: 12 });
  });
});
