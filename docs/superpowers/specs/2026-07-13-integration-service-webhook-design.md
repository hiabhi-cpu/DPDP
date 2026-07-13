# integration-service (Spec A — webhook + pre-stage) — Design

**Date:** 2026-07-13
**Status:** Approved (design), ready for implementation plan
**Scope:** Phase-2, **step 1 of 2**. The mTLS webhook receiver, the Bahmni adapter, the
Redis pending-registration store, and a consumer-agnostic internal read API. The
**front-desk-driven consent flow that consumes this** (reception queue, code-based kiosk
completion) is **Spec B**, a separate design/plan — see `plan-phase.md` Phase 2. This
spec ships and is testable on its own; it has no dependency on Spec B.

## Goal

Let a hospital's HMS (Bahmni first) tell us "this patient just registered" over a secure
channel, so the consent flow can be **pre-filled** for them instead of the patient typing
their details cold. The webhook lands, we stash a short-lived pending record, and any
internal consumer can look it up. **No consent is written here** — a pending record is
just identity pre-fill; consent still requires the patient's OTP downstream (Spec B).

## Why this is split from the consent flow (Spec B)

What began as one plan line grew a whole front-desk workflow once we worked through *how*
a patient at a shared kiosk gets matched to the right registration. That matching — and
the reception console that drives it — is a distinct build spanning admin-bff,
admin-dashboard, kiosk-bff, and the kiosk PWA, with its own design questions (claim-code
mechanics, reception RBAC). This spec (A) is pure backend, one new service, testable in
isolation. B can't be tested without A, so A ships first. Two spec→plan→build cycles.

## Key decisions (locked in brainstorming)

- **Purpose = pre-stage a consent task**, not a vault write, not an HMS-access feed, not
  an id-link. The webhook stages identity so the flow can be pre-filled.
- **Hospital identity comes from the mTLS client certificate** (CN/SAN → `hospital_id`).
  The cert is *both* the authentication and the tenant identity — a payload field can't
  spoof a hospital. Per-hospital client-cert issuance becomes an onboarding step. This is
  why the plan mandates mTLS on this endpoint.
- **Store is Redis with a TTL, no Postgres.** Pre-staging is transient. Key
  `pending:{hospital_id}:{hms_patient_id}` → JSON, **TTL 72h**, idempotent upsert (webhook
  retries are safe). No new PII table, no purge job — if the patient never shows, the
  record just expires. Same pattern the OTP store already uses.
- **Bahmni is the first and only adapter now.** One documented Bahmni-shaped payload →
  our `PendingRegistration`. A `source`-based adapter dispatch is added when the 2nd HMS
  lands (eHospital/Practo, P4) — not built now.
- **`dob` is stored opportunistically** — if Bahmni sends a birthdate we keep it in the
  pending record, so the P2 §9 guardian age-gate (a later row) has data to read. We do
  **not** build any §9 logic here.

## PII note (deliberate, bounded)

A pending record holds a **raw mobile + name from the HMS, before consent**. The vault
never stores raw mobile (it's HMAC'd); the OTP store keeps mobile in Redis with a TTL.
This is a *third*, equally-bounded place raw PII lives at rest, justified because we must
be able to send an OTP to that mobile downstream. It is mitigated by: short TTL (72h),
Redis-only (no durable table, no backups capturing it), mobile kept out of logs (same
rule as the rest of the system), and mTLS-gated ingress. Masked (`98****3210`) whenever a
value is surfaced.

## Architecture

```
Bahmni (hospital HMS)
   │  POST /webhook/patient-registered
   │  mutual TLS — client cert per hospital
   ▼
integration-service  ── mTLS listener (own port, NOT behind the gateway) ──┐
   │  verify client cert → hospital_id from CN                             │
   │  Bahmni adapter: payload → PendingRegistration                        │
   ▼                                                                       │
 Redis   pending:{hospital_id}:{hms_patient_id} → JSON   TTL 72h           │
   ▲                                                                       │
   │  internal HTTP listener (normal port) — middleware.InternalServiceAuth│
   │    GET /internal/v1/registrations           (list pending, hospital-scoped)
   │    GET /internal/v1/registrations/:hms_id    (get one)                │
   │                                                                       │
   └──── consumers (Spec B: admin-bff reception queue, kiosk-bff) ─────────┘
```

**Two listeners, one process:**

1. **mTLS webhook listener** — its own `http.Server` with
   `tls.Config{ClientAuth: RequireAndVerifyClientCert, ClientCAs: hospitalCA}` on its own
   port. This is the *only* thing on this listener. It does **not** sit behind the dev
   gateway or (later) the prod ALB — the plan already reserves integration-service its own
   client-cert-terminating listener (`plan-phase.md:167`). A bad or absent client cert
   fails the TLS handshake — no application code runs.
2. **internal HTTP listener** — a normal gin app on a standard port, all routes under
   `/internal` (never public; blocked at the edge like every other `/internal/*`).
   **Auth = the hospital JWT**, not the service JWT: the pending records are
   hospital-scoped data, and the service JWT identifies only the *calling service*, so it
   can't scope by hospital. The consumers (admin-bff, kiosk-bff) already mint/hold the
   hospital JWT for their hospital — the same credential consent-service and audit-service
   scope by. integration-service validates it against the shared auth public key
   (`shared/middleware` JWT verify) and reads `hospital_id` from its claims. This is a
   deliberate deviation from the `InternalServiceAuth` pattern used elsewhere on
   `/internal`, because those endpoints act on a service identity while these act on a
   hospital's data.

### Why a new service (not folded into an existing one)

