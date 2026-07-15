# Kiosk capture retry on flaky network — design

**Date:** 2026-07-15
**Status:** approved, not yet planned
**Phase:** P2 (`plan-phase.md` — "Kiosk capture retry on flaky network")
**Replaces:** the original P2 row *"Kiosk offline mode — WatermelonDB SQLite queue,
idempotency keys, auto-sync on reconnect"*, dropped 2026-07-15.

## Why this is not offline mode

The dropped row assumed a kiosk that could capture consent with no network and
sync later. Two things make that incoherent in the shipped design:

1. **Spec B made the kiosk code-only.** The patient types a reception-issued code;
   kiosk-bff resolves it against notification-service's Redis claimset to get a
   session and the patient's name. With no network there is no resolve, so no
   patient identity, so nothing to queue.
2. **Consent writes are gated on a live, server-verified OTP session**, and that
   check fails closed (`consent-service/pkg/consent/service/session_client.go`).
   A queued-offline capture is by definition unverified, and the Redis session TTL
   expires before reconnect — syncing it later would mean writing
   `otp_verified=true` on faith, re-opening the hole closed by the P1 row
   "consent writes gated on verified OTP session" (2026-07-07).

Idempotency (#8) makes *replays* safe. That is a different problem from offline.

If real offline capture is ever needed, the design is not a local queue: it is
notification-service issuing a short-lived **signed proof-of-verification** at
resolve time that the kiosk stores and replays, so the vault can still prove the
OTP happened. That still needs network at resolve time, so it covers roughly the
same seconds this retry does — which is why it is not being built now.

## What this actually fixes

The real gap is the network dropping in the seconds between resolve and capture.

**A manual re-press already recovers.** `consent_service.go:132` runs the
idempotency replay *before* the session verify and returns the original row (200,
not 201) even after the session TTL expires. So if the write landed but the
response was lost, the patient pressing "Confirm" again succeeds. Auto-retry saves
a press; it adds no new server-side guarantee.

**The error message is the bigger defect.** `frontend/kiosk/src/App.tsx:36-40`
maps *any* `ApiError` to "Code not recognized — please ask the front desk to
resend." On the consent step that is wrong for every non-401: a 502 from
kiosk-bff, a 403 expired session, a 409 active-consent. A transient blip sends the
patient back to reception, and reception issues a fresh code for a consent that may
already be in the vault.

Scope is therefore **both**: retry the capture POST, and stop lying about why it
failed.

## Design

### Retry

**Location:** the kiosk PWA's `post()` in `frontend/kiosk/src/api/kiosk.ts` — not
kiosk-bff. The flaky hop is browser→gateway over hospital WiFi. The
bff→consent-service hop is a wired datacenter link that already has a 10s client
timeout and returns 502 on failure; retrying there would add attempts on the leg
that rarely breaks while leaving the leg that does break uncovered.

**Opt-in:** `post()` takes an optional retry flag. Only `capture()` passes it.

**`resolveClaim()` stays single-shot.** Resolve is protected by a per-hospital
resolve cap (B1). If a resolve request landed but its response was lost, the cap
was already consumed — an auto-retry burns another and can 429 the whole hospital.
A failed resolve is cheap to recover: the code is still on the patient's phone.

**Budget:** 3 attempts total (the initial request + 2 retries), 5s per-attempt
timeout, 1s fixed gap between attempts.
Worst case ~17s of "Saving..." before an honest error; a typical blip recovers in
~1-6s. Fast failures (connection refused) retry near-instantly, so the 17s only
occurs on a genuine hang.

**Timeout mechanism:** `AbortSignal.timeout(5000)` — native, no dependency. `fetch`
has no default timeout, so without this a hung WiFi connection hangs forever and
the retry never fires; the timeout is what makes retry meaningful for the bad-WiFi
case rather than only for fast failures.

> **Known constraint.** `AbortSignal.timeout` is Chrome 103+ (mid-2022) and the
> kiosk targets ~5-year-old devices. Android Chrome updates independently of the
> OS so a 2021 handset is almost certainly current, and the existing `nomodule`
> path catches genuinely ancient browsers. If a pilot device fails on this, the
> fallback is `AbortController` + `setTimeout` (~4 lines, universal support).

**Retry on:** a thrown fetch (network failure, or the timeout aborting) and
502/503/504.

**Do not retry on:** any 4xx (403 expired session, 409 active consent, 400) —
deterministic, retrying only delays an honest answer. Nor 500: it means a bug, not
a blip, and three attempts turn a 1s error into a 12s one.

**Why it is safe:** every retried request carries the same `session_id`, and the
idempotency check precedes the session verify, so a retry after the write landed
returns the original row with a 200 rather than double-writing or 403-ing on the
now-consumed session. The retry leans entirely on #8.

### Error mapping

Replace the single shared `msg()` with two mappers, one per step — the steps have
genuinely different vocabularies.

**Code step:**

| Status | Message |
|---|---|
| 401 | "Code not recognized — please ask the front desk to resend." (today's text) |
| 429 | "Too many attempts — please wait a moment." |
| anything else | generic "Something went wrong. Please try again." |

**Consent step:**

| Status | Message |
|---|---|
| 403 | "Your code has expired — please ask the front desk to resend." |
| network failure / 502 / 503 / 504 (after all attempts) | "Network problem — please press Confirm again." |
| anything else | generic "Something went wrong. Please try again." |

Map status codes rather than surfacing the server's `error` string: those strings
are written for developers ("otp session invalid or expired — verify OTP first")
and are not sentences to show a patient.

## Out of scope — named gaps

**409 leaves the reception queue row stuck.** A 409 (`ErrActiveConsentExists`)
means the patient already has active consent — arguably *done*, not errored. But
kiosk-bff marks the staged record `DONE` only on a 201, so a 409 leaves the row at
`CODE_SENT` and reception watches a patient who will never disappear from the
queue. Fixing it means deciding whether 409 should route to the Done screen and
mark the row — a product call about a pre-existing edge case, not part of a retry
row. It falls through to the generic message for now.

## Testing

Extend the existing vitest files; no new test infrastructure.

`frontend/kiosk/src/api/kiosk.test.ts`:
- a 503 followed by a 201 succeeds after one retry (2 fetch calls)
- a 403 throws immediately (exactly 1 fetch call)
- a per-attempt timeout triggers a retry
- `resolveClaim` never retries (1 fetch call on a 503)

Backoff runs under `vi.useFakeTimers()` so the suite does not sleep.

`frontend/kiosk/src/App.test.tsx`:
- a capture 502 does **not** render "ask the front desk"
- a capture 403 **does** render "ask the front desk"

## Files touched

- `frontend/kiosk/src/api/kiosk.ts` — retry + timeout in `post()`, opt-in from `capture()`
- `frontend/kiosk/src/App.tsx` — split error mapping
- `frontend/kiosk/src/api/kiosk.test.ts` — retry tests
- `frontend/kiosk/src/App.test.tsx` — error-mapping tests

No backend changes. No new dependencies.
