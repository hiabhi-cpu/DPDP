# Front-desk-driven consent flow (Spec B of 2) — Design

**Date:** 2026-07-13
**Status:** Approved (design), ready for implementation plan
**Scope:** Phase-2, **step 2 of 2**. The consent-completion journey that consumes the
`integration-service` pre-staging shipped in **Spec A**
(`docs/superpowers/specs/2026-07-13-integration-service-webhook-design.md`). Reception
fires the OTP; the patient completes consent at any kiosk by entering that code.
Depends on Spec A (already merged). §9 guardian flow, kiosk offline mode, and multi-language
remain their own P2 rows.

## Goal

Make a registered patient actually give consent. In the Indian hospital context a
"done registering" patient will not start a new self-serve consent step on their own, so
the **front desk is the forcing function**: reception picks the patient they just
registered from a queue, fires a one-time code to the patient's phone, and directs them
to a kiosk. The patient enters that code and their pre-filled consent form appears — no
typing an HMS id, no typing a mobile — they review the notice, tick purposes, and finish.
The consent vault row is linked to the hospital's `hms_patient_id`, so the HMS badge/check
resolves afterward.

## Why this is Spec B (split from the webhook)

Spec A is one backend service, testable alone. This flow spans **five components**
(notification-service, integration-service, admin-bff, admin-dashboard, kiosk-bff + PWA)
and has its own load-bearing design question — the claim mechanic below. It could not be
built or tested before Spec A existed, so A shipped first.

## Key decisions (locked in brainstorming)

- **Reception drives it; patient completes by code.** Reception fires the OTP; the patient
  enters *only* that 6-digit code at any kiosk. No HMS-id entry, no per-kiosk device
  identity (rejected — that's a separate P2 row), no wrong-patient risk (reception knows
  who they just registered; the code is bound to the HMS-staged mobile).
- **The code IS the OTP** (one code, not two). It both selects the staged record and
  proves identity.
- **OTP secrecy is preserved.** The OTP is already stored *hashed* in notification-service.
  We do **not** key any lookup by the raw code (that would put plaintext OTPs in Redis).
  Resolution is a **hashed compare within the hospital's small active claim set**.
- **notification-service stays HMS-agnostic.** Its new "claim" feature attaches an opaque
  `ref` string to an OTP and scopes it to a hospital; it never learns what `ref` means
  (it's the `hms_patient_id`).
- **kiosk-bff stays stateless and reuses the existing capture flow.** Resolve returns
  `{session_id, mobile, name, purposes}` to the browser; the browser then captures with the
  same shape the walk-in flow already uses (`{mobile, session_id, purposes}`) plus
  `hms_patient_id`. No signing, no server-side handle — the mobile lives in the browser only
  for the patient's own in-progress capture (as the walk-in flow already does), and the
  kiosk resets-on-done, clearing it.
- **Reception is a new least-privilege role**, enforced server-side at the BFF, not just
  hidden in the UI.
- **Live queue status** `PENDING → CODE_SENT → DONE`, so reception can nudge stragglers.

## Architecture

```
Reception (admin-dashboard, "reception" role, session cookie)
   │ 1. "Send code" for a staged patient
   ▼
admin-bff ──► integration-svc  GET pending(hms)         → mobile, name
          ──► notification      claim-send(hospital, mobile, ref=hms_patient_id)  → OTP texted
          ──► integration-svc   set status = CODE_SENT
                                                        (patient walks to any kiosk)
Patient's phone holds the 6-digit OTP ──────────────────┐
                                                        ▼
Kiosk PWA "Enter your code" ──► kiosk-bff ──► notification claim-resolve(hospital, otp)
                                              (hashed match in hospital active set → verify → session)
                                          ──► integration GET pending(ref) → name, notice/purposes
   │  browser receives: {session_id, mobile, name, purposes}
   ▼  patient ticks purposes, submits {mobile, session_id, hms_patient_id, purposes}
kiosk-bff ──► consent-svc  capture(mobile, session_id, hms_patient_id, purposes) → vault row
          ──► integration   set status = DONE   (best-effort; reception queue flips to "Consent done")
```

## Component detail

### 1. notification-service — hospital-scoped OTP claim (new)

The one genuinely new mechanism. Two internal endpoints (service-token auth,
`InternalServiceAuth`), plus a small Redis claim index.

- **Claim index (Redis): one key, TTL = OTP expiry.**
  - `claimset:{hospital}` — a Redis set of the hospital's active `reference_id`s (what
    resolve iterates). The `mobile` and OTP hash already live in the existing OTP record
    keyed by `reference_id`; fold the opaque `ref` into that same record value (one extra
    field), so no separate metadata key is needed.
