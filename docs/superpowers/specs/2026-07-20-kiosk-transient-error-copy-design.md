# Kiosk consent-step: one message for an unreachable service — design

**Date:** 2026-07-20
**Status:** approved, not yet planned
**Follows:** `2026-07-15-kiosk-capture-retry-design.md` (merged 2026-07-20, `11c38a1`)

## The defect

Found by live testing the merged capture retry against a real outage, not by
review or unit tests — both failure paths pass their own tests in isolation.

`consentError` in `frontend/kiosk/src/App.tsx` gives **contradictory advice for
the same situation**:

| How consent-service failed | What `capture()` throws | What the patient is told |
|---|---|---|
| **Hung** (`docker pause` — TCP connects, nothing answers) | a thrown fetch, aborted by the 5s timeout — not an `ApiError` | "Something went wrong. Please try again." |
| **Refused** (`docker stop` — connection refused) | `ApiError` 502 from kiosk-bff | "We could not save your consent. Please ask the front desk for help." |

Both are "consent-service is unreachable". The patient cannot tell them apart and
neither can the front desk. Measured live:

- hung: gave up after 17.0s, 3 attempts, 6007ms / 6002ms each (5s timeout + 1s gap)
- refused: gave up after ~2s, 3 attempts, gaps 1022ms / 1062ms

## Why "try again" is the right advice

Front-desk staff cannot fix a down service. Sending the patient there is wasted
walking for them and wasted time for staff, at exactly the moment staff are least
able to help.

Pressing Confirm again is **idempotent-safe** — the capture reuses its
`session_id`, and consent-service checks the idempotency key before the session
verify, so a replay returns the original row. Live-proven: consent-service came
back healthy 2s after restart and a second press captured successfully.

This is also the branch's own thesis. The capture retry exists to stop sending
patients to the front desk over a blip; leaving the refused path saying "ask the
front desk" contradicts the feature that ships with it.

## Design

Export the existing `retryable` predicate from `frontend/kiosk/src/api/kiosk.ts`
and use it in `consentError`:

```ts
function consentError(e: unknown): string {
  // Same predicate the retry loop uses: if it was worth retrying automatically,
  // it is worth the patient pressing again. Hung and refused are the same
  // situation to them, so they must not get different advice.
  if (retryable(e)) return "Something went wrong. Please try again.";
  if (e instanceof ApiError && e.status === 409) return "You have already given consent — nothing more to do.";
  return "We could not save your consent. Please ask the front desk for help.";
}
```

`retryable(e)` is `!(e instanceof ApiError) || RETRY_STATUSES.has(e.status)`. That
one line replaces the existing `!(e instanceof ApiError)` check **and** adds the
502/503/504 case.

**Why the predicate, not the status set.** The plan for the previous branch had
`App.tsx` import `RETRY_STATUSES` and rebuild the condition. Importing `retryable`
instead means the two paths cannot drift — they are the same function, not two
expressions that must be kept in sync. If the retry policy changes, the copy
follows automatically. `App.tsx` also asks a more honest question this way: not
"is this status in a set?" but "was this worth retrying?"

This reverses a decision from the previous branch, which stripped `export` off
`RETRY_STATUSES` because its only consumer had been dropped. There is now a real
consumer — but for `retryable`, not the raw set. `RETRY_STATUSES` stays private.

**Zero new copy.** The message already exists, so this adds no string to
translate under the §5(3) notice-language work (P2) or the 22-language pack (P4).

**Branch order matters.** `retryable` must be tested before the 409 branch. A 409
is an `ApiError` not in `RETRY_STATUSES`, so `retryable(409)` is false and the 409
branch is still reached — but the reordering makes the `e instanceof ApiError`
guard on that line necessary, since `retryable` no longer proves the type.

## Out of scope

- **403 (expired session)** keeps "ask the front desk for help". That advice is
  *correct* — the patient genuinely needs a new code — just vague. Its 15-minute
  session TTL (`notification-service/pkg/otp/service/otp_service.go:28`) makes it
  rare. The previous plan's "Your code has expired" copy stays dropped.
- **500** keeps the front-desk message. A 500 is a bug, not a blip; another press
  will not fix it.
- **The code step** (`codeError`) is unchanged.

## Testing

One test in `frontend/kiosk/src/App.test.tsx`: capture returns 502 on all three
attempts; assert the patient is told to try again and is **not** sent to the front
desk.

It must fail against today's code, which renders "We could not save your consent.
Please ask the front desk for help." Verify by running it before the change.

The existing 409 test ("a capture failure does NOT blame the code — an
already-consented patient is told so") guards the branch being reordered.

## Files touched

- `frontend/kiosk/src/api/kiosk.ts` — `export` on `retryable`
- `frontend/kiosk/src/App.tsx` — one condition in `consentError`, plus the import
- `frontend/kiosk/src/App.test.tsx` — one test

No backend changes. No new dependencies. No new user-facing strings.
