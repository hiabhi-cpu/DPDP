-- =============================================================================
-- 11_purpose_status.sql
-- Per-purpose consent status (product plan §11/§13).
--
-- consent_vault.purposes changes MEANING (not type — it is already JSONB):
--   OLD: a flat array of granted purposes       ["treatment","insurance"]
--   NEW: a per-purpose status map               {"treatment":"ACTIVE","insurance":"WITHDRAWN"}
--
-- The latest row for a patient is the current state. A partial withdrawal carries
-- the map forward and flips only the targeted purposes to WITHDRAWN, so treatment
-- can stay ACTIVE while insurance is WITHDRAWN. The row-level `status` is the
-- derived aggregate: ACTIVE if any purpose is active, else WITHDRAWN.
--
-- This is a documentation/marker migration — no DDL change is required because the
-- column is already JSONB. Rows written before this change (flat arrays) are not
-- compatible with the new read path; clear them before running the new code on a
-- pre-existing volume:
--     TRUNCATE consent.consent_vault;      -- pre-pilot only; never with real data
-- Fresh volumes start empty, so no action is needed there.
-- =============================================================================

COMMENT ON COLUMN consent.consent_vault.purposes IS
  'Per-purpose status map, e.g. {"treatment":"ACTIVE","insurance":"WITHDRAWN"}. '
  'Latest row = current state. Row status is the derived aggregate.';
