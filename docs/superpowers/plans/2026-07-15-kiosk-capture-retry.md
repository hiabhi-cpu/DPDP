# Kiosk Capture Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a network blip between resolve and capture recoverable without sending the patient back to the front desk.

**Architecture:** Three isolated changes in the kiosk PWA. A per-attempt timeout in `post()` (so a hung request fails instead of hanging forever), a retry loop in `capture()` (so a transient failure retries automatically, leaning on the server's existing `session_id` idempotency), and a consent-step error mapper in `App.tsx` (so the patient is told the truth about why it failed). No backend changes, no new dependencies.

**Tech Stack:** Vite + React 19 + TypeScript, vitest 4 + @testing-library/react, jsdom.

**Spec:** `docs/superpowers/specs/2026-07-15-kiosk-capture-retry-design.md`

## Global Constraints

- **No new dependencies.** `frontend/kiosk/package.json` must not gain entries.
- **No backend changes.** This plan touches `frontend/kiosk/` only.
- **All commands run from `frontend/kiosk/`.** Test command: `npm test` (`vitest run`).
- **Timeout uses `AbortSignal.timeout(5000)`** — native, no polyfill. Known constraint: Chrome 103+ (mid-2022); the fallback if a pilot device fails is `AbortController` + `setTimeout`. Do not add the fallback pre-emptively.
- **Retry budget: 3 attempts total** (initial request + 2 retries), **5s** per-attempt timeout, **1s** fixed gap.
- **Retry on:** a thrown fetch (network failure, or the timeout aborting) and 502/503/504. **Never on** any 4xx or on 500.
- **`resolveClaim()` must not retry.** It is protected by a per-hospital resolve cap; the retry lives in `capture()` so resolve cannot retry by construction. Do not add a retry flag to `post()`.
- **Patient-facing copy is exact.** Use these strings verbatim:
  - `"Your code has expired — please ask the front desk to resend."`
  - `"Network problem — please press Confirm again."`
  - `"Something went wrong. Please try again."` (existing generic)
- **`msg()` in `App.tsx` is unchanged.** The code step keeps today's behavior deliberately (see the spec's "Accepted narrowing").
- Tests live beside source (`src/api/kiosk.test.ts`, `src/App.test.tsx`). Extend them; create no new test files.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `src/api/kiosk.ts` | HTTP boundary: `post()` owns transport concerns (timeout, error shape); `capture()` owns the retry policy because it is the only idempotent call | Modify |
| `src/App.tsx` | Step orchestration + mapping errors to patient copy | Modify |
| `src/api/kiosk.test.ts` | Transport + retry behavior | Modify |
| `src/App.test.tsx` | Patient-visible error copy | Modify |

The timeout sits in `post()` (every call benefits — a hung resolve is no better than a hung capture) while the retry sits in `capture()` (only capture is safe to replay). That split is the design's load-bearing boundary: it is what makes "resolve never retries" a structural fact rather than a rule someone must remember.

---

### Task 1: Per-attempt timeout in `post()`

`fetch` has no default timeout. On a hung WiFi connection the promise never settles, so Task 2's retry would never fire. This task is what makes Task 2 meaningful.

**Files:**
- Modify: `frontend/kiosk/src/api/kiosk.ts:10-28`
- Test: `frontend/kiosk/src/api/kiosk.test.ts`

**Interfaces:**
- Consumes: nothing (first task)
- Produces: `post<T>(path: string, body: unknown): Promise<T>` — unchanged signature, now aborts after 5s. Task 2 calls it and catches its throws.

- [ ] **Step 1: Write the failing test**

Add to `frontend/kiosk/src/api/kiosk.test.ts`, inside the existing `describe("kiosk api", ...)` block:

```ts
  it("post sends an abort signal so a hung request cannot hang forever", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ session_id: "s", mobile: "9", name: "A", hms_patient_id: "P" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await resolveClaim("123456");

    // Whether the abort actually fires is the browser's job; forgetting to pass
    // the signal is ours. That is what this asserts.
    const [, init] = fetchMock.mock.calls[0];
    expect(init.signal).toBeTruthy();
  });
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend/kiosk && npm test -- kiosk.test.ts`
Expected: FAIL — `expected undefined to be truthy` (no `signal` on the fetch init yet).

- [ ] **Step 3: Write minimal implementation**

In `frontend/kiosk/src/api/kiosk.ts`, add the constant above `post()` and the `signal` line inside the `fetch` init:

```ts
// fetch has no default timeout: without this a hung connection never settles
// and the capture retry below never gets a chance to fire.
const TIMEOUT_MS = 5000;

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
```

Leave the rest of `post()` exactly as it is.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend/kiosk && npm test`
Expected: PASS — the new test plus all 6 existing tests (`kiosk.test.ts` 3, `App.test.tsx` 3).

- [ ] **Step 5: Commit**

```bash
git add frontend/kiosk/src/api/kiosk.ts frontend/kiosk/src/api/kiosk.test.ts
git commit -m "fix(kiosk): give every request a 5s abort timeout

