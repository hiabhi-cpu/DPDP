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

describe("consent wizard", () => {
  it("walks mobile → otp → consent → done and resets", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
      new Response(JSON.stringify({ session_id: "sess-1" }), { status: 200 }),
      new Response("{}", { status: 201 }),
    ]);

    render(<App />);
    await user.click(screen.getByRole("button", { name: /start/i }));

    await user.type(screen.getByLabelText(/mobile/i), "9999999999");
    await user.click(screen.getByRole("button", { name: /send otp/i }));

    await user.type(await screen.findByLabelText(/otp/i), "123456");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    // consent step: at least one purpose is granted by default; confirm.
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/thank you/i)).toBeInTheDocument();

    // auto-reset returns to the welcome screen.
    vi.advanceTimersByTime(6000);
    await waitFor(() => expect(screen.getByRole("button", { name: /start/i })).toBeInTheDocument());
    vi.useRealTimers();
  });

  it("shows an inline error and lets the patient retry when OTP is wrong", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
      new Response(JSON.stringify({ error: "invalid or expired OTP" }), { status: 401 }),
    ]);

    render(<App />);
    await user.click(screen.getByRole("button", { name: /start/i }));
    await user.type(screen.getByLabelText(/mobile/i), "9999999999");
    await user.click(screen.getByRole("button", { name: /send otp/i }));
    await user.type(await screen.findByLabelText(/otp/i), "000000");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    expect(await screen.findByText(/invalid or expired otp/i)).toBeInTheDocument();
    // still on the OTP step — can retry.
    expect(screen.getByLabelText(/otp/i)).toBeInTheDocument();
  });

  it("layout uses no fixed pixel widths on the shell/card", () => {
    // ?raw (Vite's built-in text-import query, typed by vite/client) instead of
    // node:fs + new URL(..., import.meta.url): under the jsdom test
    // environment, global URL is jsdom's polyfill, so Node's fs rejects it
    // with "The URL must be of scheme file" even though it prints as file://.
    // width declarations must be fluid (%/rem/vw/auto), never a hard px width.
    const widthPx = /(^|[^-])width:\s*\d+px/m.test(globalCss);
    expect(widthPx).toBe(false);
  });
});
