# Returning-patient dead-end in the reception queue — design

**Date:** 2026-07-16
**Status:** Approved (design), ready for implementation plan
**Phase:** P2 (`plan-phase.md` — "Returning-patient dead-end in the reception queue")
**Scope:** consent-service (one new endpoint), integration-service (enrich the read API),
admin-bff (guard `SendCode`), admin-dashboard (badge + auto-hide). No schema change.

## The problem

A patient who already has an active consent re-registers at the hospital. The HMS webhook
re-stages them, the reception queue shows them `PENDING`, reception fires an SMS code, the
patient walks to the kiosk, types the code, ticks purposes — and capture fails with 409
`ErrActiveConsentExists`. The 409 is correct by design: `consent-service` blocks capture
while any purpose is still active. Nobody upstream knows, so the queue burns an SMS and
sends the patient on a pointless walk. This hits every repeat visitor, i.e. most of a real
OPD day.

Why the row reappears: the webhook's `Upsert` overwrites the whole record, and the Bahmni
adapter hardcodes `Status: "PENDING"` (`integration-service/pkg/pending/adapter/bahmni.go:50`).
Re-registration always resets a patient to actionable.

Mitigated (not solved) 2026-07-16 in commit `6d8c05b`: the kiosk no longer mislabels the
409 as "code not recognized" and now says "you have already given consent". The patient
still made the walk.

## Two findings that reshape the fix

The `plan-phase.md` row proposed "check by `hms_patient_id`". Tracing the code says
otherwise:

**1. Capture does not block on `hms_patient_id`.** It blocks on `GetLatestByPatientKey`
(`consent-service/pkg/consent/service/consent_service.go:150-156`), where `patientKey` is
an HMAC derived from the **mobile**. `hms_patient_id` is a nullable column, populated only
when the capture request carried one. So a queue check keyed on HMS ID can answer "no
consent" while capture still 409s — same person, different or absent HMS ID on the earlier
row. Keying on **mobile** mirrors capture's rule exactly, and integration-service already
holds the raw mobile.

**2. `check` is the wrong endpoint to reuse.** Beyond being per-purpose, it writes an audit
event on every call (`consent_service.go:286`). The queue polls every 5s
(`frontend/admin-dashboard/src/pages/Reception.tsx:7`). Twenty rows on the board would push
~240 `CONSENT_MISSING_ACCESS_ATTEMPT` rows per minute into the audit outbox — timer noise
that buries the real access log.

## Decisions

**Where the join lives: integration-service.** It is the only service holding the raw
mobile, so it is the only one that can key on the same value capture blocks on. admin-bff
is architecturally the tidier home (a BFF composing a view is its job), but the list it
receives has **masked** mobiles — it could only key on `hms_patient_id`, walking straight
back into finding 1. Getting mobiles there would need an N-call fan-out to `Get` per row.

**Rejected: preserve `DONE` across re-registration.** Nearly free, and wrong. The Redis TTL
is 72h (`integration-service/pkg/pending/repository/store.go:15`) but repeat OPD visits are
weeks apart, so the record is long gone. Fixes the rare case, misses the common one.

**Auth: forward the caller's hospital JWT.** `middleware.JWTAuth` carries no audience claim
and both services verify against the same auth-service public key
(`shared/middleware/jwt.go:15-22`, `consent-service/pkg/routes/routes.go:19`), so
integration-service can pass the inbound `Authorization` header straight through. No
`HOSPITAL_API_KEY`, no token provider, no auth-service dependency — one new env var. This is
not a confused deputy: the caller (admin-bff) already holds a token that can call
consent-service directly, so nothing is escalated.

**No audit write on the new endpoint.** `check` audits because a doctor reading patient data
*is* a data access. A 5-second poll asking whether a patient reception is already looking at
has consented is not: it is an operational lookup, triggered by a timer rather than a human,
revealing nothing reception cannot already see on screen. Auditing it adds noise, not
accountability.

## Components

### 1. consent-service — `POST /api/v1/consent/active`

Registered in the existing hospital-JWT group alongside `check`.

```
Request:  {"mobiles": ["9876543210", "9812345678"]}
Response: {"active": ["9876543210"]}
```

The response is the subset of the requested mobiles that have **any** active consent.
Mobiles travel in the body only, never a URL — raw mobiles never reach logs, matching
`check`/`capture`/`withdraw`. Batch capped at 200 mobiles; over that is a 400. The cap is
input validation at a trust boundary, not an optimisation.

