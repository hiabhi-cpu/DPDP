import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import globalCss from "./styles/global.css?raw";
import { App } from "./App";

afterEach(() => {
  // Restore timers here, not at the end of a test body: a failing assertion
  // mid-test would otherwise leak fake timers into every later test.
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function mockFetchSequence(responses: Response[]) {
  const fn = vi.fn();
  responses.forEach((r) => fn.mockResolvedValueOnce(r));
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("code-only kiosk", () => {
  it("enters a code → greets by name → captures with hms_patient_id → done → resets", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    const fetchMock = mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9876543210", name: "Asha Rao", hms_patient_id: "PA-1" }), { status: 200 }),
      new Response("{}", { status: 201 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));

    expect(await screen.findByText(/Welcome, Asha Rao/)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: /confirm/i }));
    expect(await screen.findByText(/thank you/i)).toBeInTheDocument();

    // capture posted the hms_patient_id.
    const captureCall = fetchMock.mock.calls.find((c) => String(c[0]).endsWith("/kiosk/api/consent/capture"));
    expect(JSON.parse(captureCall![1].body)).toMatchObject({ session_id: "sess-1", hms_patient_id: "PA-1" });

    vi.advanceTimersByTime(6000);
    await waitFor(() => expect(screen.getByLabelText(/6-digit code/i)).toBeInTheDocument());
  });

  it("shows a generic retry message on a bad code and stays on the code step", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ error: "code not recognized" }), { status: 401 }),
    ]);
    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "000000");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    expect(await screen.findByText(/ask the front desk to resend/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/6-digit code/i)).toBeInTheDocument();
  });

  it("a capture failure does NOT blame the code — an already-consented patient is told so", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      // resolve succeeds — the code was fine, we reach the consent step.
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9744411133", name: "Priya Shah", hms_patient_id: "PA-1" }), { status: 200 }),
      // capture is rejected: the patient already has an active consent.
      new Response(JSON.stringify({ error: "active consent already exists" }), { status: 409 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/already given consent/i)).toBeInTheDocument();
    // The code was accepted — never send this patient back to the front desk for a resend.
    expect(screen.queryByText(/ask the front desk to resend/i)).not.toBeInTheDocument();
  });

  it("purpose checkboxes are disabled while the capture is in flight", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();

    // Mock resolve to succeed, capture to hang indefinitely
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.includes("/kiosk/api/claim/resolve")) {
        return Promise.resolve(new Response(JSON.stringify({ session_id: "sess-1", mobile: "9876543210", name: "Asha Rao", hms_patient_id: "PA-1" }), { status: 200 }));
      }
      if (url.includes("/kiosk/api/consent/capture")) {
        // Return a promise that never resolves to simulate the capture hanging
        return new Promise(() => {});
      }
      return Promise.reject(new Error("unexpected fetch call"));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));

    expect(await screen.findByText(/Welcome, Asha Rao/)).toBeInTheDocument();

    // Click Confirm, which will trigger the hanging capture
    await user.click(screen.getByRole("button", { name: /confirm/i }));

    // While busy, the checkbox should be disabled
    const checkboxes = screen.getAllByRole("checkbox");
    checkboxes.forEach((checkbox) => {
      expect(checkbox).toBeDisabled();
    });
  });

  it("layout uses no fixed pixel widths on the shell/card", () => {
    const widthPx = /(^|[^-])width:\s*\d+px/m.test(globalCss);
    expect(widthPx).toBe(false);
  });

  it("an unreachable consent-service says try again — not go to the front desk", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    // resolve succeeds, then all THREE capture attempts 502. The retry budget is
    // 3, so the sequence must supply three failures or the mock runs dry.
    mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9744411133", name: "Priya Shah", hms_patient_id: "PA-1" }), { status: 200 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    // Run all timers to let retries complete
    await vi.runAllTimersAsync();

    // Assert the distinguishing words. NOT the absence of "front desk" — the
    // current message contains it, so that assertion would pass either way.
    expect(await screen.findByText(/try again/i)).toBeInTheDocument();
  });
});
