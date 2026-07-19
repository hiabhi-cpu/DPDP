import { describe, it, expect, vi, afterEach } from "vitest";
import { resolveClaim, capture, ApiError } from "./kiosk";

afterEach(() => vi.restoreAllMocks());

describe("kiosk api", () => {
  it("resolveClaim posts the otp and returns the claim", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({ session_id: "sess-1", mobile: "9999999999", name: "Asha Rao", hms_patient_id: "PA-1" }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const out = await resolveClaim("123456");

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/claim/resolve");
    expect(JSON.parse(init.body)).toEqual({ otp: "123456" });
    expect(out).toEqual({ session_id: "sess-1", mobile: "9999999999", name: "Asha Rao", hms_patient_id: "PA-1" });
  });

  it("resolveClaim throws ApiError on a non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "code not recognized" }), { status: 401 }),
    ));
    await expect(resolveClaim("000000")).rejects.toBeInstanceOf(ApiError);
  });

  it("capture posts session_id + purposes + hms_patient_id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await capture("9999999999", "sess-1", ["treatment"], "PA-1");

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/consent/capture");
    expect(JSON.parse(init.body)).toEqual({
      mobile: "9999999999",
      session_id: "sess-1",
      purposes: ["treatment"],
      hms_patient_id: "PA-1",
    });
  });

  it("post sends an abort signal so a hung request cannot hang forever", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ session_id: "s", mobile: "9", name: "A", hms_patient_id: "P" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await resolveClaim("123456");

    // Whether the abort actually fires is the browser's job; forgetting to pass
    // the signal is ours. That is what this asserts.
    const [, init] = fetchMock.mock.calls[0];
    expect(init.signal).toBeTruthy();
  });

  it("capture retries a 503 and succeeds on the next attempt", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 503 }))
      .mockResolvedValueOnce(new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await capture("9999999999", "sess-1", ["treatment"], "PA-1");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    // Both attempts carry the same session_id — that is what makes the replay
    // safe server-side (consent-service checks the key before the session verify).
    const sessions = fetchMock.mock.calls.map((c) => JSON.parse(c[1].body).session_id);
    expect(sessions).toEqual(["sess-1", "sess-1"]);
    vi.useRealTimers();
  });

  it("capture does not retry a 403 — an expired session will not heal", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "otp session invalid or expired — verify OTP first" }), { status: 403 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(capture("9999999999", "sess-1", ["treatment"], "PA-1")).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
