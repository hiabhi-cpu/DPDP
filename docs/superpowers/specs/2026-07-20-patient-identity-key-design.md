# Patient identity: `hms_patient_id` + mobile, not mobile alone

**Date:** 2026-07-20
**Status:** Approved, ready for implementation planning

## The bug

`shared/crypto.ComputePatientKey(mobile, systemSalt, hospitalKey)` derives
`patient_key` from the mobile number alone. Every consent lookup then treats
that key as the patient's identity.

Families share one mobile number. An Indian family routinely registers
parents, children, and grandparents under a single phone. So every member of a
household collapses onto one `patient_key`, with three consequences:

1. **The second family member can never consent.** The mother consents; the son
   registers, walks to the kiosk, and `GetLatestByPatientKey` finds *her* active
   row, so capture returns 409 `ErrActiveConsentExists`
   (`consent_service.go:151-156`). He is permanently blocked, and the kiosk tells
   him "you have already given consent" — which is false.
2. **`check` returns the wrong person's answer.** A doctor checking by mobile
   gets whichever family member consented last
   (`consent_service.go:238-246`).
3. **`withdraw` hits the wrong person.** It takes mobile + session only
   (`model/consent.go:115-118`), so the son withdraws his mother's consent.

Under DPDP, (2) and (3) are the serious ones: the consent artifact and its audit
trail name the wrong data principal.

This also blocks the §9 guardian flow. `0013_section9_guardian.sql` already
assumes a guardian's mobile receives the OTP for a child's consent
(`otp_mobile_owner = 'GUARDIAN'`), but with the key derived from that mobile,
the child's row and the guardian's own row collide.

Discovered 2026-07-20 while brainstorming the returning-patient reception-queue
fix (`plan-phase.md`). Some of the "returning-patient noise" seen in live
testing is likely this bug wearing the same 409.

## Decision

**Let the two existing columns do two jobs.**

- `patient_key` stays `HMAC(mobile)` and means **contact channel** — "this
  mobile at this hospital".
- `hms_patient_id`, already on `consent_vault`, means **which patient**.
- Identity is the pair. Every identity query scopes by both.

### Why not put identity in the key

The alternative was `ComputePatientKey(mobile, hmsPatientID, systemSalt,
hospitalKey)` — one patient, one key, unambiguous everywhere including audit's
`actor_id`.

It was rejected because of emergency-service. `patientKeyFor` there explicitly
tolerates a missing mobile — "unknown identity, a valid emergency case"
(`emergency_service.go:50-55`) — and an unconscious patient usually has no HMS
ID either. Under that scheme an emergency row's key cannot be derived on the
same basis as that patient's consent rows, so the link between an override and
the consent history the DPO review needs is lost. It degrades badly exactly
where the stakes are highest.

The chosen approach does not change key *derivation*, so `patient_key` values
themselves are stable.

**Amended during implementation (2026-07-20, user-approved):** the whole-branch
review found that `artifact_hash` covered `patient_key` but not
`hms_patient_id` — so the row's tamper evidence attested that *the household*
consented, the very claim this spec establishes is invalid, and would still
verify after the field naming the actual person was altered. `hms_patient_id`
was added to the hashed field set at all three consent-service call sites.

This was free only because of timing: no verifier exists in the repo (the hash
is write-only today) and the vault was truncated, so no stored hash was
invalidated. Once real consent rows exist, the same change would require a
versioned-hash scheme — the `v1|` prefix machinery on `patient_key` exists for
exactly that reason and has no equivalent on `artifact_hash`.

A third option — `patient_key = HMAC(hms_patient_id)` — was rejected because
`hms_patient_id` is already stored in the clear on the vault, so hashing it adds
no privacy and leaves `patient_key` a redundant derived column.

## Design

### 1. Identity rule in consent-service

`hms_patient_id` stops being optional on consent rows:

- `CaptureConsentRequest.HMSPatientID` gains `binding:"required"`
  (`model/consent.go:97`).
- `GrantConsentRequest` and `WithdrawConsentRequest` gain a required
  `HMSPatientID`.
- `consent_vault.hms_patient_id` becomes required for consent rows, enforced by
  a **partial CHECK** so emergency rows — which legitimately have neither mobile
  nor HMS ID — stay writable:

  ```sql
  CHECK (
    type = 'EMERGENCY_OVERRIDE'
    OR hms_patient_id IS NOT NULL
  )
  ```

  Only `EMERGENCY_OVERRIDE` is exempt. `RETROSPECTIVE_CONSENT` is a consent row
  and must name a patient — it is written after an emergency, once the patient
  *is* identified, which is the whole point of converting the override into a
  consent record. Nothing writes that type today (it exists only in the schema's
  type list), so requiring the ID costs nothing now and encodes the rule for
  whoever builds the flow.

`queryGetLatestConsent` (`repository/queries.go:21`) gains
`AND hms_patient_id = $3`, and `GetLatestByPatientKey` becomes
`GetLatestByPatientAndHMS`. That single query change fixes the bug at its root:
it is the one function all three broken paths route through.

- **Capture's block** (`consent_service.go:151`) now fires only when *this*
  patient has an active consent. The son is no longer blocked by his mother's
  row.
- **Withdraw** (`consent_service.go:304`) gains `hms_patient_id` on its request
  and withdraws the right person's consent.
- **Grant** takes the same pair.

`Check` needs no query change — it already routes HMS-ID lookups through
`GetLatestByHMSPatientID` (`consent_service.go:230`). But its mobile-only branch
becomes indefensible: with a shared number it returns whichever family member
consented last. So `CheckConsentRequest` **drops `Mobile` entirely** and requires
`hms_patient_id`. The doctor path already sends the HMS ID; the kiosk has it
from `claim.Ref`.