- **Repository:** `ActivePatientKeys(ctx, hospitalID string, keys []string) (map[string]bool, error)`.
  One query, reusing `scanConsentRow` (`pgx.Rows` satisfies the `pgx.Row` interface it takes):

  ```sql
  SELECT DISTINCT ON (patient_key) <consentColumns>
  FROM consent.consent_vault
  WHERE hospital_id = $1 AND patient_key = ANY($2)
  ORDER BY patient_key, version DESC
  ```

  Served by the existing `idx_cv_patient_hospital_status (hospital_id, patient_key, status)`,
  whose schema comment is already "Primary lookup: does this patient have active consent at
  this hospital?". No new index.

  **RLS — the load-bearing detail.** `consent.consent_vault` is `FORCE ROW LEVEL SECURITY`,
  and every existing read goes through `getOneConsent`, which opens a transaction and calls
  `setHospitalContext` before querying (`repository.go:63-82`). That helper is single-row and
  **cannot be reused here**. `ActivePatientKeys` must open its own transaction, call
  `setHospitalContext(ctx, tx, hospitalID)`, then `tx.Query` — mirroring `getOneConsent`'s
  shape. Querying `r.pool` directly returns **zero rows with no error**, which looks exactly
  like "nobody has consented" and would silently restore the bug this spec fixes. Any test
  for this method must run with RLS active, or it will pass while the code is broken.

- **Service:** `ActiveMobiles(ctx, hospitalID string, mobiles []string) ([]string, error)` —
  `patientKeyFor` each mobile, look up, map results back to mobiles.

- **Predicate:** `AnyActive()` on the scanned row (`model/consent.go:55`) — deliberately the
  same call capture blocks on, **not** the `status` column. This is what guarantees the
  queue and capture cannot disagree. `status` is a derived aggregate maintained by the write
  paths; `AnyActive()` is the rule itself.

### 2. integration-service — `List` enriches

`ReadHandler.List` collects the raw mobiles it already reads from Redis, makes one call to
`/api/v1/consent/active` forwarding the inbound `Authorization` header, and sets a new
`consented bool` field on `listItem`. The masked-mobile contract of `List` is unchanged —
mobiles go out to consent-service server-side, never to the browser.

A `ConsentChecker` interface goes in `pkg/pending/controller/deps.go` next to
`PendingStore`, following the existing consumer-side pattern:

```go
type ConsentChecker interface {
    ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error)
}
```

New env var: `CONSENT_SERVICE_URL`.

**Fails open.** Lookup error, timeout, or non-200 → log and leave `consented` false, so rows
render exactly as they do today. A consent-service blip must never empty the reception
board; the worst case degrades to current behaviour, which is a wasted SMS, not a lost
patient.

### 3. admin-bff — `SendCode` guard

`SendCode` already fetches the raw mobile as step 1 (`admin-bff/pkg/handlers/reception.go:65`).
Add a step between that and the OTP call: ask `/api/v1/consent/active` with the single
mobile; if active, return 409 with an "already consented" message and do not fire the OTP.
Same endpoint, batch of one.

This is what actually stops the wasted SMS. The badge makes the button disabled, but the
queue's view is up to 5s stale — this closes the race where reception clicks just as the
patient's consent lands.

### 4. admin-dashboard — badge, then vanish

A `consented` row renders badged "Already consented — no action" with **Send code
disabled**, and disappears **15 seconds after first sighting**. Reception sees the patient
was handled, then the board self-cleans.

**The trap:** the queue re-polls every 5s and `rows` is a fresh array each time. A
`useEffect` keyed on `rows` would clear and re-arm the timer on every poll, so a 15s timer
inside a 5s poll **would never fire** and the row would never disappear. Implementation must
use a `useRef` map of `hms_patient_id → timeout`, armed once per id on first sighting and
never re-armed, with a hidden-set in state driving the filter.

**Accepted consequences** (noted, not coded around — no action is needed in either case):
- The suppressed set lives in page state, so a browser refresh shows a consented row for one
  more 15s cycle.
- A same-day re-registration on a never-refreshed tab will not re-show the row.

## Testing

- **consent-service repo:** `ActivePatientKeys` returns the latest row per key; a partially
  withdrawn patient (one purpose withdrawn, one active) still counts as active; a fully
  withdrawn patient does not; unknown mobiles are absent from the map.
- **consent-service service:** mobiles map back to the right subset; over-cap batch is 400.
- **integration-service:** `List` sets `consented` from the checker; a checker that errors
  still returns all rows with `consented` false (fail-open).
- **admin-bff:** `SendCode` returns 409 and fires no OTP when the mobile is active.
- **admin-dashboard (`Reception.test.tsx`):** consented row shows the badge with Send code
  disabled; the row still disappears at 15s across two intervening 5s polls (the regression
  test for the trap above).

## Out of scope

- **A returning patient who wants to change their consent** (withdraw a purpose, add one).
  `consent-service` has `Grant` and `Withdraw` for this, but the kiosk only has `capture`,
  so there is no path to reach them. That is a separate build; this spec's badge says "no
  action" because today there is no action available.
- **Batching/caching the lookup beyond one call per list build.** At pilot scale (a few live
  registrations at a time) a single indexed query per 5s poll is negligible.
