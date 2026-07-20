// The capture retry, against real outages.
//
// What only this suite can prove: that AbortSignal.timeout actually fires. A
// unit test with fake timers proves the retry loop calls fetch three times; it
// cannot prove that a genuinely hung TCP connection ever gives up. Without the
// timeout the kiosk spins forever — the bug the timeout was added for.
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../src/App";
import { pointFetchAtLiveBff, withOutage, code } from "./helpers";

pointFetchAtLiveBff();

async function reachConsentStep(user: ReturnType<typeof userEvent.setup>, otp: string) {
  render(<App />);
  await user.type(screen.getByLabelText(/6-digit code/i), otp);
  await user.click(screen.getByRole("button", { name: /continue/i }));
  await screen.findByRole("button", { name: /confirm/i }, { timeout: 10_000 });
}

describe("LIVE: capture retry", () => {
  it("a REFUSED connection retries 3× at ~1s and keeps the patient informed", async () => {
    const user = userEvent.setup();
    await reachConsentStep(user, code("CODE_RETRY_REFUSED"));

    const attempts: number[] = [];
    const realFetch = globalThis.fetch;
    globalThis.fetch = ((url: string, init?: RequestInit) => {
      if (String(url).includes("/consent/capture")) attempts.push(Date.now());
      return realFetch(url, init);
    }) as typeof fetch;

    await withOutage("dpdp-consent", 9000, "stop", async () => {
      await user.click(screen.getByRole("button", { name: /confirm/i }));

      // Mid-flight the patient must see progress and be unable to change their
      // answer — onConfirm has already snapshotted the purposes.
      expect(await screen.findByRole("button", { name: /saving/i })).toBeInTheDocument();
      screen.getAllByRole("checkbox").forEach((b) => expect(b).toBeDisabled());

      await screen.findByRole("alert", {}, { timeout: 30_000 });
    });

    const gaps = attempts.slice(1).map((t, i) => t - attempts[i]);
    console.log(`[live] refused: ${attempts.length} attempts, gaps ${gaps.map((g) => g + "ms").join(", ")}`);
    // Assert the budget, not wall-clock: a refused connection fails instantly,
    // so the ~17s worst case belongs to the hung test below, not this one.
    expect(attempts.length).toBe(3);
    gaps.forEach((g) => expect(g).toBeGreaterThanOrEqual(1000));

    // Controls must come back so the patient can retry themselves.
    expect(screen.getByRole("button", { name: /confirm/i })).toBeEnabled();
    globalThis.fetch = realFetch;
  });

  it("a HUNG connection still gives up — proving the 5s timeout fires", async () => {
    const user = userEvent.setup();
    await reachConsentStep(user, code("CODE_RETRY_HUNG"));

    const attempts: number[] = [];
    const realFetch = globalThis.fetch;
    globalThis.fetch = ((url: string, init?: RequestInit) => {
      if (String(url).includes("/consent/capture")) attempts.push(Date.now());
      return realFetch(url, init);
    }) as typeof fetch;

    const t0 = Date.now();
    await withOutage("dpdp-consent", 9000, "pause", async () => {
      await user.click(screen.getByRole("button", { name: /confirm/i }));
      await screen.findByRole("alert", {}, { timeout: 45_000 });
    });
    const elapsed = Date.now() - t0;

    const gaps = attempts.slice(1).map((t, i) => t - attempts[i]);
    console.log(`[live] hung: gave up after ${(elapsed / 1000).toFixed(1)}s, gaps ${gaps.map((g) => g + "ms").join(", ")}`);
    expect(attempts.length).toBe(3);
    // ~6s per attempt = the 5s AbortSignal.timeout + the 1s gap. If the timeout
    // did not fire these would never elapse and the test would time out.
    gaps.forEach((g) => expect(g).toBeGreaterThan(5000));
    expect(elapsed).toBeGreaterThan(12_000);
    expect(elapsed).toBeLessThan(25_000);

    globalThis.fetch = realFetch;
  });
});
