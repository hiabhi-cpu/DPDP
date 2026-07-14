# Front-desk consent flow — B2 (reception + kiosk UIs) — Design

**Date:** 2026-07-13
**Status:** Approved (design), ready for implementation plan
**Scope:** Phase-2, the UI half of the front-desk-driven consent flow. Builds on **B1**
(backend claim plumbing, merged) and the parent design
(`docs/superpowers/specs/2026-07-13-frontdesk-consent-flow-design.md`). Two UIs + a small
kiosk-bff wiring: the reception **consent queue** (admin-dashboard) and the **code-entry**
kiosk flow. Backend is done; this is presentation + one BFF endpoint.

## Goal

Give the two humans in the flow their screens. **Reception** sees an "awaiting consent"
queue and fires a code with one tap. The **patient** walks to any kiosk, enters that code,
and their pre-filled consent form appears — greet, review notice, tick purposes, done. No
HMS id, no mobile typed at the kiosk.

## Key decisions (locked in brainstorming)

- **Code-only kiosk.** The kiosk is purely reception-driven: its single entry is "enter the
  code we texted you." The existing walk-in (type-mobile → OTP) steps are **removed** — every
  patient is reception-driven (the front desk is the forcing function). A patient with no
  code goes back to reception, not the kiosk.
- **DONE rows leave the queue.** A completed patient **disappears** from the reception queue
  (completion by disappearance). The queue shows only `PENDING` + `CODE_SENT`.
