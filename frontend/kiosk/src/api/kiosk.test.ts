import { describe, it, expect, vi, afterEach } from "vitest";
import { sendOtp, verifyOtp, capture, ApiError } from "./kiosk";

afterEach(() => vi.restoreAllMocks());

describe("kiosk api", () => {
  it("posts mobile to /kiosk/api/otp/send and returns reference_id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const out = await sendOtp("9999999999");

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/otp/send");
    expect(JSON.parse(init.body)).toEqual({ mobile: "9999999999" });
    expect(out.reference_id).toBe("ref-1");
  });

  it("throws ApiError on non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid or expired OTP" }), { status: 401 }),
    ));
    await expect(verifyOtp("9999999999", "ref-1", "000000")).rejects.toBeInstanceOf(ApiError);
  });

  it("capture posts session_id + purposes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await capture("9999999999", "sess-1", ["treatment"]);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/consent/capture");
    expect(JSON.parse(init.body)).toEqual({
      mobile: "9999999999",
      session_id: "sess-1",
      purposes: ["treatment"],
    });
  });
});
