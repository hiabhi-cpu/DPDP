# Kiosk Consent-Step Error Copy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the kiosk giving a patient contradictory or unhelpful advice when a consent capture fails.

**Architecture:** Two independent one-branch changes to `consentError` in `frontend/kiosk/src/App.tsx`. Task 1 routes an unreachable consent-service to the same message the hung path already renders, by exporting and reusing the retry loop's own `retryable` predicate. Task 2 gives an expired session its own message instead of the generic fallback. No backend changes, no new dependencies.

**Tech Stack:** Vite + React 19 + TypeScript, vitest 4 + @testing-library/react, jsdom.

**Spec:** `docs/superpowers/specs/2026-07-20-kiosk-transient-error-copy-design.md`

## Global Constraints

- **No new dependencies.** `frontend/kiosk/package.json` must not gain entries.
- **No backend changes.** This plan touches `frontend/kiosk/` only.
- **All commands run from `frontend/kiosk/`.** Test: `npm test` · build: `npm run build` · lint: `npm run lint`.
- **Patient-facing copy is exact.** Use these strings verbatim, including the em dash `—`:
  - `"Something went wrong. Please try again."` (already exists — do not retype it differently)
  - `"You have already given consent — nothing more to do."` (already exists, unchanged)
  - `"Your code has expired — please ask the front desk to resend."` (new, Task 2)
  - `"We could not save your consent. Please ask the front desk for help."` (already exists, stays the fallback)
- **`codeError` and the code step are unchanged.** Do not touch them.
- **`RETRY_STATUSES` stays module-private** in `kiosk.ts`. Only `retryable` gains an `export`.
- **500 keeps the generic fallback.** A 500 is a bug, not a blip; do not route it to "try again".
- **Assert on the distinguishing word, never on the absence of "front desk".** Today's generic message contains "front desk", and the new 403 copy contains "ask the front desk to resend" — the same phrase the *code-step* message uses. A test asserting absence would pass either way. Assert on `try again` (Task 1) and `expired` (Task 2).
- Tests live beside source. Extend `frontend/kiosk/src/App.test.tsx`; create no new test files.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `src/api/kiosk.ts` | HTTP boundary + retry policy. Owns the definition of "transient" | Add `export` to `retryable` (Task 1) |
| `src/App.tsx` | Step orchestration + mapping errors to patient copy | `consentError` branches (Tasks 1 and 2) |
| `src/App.test.tsx` | Patient-visible error copy | One test per task |

The point of exporting `retryable` rather than `RETRY_STATUSES` is that `App.tsx` should ask "was this worth retrying?" and get its answer from the same function the retry loop uses — so the copy and the retry policy cannot drift apart.

---

### Task 1: One message for an unreachable consent-service

Today a **hung** service (thrown fetch, aborted by the 5s timeout — not an `ApiError`) tells the patient "Something went wrong. Please try again.", while a **refused** one (`ApiError` 502 from kiosk-bff) tells them "We could not save your consent. Please ask the front desk for help." Both mean the service is unreachable. Live-measured: hung gave up after 17.0s, refused after ~2s. Front-desk staff cannot fix either, and pressing Confirm again is idempotent-safe — so both should say "try again".

**Files:**
- Modify: `frontend/kiosk/src/api/kiosk.ts:24` (add `export`)
- Modify: `frontend/kiosk/src/App.tsx:2` (import) and `:45-51` (`consentError`)
- Test: `frontend/kiosk/src/App.test.tsx`

**Interfaces:**
- Consumes: `ApiError` (has `.status: number`), `capture()` — both already exported from `./api/kiosk`
- Produces: `retryable(e: unknown): boolean` exported from `src/api/kiosk.ts`. Returns `true` for a non-`ApiError` throw (network failure or timeout abort) and for `ApiError` with status 502/503/504. Task 2 relies on it being `false` for 403.

- [ ] **Step 1: Write the failing test**

