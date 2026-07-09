import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "./client";

describe("api client", () => {
  beforeEach(() => {
    document.cookie = "csrf_token=tok-123";
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("attaches the CSRF header on mutating requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ email: "a@b.c", role: "admin" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.login("a@b.c", "pw");

    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers["X-CSRF-Token"]).toBe("tok-123");
    expect(init.credentials).toBe("include");
  });

  it("throws ApiError on non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "bad" }), { status: 401 }),
    ));
    await expect(api.getStats(30)).rejects.toBeInstanceOf(ApiError);
  });
});