- **`POST /internal/v1/otp/claim/send` `{hospital_id, mobile, ref}`** → this is the existing
  OTP `send` core (generate + hash + SMS + the per-mobile cooldown/hourly cap) plus claim
  indexing — reuse it, don't fork a parallel send path. Add optional `hospital_id`/`ref` so
  the send path stores `ref` on the OTP record and adds `reference_id` to
  `claimset:{hospital}`, **regenerating until the code is unique within that set** (a handful
  of live codes — cheap). Returns `{reference_id}`. A resend simply fires a fresh code; the
  patient's previous claim is left to expire on its own OTP TTL (a stale extra code for the
  same patient resolves to the same `ref` — harmless), so there is no supersede bookkeeping.
- **`POST /internal/v1/otp/claim/resolve` `{hospital_id, otp}`** → **per-hospital
  resolve-attempt cap** (Redis counter; the existing per-`reference_id` 5-attempt cap does
  not cover code-only resolve, so this is required to stop code-hammering at a kiosk).
  Iterate `claimset:{hospital}`, **hash-compare** the submitted code against each active
  OTP hash; on the single match, run the normal verify path (creates the verified session,
  burns the OTP, discards its `reference_id` from `claimset`) and return
  `{session_id, mobile, ref}`. No match → generic failure (no enumeration signal).
- Codes are never stored or logged in plaintext; uniqueness-on-send guarantees one code →
  at most one record.

### 2. integration-service — status on the pending record (extends Spec A)

- Add `status` to `PendingRegistration`: `PENDING` (default on webhook) → `CODE_SENT` →
  `DONE`. All in the existing Redis JSON; no new store.