Add to `frontend/kiosk/src/App.test.tsx`, inside the existing `describe("code-only kiosk", ...)` block:

```tsx
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

    // Assert the distinguishing words. NOT the absence of "front desk" — the
    // current message contains it, so that assertion would pass either way.
    expect(await screen.findByText(/try again/i)).toBeInTheDocument();
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend/kiosk && npm test -- App.test.tsx`
Expected: FAIL — the screen renders "We could not save your consent. Please ask the front desk for help.", so `findByText(/try again/i)` times out.

- [ ] **Step 3: Export the predicate**

In `frontend/kiosk/src/api/kiosk.ts`, add `export` to the existing function (line ~24). Leave its body and `RETRY_STATUSES` untouched:

```ts
export function retryable(e: unknown): boolean {
  return !(e instanceof ApiError) || RETRY_STATUSES.has(e.status);
}
```

- [ ] **Step 4: Use it in `consentError`**

In `frontend/kiosk/src/App.tsx`, extend the import on line 2:

```tsx
import { resolveClaim, capture, ApiError, retryable } from "./api/kiosk";
```

Then replace the body of `consentError` (leave `codeError` above it completely alone):

```tsx
  function consentError(e: unknown): string {
    // Same predicate the retry loop uses: if it was worth retrying
    // automatically, it is worth the patient pressing again. Hung and refused
    // are the same situation to them, so they must not get different advice.
    if (retryable(e)) return "Something went wrong. Please try again.";
    // 409 = an active consent already exists for this patient (a re-visit).
    if (e instanceof ApiError && e.status === 409) return "You have already given consent — nothing more to do.";
    // Anything else: stay generic, never surface an internal error on a kiosk.
    return "We could not save your consent. Please ask the front desk for help.";
  }
```

Note the `e instanceof ApiError` guard is now required on the 409 line: `retryable` no longer narrows the type on the way past.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd frontend/kiosk && npm test`
Expected: PASS — 15 tests (kiosk.test.ts 9, App.test.tsx 6). The existing 409 test must still pass untouched: 409 is an `ApiError` not in `RETRY_STATUSES`, so `retryable(409)` is `false` and its branch is still reached.

- [ ] **Step 6: Commit**

```bash
git add frontend/kiosk/src/api/kiosk.ts frontend/kiosk/src/App.tsx frontend/kiosk/src/App.test.tsx
git commit -m "fix(kiosk): give one answer when consent-service is unreachable

A hung service told the patient to try again; a refused one sent them to
the front desk. Both mean unreachable, staff cannot fix either, and
pressing Confirm again is idempotent-safe.

