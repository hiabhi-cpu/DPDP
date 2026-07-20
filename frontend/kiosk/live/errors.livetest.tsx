// The consent-step error copy, against real outages.
//
// What only this suite can prove: that the status a real service emits is the
// one the mapping expects. Every unit test here mocks fetch, so a kiosk-bff
// that returned 500 instead of 502 would pass them all and still show the wrong
// sentence on a tablet.
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../src/App";
import { pointFetchAtLiveBff, withOutage, sh, code } from "./helpers";

pointFetchAtLiveBff();

describe("LIVE: consent-step error copy", () => {
  it("an expired session says so AND returns the patient to the code step", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), code("CODE_EXPIRED"));
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await screen.findByRole("button", { name: /confirm/i }, { timeout: 10_000 });

    // Force a REAL server-side 403 by dropping the session from Redis, rather
    // than waiting out the 15-minute TTL. notification-service then fails
    // ValidateSession exactly as a true expiry would.
    const keys = sh(`docker exec dpdp-redis redis-cli KEYS 'session:*'`).split("\n").filter(Boolean);
    expect(keys.length).toBeGreaterThan(0);
    sh(`docker exec dpdp-redis redis-cli DEL ${keys.join(" ")}`);

    await user.click(screen.getByRole("button", { name: /confirm/i }));

    // "expired" is the only word no other message contains — asserting on
    // "front desk" would match the code-step message too.
    expect(await screen.findByText(/expired/i, {}, { timeout: 15_000 })).toBeInTheDocument();
    // The fix that matters: a resent code needs a field waiting for it. The
    // consent step has none, so the patient must be back on code entry.
    expect(await screen.findByLabelText(/6-digit code/i)).toBeInTheDocument();
    console.log("[live] 403 → 'expired' AND back on the code step");
  });

  it("a code-service outage says try again — not that the code was wrong", async () => {
    const user = userEvent.setup();
    render(<App />);

    // kiosk-bff answers 502 "code service unavailable" when notification-service
    // is unreachable. That used to render as "Code not recognized", blaming a
    // perfectly good code for someone else's outage.
    await withOutage("dpdp-notification", 9004, "stop", async () => {
      await user.type(screen.getByLabelText(/6-digit code/i), code("CODE_CODE_OUTAGE"));
      await user.click(screen.getByRole("button", { name: /continue/i }));

      expect(await screen.findByText(/try again/i, {}, { timeout: 20_000 })).toBeInTheDocument();
      expect(screen.queryByText(/not recognized/i)).not.toBeInTheDocument();
      console.log("[live] code-step 502 → 'try again', not 'Code not recognized'");
    });
  });

  it("an unreachable consent-service says try again — not go to the front desk", async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), code("CODE_CAPTURE_OUTAGE"));
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await screen.findByRole("button", { name: /confirm/i }, { timeout: 10_000 });

    await withOutage("dpdp-consent", 9000, "stop", async () => {
      await user.click(screen.getByRole("button", { name: /confirm/i }));
      expect(await screen.findByText(/try again/i, {}, { timeout: 30_000 })).toBeInTheDocument();
      expect(screen.queryByText(/could not save your consent/i)).not.toBeInTheDocument();
      console.log("[live] capture 502 → 'try again', not the front-desk message");
    });
  });
});