- **`POST /internal/v1/registrations/:hms_patient_id/status` `{status}`** (hospital-JWT
  auth, same as Spec A's read API) — sets the status, hospital-scoped. Called by admin-bff
  (`CODE_SENT`) and kiosk-bff (`DONE`). Setting status preserves the record's remaining TTL.
- Spec A's list endpoint already returns the record; it now includes `status`.

### 3. admin-bff — reception role + orchestration (new)

- **Role:** add `reception` to `auth.admin_users.role`. Login reuses the existing bcrypt +
  Redis-session + CSRF flow. A helper gates reception-only endpoints and, symmetrically,
  blocks reception sessions from the admin/dpo endpoints (`/api/v1/audit`,
  `/api/v1/consent/stats`, `/api/v1/emergency/*`).
- **`GET /api/v1/reception/registrations`** → calls Spec A's list with the hospital JWT →
  returns `[{hms_patient_id, name, mobile_masked, status, registered_at}]`. Polled ~5s.
- **`POST /api/v1/reception/registrations/:hms/send-code`** → integration get(hms) → mobile;
  notification claim-send(hospital, mobile, ref=hms); integration set `CODE_SENT`; return
  `{ok}`. **Resend = the same endpoint** (a 429 from the OTP cap surfaces as "please wait").

### 4. admin-dashboard — reception screen (new)

- Reception login lands on a single **Consent queue** screen (role-routed; reception cannot
  navigate to stats/audit/emergency). One table reusing the dashboard's existing
  table/badge components: name · masked mobile · status badge (Pending / Code sent /
  Consent done) · action (Send code / Resend). Polls the queue endpoint for live status.

### 5. kiosk-bff + kiosk PWA — code-entry completion (new)

- **kiosk-bff** gains a `serviceauth` service-token client (it already mints a hospital JWT
  for consent/otp; the notification `/internal` resolve needs a service token). No new
  signing secret.
- **`POST /kiosk/api/claim/resolve` `{otp}`** (one new endpoint) → notification claim-resolve
  → `{session_id, mobile, ref}`; integration get(ref) (hospital JWT) → `name` + notice
  context; return to the browser `{session_id, mobile, name, purposes}`. Resolve failure →
  generic "code not recognized — ask reception to resend."
- **Capture reuses the existing `/kiosk/api/consent/capture` proxy** — the browser submits
  `{mobile, session_id, hms_patient_id, purposes}` (walk-in shape + the hms link). The only
  addition: after a 201 with an `hms_patient_id`, kiosk-bff fires integration set `DONE`
  (best-effort). No new capture endpoint.
- **PWA:** new primary landing "Enter the code we texted you" (6-digit input) →
  greeting + the existing per-purpose notice/consent UI (unchanged) → done screen. The
  existing **mobile-typed walk-in flow stays** as a secondary path for patients not
  registered via the HMS webhook.

## Data flow (happy path)

1. (Spec A) Bahmni webhook → pending record `PENDING`.
2. Reception "Send code" → OTP texted, record `CODE_SENT`, one active claim for the patient.
3. Patient enters the code at a kiosk → hashed-match resolve → verified session + `ref`;
   browser shows name + purposes (holds `session_id` + `mobile` for its own capture).
4. Patient ticks purposes, submits → capture writes the vault row (`patient_key` +
   `hms_patient_id`) → record `DONE` → reception queue shows "Consent done".

## Error handling / security

- **Code-hammering:** per-hospital resolve-attempt cap → 429; hashed compare only; generic
  failure text (no "valid but wrong hospital" hints).
- **OTP secrecy:** never stored/logged in plaintext; resolve is a hash compare.
- **PII:** the mobile reaches the browser only for the patient's own in-progress capture
  (as the walk-in flow already does) and is cleared on kiosk reset-on-done; it never appears
  in logs or URLs; the reception queue and SMS logs mask the mobile.
- **RBAC:** reception role enforced at admin-bff, not only in the SPA.
- **Idempotency & recovery:** capture is already idempotent (`session_id`, hardening #8).
  A resend just fires a fresh code; a superseded code stays valid until its short OTP TTL
  but maps to the same patient, so it is harmless. The `DONE` signal is best-effort — if
  lost, the record still expires at 72h and reception sees a stale "code sent" at worst;
  nothing is mis-captured.
- **Availability:** notification/integration unreachable at the kiosk → resolve fails
  cleanly → patient asks reception to resend or uses the walk-in mobile flow. The webhook
  path is an accelerator, never a hard dependency.

## Testing

- **notification:** claim-send uniqueness-on-send (collision regenerates), hashed resolve →
  `{session_id, mobile, ref}`, wrong code → generic fail + resolve-attempt cap, resend
  fires a fresh code (both old and new resolve to the same `ref`).
- **integration:** `status` transitions `PENDING→CODE_SENT→DONE`; set-status hospital-scoped;
  TTL preserved.
- **admin-bff:** reception role gating (reception blocked from audit/stats/emergency;
  admin/dpo unaffected), send-code orchestration order, queue masks mobile.
- **kiosk-bff:** resolve returns the staged patient's session + name; capture (existing proxy)
  links `hms_patient_id` and, on 201, marks the record `DONE`.
- **Live e2e:** stage via the Spec A mTLS webhook → reception send-code → read the mock-SMS
  code from the notification log → kiosk resolve(code) → tick purposes → capture → vault row
  carries `hms_patient_id` → reception queue shows "Consent done".

## Build order (for the plan, not the spec)

One journey, but large. Two plan-phases: **B1** — the backend claim plumbing (notification
claim send/resolve, integration status, admin-bff endpoints), fully API-testable end to end;
then **B2** — the reception console screen and the kiosk code-entry UI on top. B1 must land
before B2 can be exercised.

## Explicitly out of scope

- §9 guardian age-gate (own P2 row — the flow will slot the age gate in later; not built
  here), kiosk offline mode, multi-language notice, WhatsApp, bypass-detection.
- Per-kiosk device identity (deliberately avoided by the code-entry design).
- Any prod-ingress change; the internal resolve stays `/internal` (service-token), off the
  public edge.