consentError now reuses the retry loop's own retryable() predicate, so
the copy tracks the retry policy instead of restating it."
```

---

### Task 2: An expired session says so

A 403 means the OTP session outlived the patient's time on the consent screen (TTL is 15 minutes, `notification-service/pkg/otp/service/otp_service.go:28`). It currently falls to "We could not save your consent. Please ask the front desk for help." — correct advice, since only a fresh code fixes it, but it never says *why*, so the patient reports a broken kiosk and the front desk debugs the wrong thing.

**Files:**
- Modify: `frontend/kiosk/src/App.tsx` (`consentError`)
- Test: `frontend/kiosk/src/App.test.tsx`

**Interfaces:**
- Consumes: `retryable(e: unknown): boolean` and `ApiError` from `./api/kiosk` — the import line is already correct after Task 1; do not change it
- Produces: nothing downstream — this is the last task

- [ ] **Step 1: Write the failing test**

Add to `frontend/kiosk/src/App.test.tsx`, inside the existing `describe("code-only kiosk", ...)` block:

```tsx
  it("an expired session tells the patient the code expired", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9744411133", name: "Priya Shah", hms_patient_id: "PA-1" }), { status: 200 }),
      // 403 = the OTP session expired while the patient read the notice.
      // Not retried: a 4xx is deterministic, so ONE capture response suffices.
      new Response(JSON.stringify({ error: "otp session invalid or expired — verify OTP first" }), { status: 403 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    // "expired" is the only word no current message contains. Do NOT assert on
    // "ask the front desk to resend" — the code-step message says that too.
    expect(await screen.findByText(/expired/i)).toBeInTheDocument();
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd frontend/kiosk && npm test -- App.test.tsx`
Expected: FAIL — the screen renders "We could not save your consent. Please ask the front desk for help.", so `findByText(/expired/i)` times out.

- [ ] **Step 3: Add the 403 branch**

In `frontend/kiosk/src/App.tsx`, nest the status checks inside one `ApiError` guard:

```tsx
  function consentError(e: unknown): string {
    // Same predicate the retry loop uses: if it was worth retrying
    // automatically, it is worth the patient pressing again. Hung and refused
    // are the same situation to them, so they must not get different advice.
    if (retryable(e)) return "Something went wrong. Please try again.";
    if (e instanceof ApiError) {
      // 409 = an active consent already exists for this patient (a re-visit).
      if (e.status === 409) return "You have already given consent — nothing more to do.";
      // The session outlived the patient's time on this screen. Only a fresh
      // code fixes it, so name the cause instead of the generic "ask for help".
      if (e.status === 403) return "Your code has expired — please ask the front desk to resend.";
    }
    // Anything else: stay generic, never surface an internal error on a kiosk.
    return "We could not save your consent. Please ask the front desk for help.";
  }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd frontend/kiosk && npm test`
Expected: PASS — 16 tests (kiosk.test.ts 9, App.test.tsx 7).

- [ ] **Step 5: Typecheck and lint**

Run: `cd frontend/kiosk && npm run build && npm run lint`
Expected: both clean. `npm run build` runs `tsc -b`, which will catch it if the `ApiError` guard is missing and `e.status` is accessed on `unknown`.

- [ ] **Step 6: Commit**

```bash
git add frontend/kiosk/src/App.tsx frontend/kiosk/src/App.test.tsx
git commit -m "fix(kiosk): tell the patient when their code has expired

A 403 fell to the generic 'could not save your consent' — correct advice,
since only a fresh code fixes it, but it never said why, so the patient
reported a broken kiosk and the front desk debugged the wrong thing."
```

---

## Verification

The unit tests mock `fetch`, so they prove the mapping but not that a real failure produces the status the mapping expects. That seam was measured on 2026-07-20 and is what motivated this work:

- `docker stop dpdp-consent` → kiosk-bff returns **502** → Task 1's branch
- `docker pause dpdp-consent` → the request hangs, the 5s `AbortSignal.timeout` aborts it → a thrown fetch → also Task 1's branch

Re-running that is optional here, since neither task changes what the services emit. If you do want it end-to-end, `RUN_LOCAL.md` §5 covers staging a patient and reading the code from the mock SMS log, and `docker pause`/`docker unpause` is the cheapest way to produce a hang.

## Notes for the implementer

**Why `retryable` and not `RETRY_STATUSES`:** `App.tsx` should ask "was this worth retrying?", not "is this status in a set?". Importing the predicate means the copy and the retry policy are the same decision in one place. `RETRY_STATUSES` deliberately stays module-private.

**Why Task 1's test needs three 502 responses:** `capture()` retries 3 times. `mockFetchSequence` queues responses with `mockResolvedValueOnce`, so with fewer than three the mock runs dry mid-retry and the test fails for the wrong reason. Task 2's 403 needs only one, because a 4xx is not retried.

**Why the fake timers in Task 1:** the retry sleeps 1s between attempts. `vi.useFakeTimers({ shouldAdvanceTime: true })` matches the existing pattern in `kiosk.test.ts`. `App.test.tsx`'s `afterEach` already calls `vi.useRealTimers()`, so do not add a restore inside the test body.

**Do not touch `codeError`.** Its "Code not recognized — please ask the front desk to resend." is correct for the code step, and an existing test asserts it.