mTLS client-cert termination and a hospital-CA trust store are a distinct security
surface that nothing else has; the ingin-point is HMS-facing, not patient- or
admin-facing. It's the service the plan already names. Follows the standard service
template (`bootstrap/`, `cmd/server/`, `pkg/{routes,<domain>/{controller,service,repository,model}}`).

## Components

### 1. mTLS webhook receiver
- `POST /webhook/patient-registered`.
- `hospital_id` extracted from `r.TLS.PeerCertificates[0]` (CN or a SAN — decided in the
  plan; CN to start). If the cert maps to no known hospital → 403.
- Body parsed by the Bahmni adapter into `PendingRegistration`. Validation at this trust
  boundary: `hms_patient_id` and `mobile` required, `mobile` 10 digits, `name` non-empty.
  Reject malformed with 400 (no partial stores).
- On success: idempotent upsert into Redis, return **200** (or 202). Retries with the same
  `hms_patient_id` overwrite harmlessly (latest registration wins).
- Redis unavailable → **503** so Bahmni retries (the record must not be silently lost).

### 2. Bahmni adapter
- A pure mapping function: documented Bahmni registration payload → `PendingRegistration`.
  Bahmni is OpenMRS-based; we map patient identifier → `hms_patient_id`, given/family name
  → `name`, the phone attribute → `mobile`, birthdate → `dob` (optional).
- We define and document the **envelope we accept** (Bahmni's outbound webhook is
  configured on their side — Atom-feed/module specifics are confirmed at pilot). The
  adapter is where that contract lives; changing HMS = changing (or adding) an adapter,
  nothing else.
- One adapter now. `source` dispatch is a later, mechanical add.

### 3. Redis pending store
- Key `pending:{hospital_id}:{hms_patient_id}`, value = JSON `PendingRegistration`, TTL 72h.
- Ops: `Upsert` (SET with TTL), `Get(hospital_id, hms_id)`, `List(hospital_id)`.
- `List` scans `pending:{hospital_id}:*` (per-hospital keyspace). **ponytail: `SCAN` over a
  per-hospital prefix; if a single hospital ever holds thousands of concurrent pending
  records, add a per-hospital index set (`SADD`) — not needed at pilot scale (a few live
  registrations at a time).**

### 4. Internal read API (consumer-agnostic)
- `GET /internal/v1/registrations` → list pending for the caller's hospital. `hospital_id`
  comes from the hospital JWT's claims (see listener 2); results are hospital-scoped,
  mobile returned masked in the list.
- `GET /internal/v1/registrations/:hms_patient_id` → one record. Returns the raw mobile
  (a trusted internal consumer needs it to send the OTP); still never logged.
- Both are read-only. Spec B's reception queue consumes the list; Spec B's kiosk
  completion consumes the get.

### 5. Cert tooling (dev)
- A script under `integration-service/certs/` (gitignored) generating: a dev hospital CA,
  the server cert, and one "Bahmni" client cert whose CN is a test `hospital_id`. Enough
  to exercise the handshake and the CN→hospital mapping locally.
- Per-hospital client-cert issuance is an **onboarding** concern; prod swaps this dev CA
  for a real CA / ACM private CA later (not this spec).

## Data model

```go
type PendingRegistration struct {
    HospitalID   string `json:"hospital_id"`    // from the client cert, never the body
    HMSPatientID string `json:"hms_patient_id"`
    Name         string `json:"name"`
    Mobile       string `json:"mobile"`         // raw; masked when surfaced in lists
    DOB          string `json:"dob,omitempty"`  // optional; for the later §9 age-gate
    RegisteredAt string `json:"registered_at"`  // when we received the webhook
}
```

No purposes on the pending record — the kiosk presents the standard per-purpose notice
(purposes are not HMS-driven).

## Error handling

| Situation | Behaviour |
|---|---|
| Bad / missing client cert | TLS handshake fails; no app code runs |
| Cert CN maps to no known hospital | 403 |
| Malformed / incomplete payload | 400, nothing stored |
| Redis down on write | 503 → Bahmni retries |
| Duplicate webhook (same hms_id) | Idempotent overwrite, 200 |
| Consumer asks for an unknown/expired hms_id | 404 (Spec B falls back to the manual flow) |
| Bad/absent hospital JWT on `/internal/*` | 401 (shared JWT verify) |

## Testing

- **Bahmni adapter** — unit test: representative payload → expected `PendingRegistration`,
  including missing `dob` and rejecting a missing mobile.
- **mTLS** — handshake test: a client with a valid cert reaches the handler and the
  extracted `hospital_id` matches the CN; a client with no cert is rejected at TLS.
- **Redis store** — upsert/get/list + TTL set; idempotent overwrite.
- **Internal API** — list is hospital-scoped and masks mobile; get returns the record;
  unknown id → 404; `/internal/*` rejects a missing/invalid hospital JWT; hospital A's
  token never lists hospital B's records.
- **End-to-end (live, like prior phases)** — start integration-service with the dev certs,
  POST a Bahmni payload over mTLS, then read it back through the internal API → the row is
  present, correct, and hospital-scoped. No consent product needed to verify A.

## Explicitly out of scope (this spec)

- The reception console, "Send code", claim-code mechanics, kiosk "enter code" completion,
  reception RBAC — **all Spec B**.
- §9 guardian logic (we only *store* `dob`), kiosk offline queue, WhatsApp,
  bypass-detection, the 2nd HMS adapter, prod CA/ACM wiring.
- Any change to consent-service (capture already accepts `mobile` + optional
  `hms_patient_id`, so the eventual vault-linking needs no change there).