- **kiosk-bff reuses the hospital JWT.** B1 made the claim endpoints hospital-JWT-gated, so
  kiosk-bff's resolve proxy uses the hospital JWT it already mints — **no service-token
  client** (a simplification vs the parent spec's assumption).
- **Purposes stay static.** The kiosk presents its existing `notice.ts` per-purpose notice;
  resolve returns identity only (`session_id`, `mobile`, `name`, `hms_patient_id`), not purposes.
- **Role-scoped UI.** `reception` lands on `/reception` and sees only "Consent queue";
  admin/dpo never see it; the UI mirrors the BFF's already-enforced RBAC.

## Architecture

```
Reception (admin-dashboard, role=reception)
  /reception  ──poll ~5s──► GET  /api/reception/registrations        (admin-bff, B1)
              ──"Send code"─► POST /api/reception/registrations/:hms/send-code (admin-bff, B1)

Patient's phone has the 6-digit code
  Kiosk PWA (code-only)
   landing "enter code" ──► POST /kiosk/api/claim/resolve {otp}       (kiosk-bff, NEW)
        kiosk-bff ──► notification /internal/v1/otp/claim/resolve (hospital JWT) → {session_id, mobile, ref}
                  ──► integration GET /internal/v1/registrations/:ref (hospital JWT) → name
        → browser gets {session_id, mobile, name, hms_patient_id}
   consent (greet by name, tick purposes) ──► POST /kiosk/api/consent/capture
        body {mobile, session_id, hms_patient_id, purposes}          (existing proxy, EXTENDED)
        kiosk-bff → consent-svc capture → 201 → integration set-status DONE (best-effort)
   done → reset-on-done → landing
```

## Components

### 1. kiosk-bff (Go) — one new endpoint + one extension

- **`POST /kiosk/api/claim/resolve` `{otp}`** (NEW). Calls notification
  `/internal/v1/otp/claim/resolve` with the hospital JWT → `{session_id, mobile, ref}`; on
  success calls integration `GET /internal/v1/registrations/{ref}` → the record's `name`;
  returns `{session_id, mobile, name, hms_patient_id}` to the browser (hms_patient_id = ref, the opaque non-PII HMS id the capture call needs). On resolve failure (bad/expired code,
  429) returns a generic error the PWA shows as "ask the front desk to resend." (Name lookup
  failure is non-fatal — return the session with an empty `name`.)
- **`POST /kiosk/api/consent/capture` (EXTENDED).** Already proxies capture; now it also
  forwards `hms_patient_id` in the body, and **after a 201 with an `hms_patient_id`** fires
  integration `POST /internal/v1/registrations/{hms}/status {status:"DONE"}` best-effort
  (log-on-failure, never blocks the patient's success response).
- kiosk-bff gains an integration base URL in its env; it already has the notification base
  and the hospital JWT token client.

### 2. Kiosk PWA (React/TS) — code-only wizard

- Wizard becomes `code → consent → done` (state machine in `App.tsx`); **remove** the
  `mobile` and `otp` steps and the `sendOtp`/`verifyOtp` API calls.
- **Landing / code step** (replaces Welcome+Mobile): heading "Enter the code we texted you",
  a 6-digit numeric input (large, ≥44px targets, the existing fluid layout), Submit. On
  submit → `resolve(otp)`; on error show the generic message and let them retry.
- **Consent step** (existing `Consent.tsx`, minor change): greet "Welcome, {name}" (omit the
  greeting if name empty), same per-purpose notice from `notice.ts`; on confirm →
  `capture(mobile, session_id, purposes, hms_patient_id=ref)`.
- **Done step** (existing): unchanged; reset-on-done returns to the code landing.
- `api/kiosk.ts`: add `resolveClaim(otp) → {session_id, mobile, name, hms_patient_id}`; extend `capture` to
  send `hms_patient_id`; drop `sendOtp`/`verifyOtp`.
- App state carries `sessionId`, `mobile`, `name`, `hmsPatientId` (= `ref`).

### 3. Reception queue page (admin-dashboard, React/TS)

- New **`/reception`** route + `ReceptionQueue` page reusing the existing `DataTable`.
- Columns: **name · mobile (masked, as returned) · status badge · action**. Action button
  reads **"Send code"** when `PENDING`, **"Resend"** when `CODE_SENT`; click → POST
  `send-code`; a 429 shows "please wait" inline.
- **Filter out `DONE`** rows client-side (queue shows only PENDING + CODE_SENT), newest
  first. Poll `GET /api/reception/registrations` every ~5s for live status. Empty state:
  "No patients awaiting consent."
- `api/client.ts` + `api/types.ts`: add `receptionRegistrations()` and `sendCode(hms)` and
  the row type `{hms_patient_id, name, mobile, status, registered_at}`.

### 4. Role-scoped routing (admin-dashboard)

- Add `/reception` under the protected `AppShell`.
- `AppShell` nav renders links by `user.role`: reception → only "Consent queue"; admin/dpo →
  Dashboard/Audit/Emergency (unchanged), no "Consent queue".
- A small role guard: reception hitting `/`, `/audit`, `/emergency` is redirected to
  `/reception`; admin/dpo hitting `/reception` is redirected to `/`. After login, redirect by
  role to the right landing. Driven by the `role` already on `AuthContext`'s `user`. The BFF
  is the real gate (403s); this keeps the UI coherent.

## Error handling

- Bad/expired/oversubmitted code at the kiosk → generic "Code not recognized — please ask
  the front desk to resend" (no enumeration hint; mirrors the BFF's generic 401/429).
- kiosk-bff resolve: notification unreachable → generic failure; integration name lookup
  fails → proceed with empty name (identity still verified by the session).
- Best-effort DONE: a failed status write leaves the row in the queue at CODE_SENT and is
  logged; the patient still sees success and the vault row is written. Reception can leave it
  (ages out) — no mis-capture.
- Reception send-code 429 (OTP cooldown/cap) → inline "please wait before resending."

## Testing

- **kiosk-bff:** resolve proxy returns `{session_id, mobile, name, hms_patient_id}` (name fetched from
  integration; empty-name fallback when the lookup fails); resolve failure → generic error;
  capture-with-`hms_patient_id` fires the DONE status call after a 201 and still returns
  success if that call fails.
- **Kiosk PWA:** code → consent → capture happy path (capture body carries `hms_patient_id`);
  bad code shows the generic error; greeting uses `name`; reset-on-done returns to the code
  landing. (Vitest, like the existing kiosk tests.)
- **Reception page:** renders the queue, masks mobile, hides DONE rows, Send→Resend label
  transition, 429 inline message; role guard redirects. (Vitest, like the existing
  dashboard tests.)
- **Live e2e:** reception UI "Send code" → read the mock-SMS code → kiosk UI code-entry →
  consent → done → the row disappears from the reception queue; vault row carries
  `hms_patient_id`; check-by-HMS returns allowed.

## Explicitly out of scope

- §9 guardian age-gate (own P2 row), kiosk offline mode, multi-language notice, the resolve
  brute-force-cap refinement (B1 pilot-scale note), any prod-ingress change.
- Removing the now-unused kiosk-bff OTP `send`/`verify` proxy routes is optional cleanup, not
  required by B2 (they're harmless dead proxies once the PWA stops calling them); leave them
  unless a reviewer wants them gone.