fetch has no default timeout, so a hung WiFi connection left the kiosk
spinning forever with no way to recover. Prerequisite for the capture
retry: a hung request must fail before it can be retried."
```

---

### Task 2: Retry loop in `capture()`

**Files:**
- Modify: `frontend/kiosk/src/api/kiosk.ts:36-48` (the `capture()` export)
- Test: `frontend/kiosk/src/api/kiosk.test.ts`

**Interfaces:**
- Consumes: `post<T>(path, body)` from Task 1; `ApiError` (existing, has `.status: number`)
- Produces:
  - `capture(mobile: string, sessionId: string, purposes: string[], hmsPatientId: string): Promise<void>` — unchanged signature. On failure it throws the **last** error, which Task 3 maps to patient copy. A thrown fetch (network/timeout) surfaces as a non-`ApiError`; an HTTP failure surfaces as `ApiError`.
  - `RETRY_STATUSES: Set<number>` — exported. Task 3 imports it to map the same statuses to the "network problem" copy.

- [ ] **Step 1: Write the failing tests**

Add both to `frontend/kiosk/src/api/kiosk.test.ts`, inside the existing `describe("kiosk api", ...)` block:

```ts
  it("capture retries a 503 and succeeds on the next attempt", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 503 }))
      .mockResolvedValueOnce(new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await capture("9999999999", "sess-1", ["treatment"], "PA-1");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    // Both attempts carry the same session_id — that is what makes the replay
    // safe server-side (consent-service checks the key before the session verify).
    const sessions = fetchMock.mock.calls.map((c) => JSON.parse(c[1].body).session_id);
    expect(sessions).toEqual(["sess-1", "sess-1"]);
    vi.useRealTimers();
  });

  it("capture does not retry a 403 — an expired session will not heal", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "otp session invalid or expired — verify OTP first" }), { status: 403 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(capture("9999999999", "sess-1", ["treatment"], "PA-1")).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend/kiosk && npm test -- kiosk.test.ts`
Expected: FAIL — the 503 test fails with `expected "spy" to be called 2 times, but got 1 time` (no retry yet; `capture` throws on the 503).

- [ ] **Step 3: Write minimal implementation**

In `frontend/kiosk/src/api/kiosk.ts`, add above the `capture()` export:

```ts
const ATTEMPTS = 3;
const RETRY_GAP_MS = 1000;

// Exported: App.tsx maps these same statuses to the "network problem" copy.
// One list, one meaning — "the transport failed, not the request".
export const RETRY_STATUSES = new Set([502, 503, 504]);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// A thrown fetch (network down, or the timeout aborting) carries no status and
// is worth another go. Every 4xx is deterministic, and a 500 is a bug, not a
// blip — retrying either just delays an honest answer.
function retryable(e: unknown): boolean {
  return !(e instanceof ApiError) || RETRY_STATUSES.has(e.status);
}
```

Then replace the whole `capture()` export with:

```ts
export async function capture(
  mobile: string,
  sessionId: string,
  purposes: string[],
  hmsPatientId: string,
): Promise<void> {
  const body = { mobile, session_id: sessionId, purposes, hms_patient_id: hmsPatientId };
  // Retrying is safe because every attempt reuses session_id: consent-service
  // checks the idempotency key before the session verify, so a replay after the
  // write landed returns the original row (200) instead of double-writing.
  // ponytail: the retry lives here, not in post(), so resolveClaim — which is
  // capped per-hospital — cannot retry by construction.
  for (let attempt = 1; ; attempt++) {
    try {
      return await post("/kiosk/api/consent/capture", body);
    } catch (e) {
      if (attempt === ATTEMPTS || !retryable(e)) throw e;
      await sleep(RETRY_GAP_MS);
    }
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend/kiosk && npm test`
Expected: PASS — 9 tests (`kiosk.test.ts` 6, `App.test.tsx` 3).

- [ ] **Step 5: Commit**

```bash
git add frontend/kiosk/src/api/kiosk.ts frontend/kiosk/src/api/kiosk.test.ts
git commit -m "feat(kiosk): retry the capture POST on a transient failure

3 attempts, 1s apart, on a thrown fetch or 502/503/504. Safe because
every attempt reuses session_id and consent-service checks the
idempotency key before the session verify — a replay after the write
landed returns the original row rather than double-writing.

The loop lives in capture() rather than post() so resolveClaim, which
burns a per-hospital resolve cap, cannot retry by construction."
```

---

### Task 3: Honest consent-step errors

Today [App.tsx:36-40](frontend/kiosk/src/App.tsx#L36-L40) maps *any* `ApiError` to "Code not recognized — please ask the front desk to resend." On the consent step that is wrong for every non-401, and it is the harmful direction: a blip sends the patient to reception, who issues a fresh code for a consent that may already be in the vault.

**Files:**
- Modify: `frontend/kiosk/src/App.tsx:36-40` (add `consentError`), `:59-70` (`onConfirm` uses it)
- Test: `frontend/kiosk/src/App.test.tsx`

**Interfaces:**
- Consumes: `ApiError` (`.status: number`), `capture()`, and `RETRY_STATUSES` from Task 2
- Produces: nothing downstream — this is the last task

- [ ] **Step 1: Write the failing tests**

Add both to `frontend/kiosk/src/App.test.tsx`, inside the existing `describe("code-only kiosk", ...)` block:

```ts
  it("a network blip on confirm does not send the patient back to the front desk", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    // One resolve, then all 3 capture attempts fail: the patient's consent may
    // already be in the vault, so "ask the front desk" is the wrong advice.
    mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9876543210", name: "Asha Rao", hms_patient_id: "PA-1" }), { status: 200 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
      new Response(JSON.stringify({ error: "consent service unavailable" }), { status: 502 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/network problem/i)).toBeInTheDocument();
    expect(screen.queryByText(/ask the front desk/i)).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("an expired session on confirm does tell the patient to ask the front desk", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9876543210", name: "Asha Rao", hms_patient_id: "PA-1" }), { status: 200 }),
      new Response(JSON.stringify({ error: "otp session invalid or expired — verify OTP first" }), { status: 403 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/expired — please ask the front desk/i)).toBeInTheDocument();
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend/kiosk && npm test -- App.test.tsx`
Expected: FAIL — the blip test fails on `findByText(/network problem/i)`; the screen currently shows "Code not recognized — please ask the front desk to resend."

- [ ] **Step 3: Write minimal implementation**

In `frontend/kiosk/src/App.tsx`, leave `msg()` exactly as it is and add below it:

```tsx
// The consent step needs its own vocabulary: msg()'s "ask the front desk" is
// right on the code step but actively harmful here — a blip would send the
// patient to reception for a consent that may already be in the vault.
function consentError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) return "Your code has expired — please ask the front desk to resend.";
    if (RETRY_STATUSES.has(e.status)) return "Network problem — please press Confirm again.";
    return "Something went wrong. Please try again.";
  }
  // A thrown fetch that survived every retry: network failure or timeout.
  return "Network problem — please press Confirm again.";
}
```

Extend the existing import at the top of `App.tsx` — reuse Task 2's list rather than
restating `[502, 503, 504]` here, so the two can't drift:

```tsx
import { resolveClaim, capture, ApiError, RETRY_STATUSES } from "./api/kiosk";
```

Then in `onConfirm`, change the one catch line:

```tsx
    } catch (e) {
      setError(consentError(e));
    } finally {
```

`onCode` keeps `setError(msg(e))`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend/kiosk && npm test`
Expected: PASS — 11 tests (`kiosk.test.ts` 6, `App.test.tsx` 5). The existing "shows a generic retry message on a bad code" test must still pass — it covers the code step, which is unchanged.

- [ ] **Step 5: Typecheck and lint**

Run: `cd frontend/kiosk && npm run build && npm run lint`
Expected: both clean. `npm run build` runs `tsc -b` — it catches the `AbortSignal.timeout` lib typing if the TS target is too low.

- [ ] **Step 6: Commit**

```bash
git add frontend/kiosk/src/App.tsx frontend/kiosk/src/App.test.tsx
git commit -m "fix(kiosk): stop telling patients their code is bad on a network blip

App.tsx mapped every ApiError to 'Code not recognized — ask the front
desk to resend'. On the consent step that is wrong for every non-401,
and it is the harmful direction: a 502 sent the patient to reception,
who issued a fresh code for a consent that may already be in the vault.

The consent step gets consentError(); msg() and the code step are
unchanged (see the spec's accepted narrowing)."
```

---

## Verification

After Task 3, drive the real flow rather than trusting the suite — the retry's whole point is behavior under a network failure that tests can only simulate.

- [ ] Bring the stack up: `docker compose up -d` per `RUN_LOCAL.md`; confirm the kiosk loads through the gateway on `:8080`.
- [ ] Reception sends a code; enter it at the kiosk; confirm the happy path still captures (201, vault row, queue row goes `DONE`).
- [ ] **Blip test:** send a code, reach the consent step, then `docker compose stop consent-service`. Press Confirm. Expect ~17s of "Saving...", then "Network problem — please press Confirm again" — **not** "ask the front desk". Restart consent-service and press Confirm again; expect success.
- [ ] **Replay test:** confirm the vault holds exactly one `CONSENT_GIVEN` row for that patient afterward, not two.

---

## Notes for the implementer

**Why no test that `resolveClaim` skips the retry:** with the loop inside `capture()`, resolve cannot retry. Asserting the absence of code that does not exist tests nothing.

**Why the signal assertion is one line and not a timing test:** whether `AbortSignal.timeout` actually fires is the browser's behavior, and a fake-timer test of it mostly fights the platform. What can realistically break on our side is forgetting to pass the signal to `fetch`.

**Known gap, deliberately not fixed here:** a 409 (`ErrActiveConsentExists`) falls through to the generic message, and kiosk-bff marks the staged record `DONE` only on a 201 — so a 409 leaves the reception queue row stuck at `CODE_SENT`. That is a pre-existing product call (should 409 route to the Done screen?), tracked in the spec's "Out of scope" section. Do not fix it in this plan.
