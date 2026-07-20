# Kiosk consent-step error copy: unreachable service + expired code — design

**Date:** 2026-07-20
**Status:** approved, not yet planned
**Follows:** `2026-07-15-kiosk-capture-retry-design.md` (merged 2026-07-20, `11c38a1`)

Two changes to `consentError`, both about telling the patient something they can
act on: **(1)** an unreachable consent-service gives one message instead of two
contradictory ones, and **(2)** an expired session says so instead of falling to
the generic message.

## Defect 1 — contradictory advice for an unreachable service

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

## Defect 2 — an expired session does not say so

A 403 (the OTP session outlived the patient's time on the consent screen) falls
to "We could not save your consent. Please ask the front desk for help." That
advice is *correct* — only a fresh code fixes it — but it does not say why, so
the patient reports "the kiosk is broken" rather than "my code expired", and the
front desk debugs the wrong thing.

Rare but real: the session TTL is 15 minutes
(`notification-service/pkg/otp/service/otp_service.go:28`), so it needs a patient
parked mid-flow — distracted, interrupted, or a kiosk left open.

This copy was specified in the previous branch's plan and dropped when that
branch deferred to the already-merged mapping. Added here rather than deferred a
third time.

## Design

Export the existing `retryable` predicate from `frontend/kiosk/src/api/kiosk.ts`
and use it in `consentError`:

```ts
function consentError(e: unknown): string {
  // Same predicate the retry loop uses: if it was worth retrying automatically,
  // it is worth the patient pressing again. Hung and refused are the same
  // situation to them, so they must not get different advice.
  if (retryable(e)) return "Something went wrong. Please try again.";
  if (e instanceof ApiError) {
    if (e.status === 409) return "You have already given consent — nothing more to do.";
    // The session outlived the patient's time on this screen. Only a fresh code
    // fixes it, so name the cause instead of the generic "ask for help".
    if (e.status === 403) return "Your code has expired — please ask the front desk to resend.";
  }
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

**Copy cost: one new string.** The unreachable-service fix adds none — it reuses
the message the hung path already renders. The 403 case adds exactly one string,
"Your code has expired — please ask the front desk to resend.", which must be
translated under the §5(3) notice-language work (P2) and the 22-language pack
(P4). Accepted deliberately (2026-07-20): the generic fallback is correct advice
for a 403 but does not tell the patient *why*, and "expired" is the difference
between a patient who asks for a resend and one who thinks the kiosk is broken.

**Branch order matters.** `retryable` must be tested first. Neither 409 nor 403 is
in `RETRY_STATUSES`, so `retryable` is false for both and their branches are still
reached — but because `retryable` no longer proves the type, the status checks now
need an `e instanceof ApiError` guard, which is why they are nested rather than
flat.

Resulting map, every branch reachable:

| Thrown | Message |
|---|---|
| thrown fetch (hung / timeout abort), 502, 503, 504 | "Something went wrong. Please try again." |
| 409 | "You have already given consent — nothing more to do." |
| 403 | "Your code has expired — please ask the front desk to resend." |
| 400, 500, anything else | "We could not save your consent. Please ask the front desk for help." |

## Out of scope

- **500** keeps the generic front-desk message. A 500 is a bug, not a blip;
  another press will not fix it, and the patient should not be told to wait for
  something that is not coming back on its own.
- **The code step** (`codeError`) is unchanged.
- **`RETRY_STATUSES`** stays module-private. Only `retryable` gains an `export`.

## Testing

Two tests in `frontend/kiosk/src/App.test.tsx`:

1. **502 on all three attempts** → the patient is told to try again and is **not**
   sent to the front desk.
2. **403** → the patient is told the code expired and to ask for a resend.

Both must fail against today's code, which renders "We could not save your
consent. Please ask the front desk for help." for each. Verify by running them
before the change — a test that passes either way proves nothing here, since the
current string also contains "front desk".

For that reason test 1 must assert on the *specific* new text ("try again"), not
merely the absence of "front desk"; and test 2 must assert on "expired", which no
current message contains.

The existing 409 test ("a capture failure does NOT blame the code — an
already-consented patient is told so") guards the branch being reordered and must
still pass untouched.

## Files touched

- `frontend/kiosk/src/api/kiosk.ts` — `export` on `retryable`
- `frontend/kiosk/src/App.tsx` — `consentError`: the `retryable` branch replacing
  the `!(e instanceof ApiError)` check, the new 403 branch, and the import
- `frontend/kiosk/src/App.test.tsx` — two tests (502, 403)

No backend changes. No new dependencies. One new user-facing string (the 403
copy) — see "Copy cost" above.
