# §9 (children / guardian) schema groundwork — design

**Date:** 2026-07-11
**Plan row:** [plan-phase.md](../../../plan-phase.md) Phase 1 · DB · "§9 schema groundwork"
**Scope:** one additive migration. Schema only — no application code.

## Why

DPDP **§9** requires a verified **guardian** to consent on behalf of a minor (or
person with disability). The `consent_vault` today implicitly assumes every row
is an adult consenting for themselves — there is no way to record that a row is a
minor, who the guardian is, or that the OTP went to the guardian's phone.

This is done **now, pre-pilot, on an empty table** on purpose: adding these
columns after real consent rows exist means backfilling legal-evidence rows in an
**append-only** vault. The migration is cheap now, painful later. The kiosk
guardian *flow* (age-gate, guardian OTP routing, enforcement) is deliberately
deferred to **Phase 2** — this row only lays the columns so the P2 flow has
somewhere to write.

## Decisions

The one real tension: the vault is privacy-by-design — it **never stores raw
mobile**, only `HMAC_SHA256(mobile + SYSTEM_SALT + hospital_key)` as
`patient_key`. §9 forces collection of fresh guardian PII. How it lands:

1. **No raw guardian PII in the vault.** Guardian mobile → `guardian_key`, an HMAC
   using the **same scheme as `patient_key`**. Guardian **name is not stored in the
   vault** — it belongs in the notice/receipt artifact, keeping the "no raw PII in
   the queryable store" invariant and staying clear of the P3 §12 erasure work.
2. **`data_principal_type` is `ADULT`/`CHILD` only** — not the plan's original
   `ADULT / CHILD / GUARDIAN_CONSENT`, which conflated *who the principal is* with
   *how consent was obtained*. "Guardian consented" is implied by
   `guardian_key IS NOT NULL`. Plan row updated to match.
3. **`otp_mobile_owner` (`SELF`/`GUARDIAN`)** records whose phone got the OTP —
   the §9 accountability requirement — without storing another number.
4. **All columns nullable-or-defaulted.** Existing rows become `ADULT` / `SELF`
   with null guardian columns. No data migration; the append-only rule holds
   (migrations add columns, never rewrite rows).

## Schema

`ALTER TABLE consent.consent_vault ADD COLUMN` (migration `0013_section9_guardian.sql`):

| Column | Type / constraint | Default |
|---|---|---|
| `data_principal_type` | `VARCHAR NOT NULL CHECK IN ('ADULT','CHILD')` | `'ADULT'` |
| `guardian_relationship` | `VARCHAR CHECK IN ('MOTHER','FATHER','LEGAL_GUARDIAN')`, nullable | — |
| `guardian_key` | `VARCHAR(72)`, nullable (HMAC, `v1|<hex>`) | — |
| `otp_mobile_owner` | `VARCHAR NOT NULL CHECK IN ('SELF','GUARDIAN')` | `'SELF'` |

Plus a `COMMENT ON COLUMN` per column.

## Deliberately deferred

Added **with** the P2 kiosk flow, when the write path is known — not now:

- **Cross-column CHECK** (`CHILD ⇒ guardian_key NOT NULL`). The staged kiosk write
  path isn't defined; a hard constraint now could fight partial writes.
- **Index on `data_principal_type`.** Nothing reads these columns until P2 —
  indexing an unqueried column is speculative.

## Verification

`migrate.sh up` on a clean volume applies `0013`; `\d consent.consent_vault` shows
the four columns with the stated defaults/checks. Existing (pre-0013) rows read
back as `ADULT` / `SELF`. No application code exercises these columns yet, so
there is no runtime path to drive — verification is the DDL applying cleanly and
the append-only trigger still rejecting UPDATE/DELETE.
