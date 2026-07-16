import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import globalCss from "./styles/global.css?raw";
import { App } from "./App";

afterEach(() => vi.restoreAllMocks());

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
    vi.useRealTimers();
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

  it("layout uses no fixed pixel widths on the shell/card", () => {
    const widthPx = /(^|[^-])width:\s*\d+px/m.test(globalCss);
    expect(widthPx).toBe(false);
  });
});