Mobile is not kept as an optional cross-check. It would select nothing —
`hms_patient_id` is already unique per hospital — and identity on the write
paths is pinned by the session↔ref binding in section 2, so a mobile comparison
guards nothing that is not already guarded. Dropping the field also deletes the
existing "exactly one of mobile or hms_patient_id" branch at
`controller/consent_handler.go:74-83`, making this a net removal.

**No index change.** The existing `idx_cv_patient_hospital_status`
(`0003_consent_vault.sql:66`) already narrows to a single household — under ten
rows — and Postgres filters `hms_patient_id` from there. Widening it to four
columns is a migration for no measurable gain. The comment at
`0003_consent_vault.sql:7` claiming `patient_key` identifies the patient is
still corrected to say what it actually is.

### 2. Binding the OTP session to the patient

Under the old scheme mobile *was* identity, so verifying a session against the
mobile was enough. It no longer is: `Verify(ctx, sessionID, mobile)`
(`session_client.go:23`) proves someone at that number passed an OTP, but the
caller now supplies `hms_patient_id` separately and nothing checks the two
belong together.

In a family that is a real hole — an OTP legitimately issued for the son
validates a capture naming the mother. The kiosk pairs them correctly today
because it takes both from the same claim (`kiosk-bff/pkg/handlers/claim.go:113`),
but that is the client being well-behaved, not the server enforcing anything.

The data already exists one layer down: the claim store keeps `ref` — the HMS
patient ID — beside the mobile (`notification-service/.../repository/interface.go:32`),
and resolve has it in hand when it mints the session
(`notification-service/.../service/otp_service.go:249`). It is simply dropped.

- `SessionState` (`notification-service/.../model/otp.go:24`) gains a `Ref` field,
  set at session creation.
- `ValidateSessionRequest` gains `hms_patient_id`; validate rejects a mismatch
  the same way it rejects a wrong mobile.
- `SessionVerifier.Verify` takes the extra argument and stays fail-closed.

**Consequence:** the generic `/otp/verify` path
(`notification-service/.../service/otp_service.go:125-132`) mints sessions with
no ref, so those can no longer authorize a capture. Nothing outside
notification-service calls `/otp/send` or `/otp/verify` today — it is an unused
path built ahead of a patient portal — so this costs nothing now, and it encodes
the right rule for when the portal arrives: a session that cannot name a patient
cannot consent for one.

### 3. emergency-service

**No change.** Its queries never read consent rows by `patient_key`; its reads
are the DPO review queue by `access_id` and `review_status`. It writes
`patient_key` and `hms_patient_id` as evidence and both stay nullable.

### 4. Audit

`hms_patient_id` goes into the `details` JSONB, not a new column — `Check`
already does this (`consent_service.go:267-269`).

`actor_id` and `patient_key` on `audit_log` stay as they are, which means
`idx_audit_patient_key` (`0004_audit_log.sql:63-65`) — the "show me everything
for this patient" index — is household-scoped. The per-patient trail stays
recoverable: the index narrows to the household, then filter on the JSON field.
A household is a handful of events, so this is fine at pilot scale.

Carries a `ponytail:` comment naming the ceiling, with "add an `hms_patient_id`
column and index" as the upgrade path if audit volume per household grows.

### 5. Data disposal

Confirmed with the user: no existing consent data needs to be kept.

This change does **not** alter key derivation, so no artifact hash is
invalidated and nothing needs rehashing. The only reason to wipe is that
existing rows have NULL `hms_patient_id` and would fail the new constraint.

`TRUNCATE consent.consent_vault` — that is the whole migration. `audit_log` and
the outboxes gain no constraint and need no wipe, and OTP sessions expire in
minutes on their own.

The append-only triggers do not block this — they are
`FOR EACH ROW BEFORE UPDATE/DELETE`, and TRUNCATE does not fire them.

## Testing

The first test is the whole point — it fails today with a 409:

- **consent-service:** mother captures on `9876543210` / `PA-mother`; son
  captures on the **same mobile** with `PA-son` — succeeds.
- **consent-service:** withdraw targets only the named patient and leaves the
  sibling's consent active.
- **notification-service:** validate rejects a session whose stored `ref` does
  not match the supplied `hms_patient_id`; a ref-less session is rejected
  outright.
- **kiosk:** the existing live-stack fixture from `949410b` passes unchanged —
  it already sends `hms_patient_id` — evidence the client contract did not
  quietly break.

## The reception queue: already built, and blocked on this

The **returning-patient reception-queue notice** was brainstormed alongside this
spec and then discovered to already exist, implemented and final-review-clean,
on the unmerged branch `feat/returning-patient-queue` (its own plan and spec,
dated 2026-07-16, live on that branch). The brainstorm did not check `git
branch` first. The two designs agree on nearly everything; the branch is the
implementation of record.

**That branch must not merge before this fix, and must be re-keyed after it.**
It answers "has this patient already consented?" with
`ActiveMobiles(ctx, hospitalID, mobiles []string)`, keyed by
`patientKeyFor(mobile)` — a household lookup, the exact defect this spec
corrects.

Merged as it stands, a son whose mother consented is badged "Already consented
— no action" **and his Send code button is disabled**: reception never sends him
a code, nothing errors, and he is silently denied consent capture. That is worse
than the wasted-walk dead-end the branch exists to fix — and this change is what
makes it reachable, because capture would finally accept him at the moment the
queue stops offering him a code.

The re-keying (`ActiveMobiles` → `ActiveHMSPatientIDs`) is Task 8 of
`docs/superpowers/plans/2026-07-20-patient-identity-key.md`, scoped to run on
that branch after this one merges.
