package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDuplicateIdempotency is returned by Insert when a consent with the same
// (hospital_id, idempotency_key) already exists — i.e. a replayed capture. The
// caller should fetch and return the original record rather than erroring.
var ErrDuplicateIdempotency = errors.New("duplicate consent for idempotency key")

// isUniqueViolation reports whether err is a Postgres unique-constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// nullIfEmpty returns nil for an empty string so the column is stored NULL,
// keeping the partial idempotency index from treating "" as a real key.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanConsentRow scans a row selected with queries.consentColumns into a Consent.
// hms_patient_id is nullable, so it is read via NullString. Keep the field order
// aligned with the consentColumns SELECT list.
func scanConsentRow(row pgx.Row) (*model.Consent, error) {
	var c model.Consent
	var hms sql.NullString
	if err := row.Scan(
		&c.ID,
		&c.HospitalID,
		&c.PatientKey,
		&hms,
		&c.Type,
		&c.Status,
		&c.Purposes,
		&c.CreatedAt,
		&c.PreviousID,
		&c.Version,
		&c.ArtifactHash,
	); err != nil {
		return nil, err
	}
	c.HMSPatientID = hms.String
	return &c, nil
}

// getOneConsent runs a single-row consent query under the hospital's RLS context
// and returns the row, or nil if none matched.
func (r *pgxConsentRepository) getOneConsent(ctx context.Context, hospitalID, query string, args ...any) (*model.Consent, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, hospitalID); err != nil {
		return nil, err
	}

	c, err := scanConsentRow(tx.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query failed: %w", err)
	}
	return c, nil
}

type pgxConsentRepository struct {
	pool *pgxpool.Pool
}

// New returns a ConsentRepository backed by a pgx connection pool.
func New(pool *pgxpool.Pool) ConsentRepository {
	return &pgxConsentRepository{pool: pool}
}

// setHospitalContext sets the transaction-local RLS session variable that every
// consent_vault policy checks. It uses a parameterized query — NEVER string
// interpolation — and validates the ID is a UUID first, so a malformed claim
// fails fast instead of reaching SQL. This one call is the load-bearing control
// for hospital data isolation; keep it parameterized.
func setHospitalContext(ctx context.Context, tx pgx.Tx, hospitalID string) error {
	if _, err := uuid.Parse(hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: invalid hospital_id %q: %w", hospitalID, err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.hospital_id', $1, true)", hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: failed to set RLS context: %w", err)
	}
	return nil
}

func (r *pgxConsentRepository) Insert(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repository.Insert: failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, consent.HospitalID); err != nil {
		return fmt.Errorf("repository.Insert: %w", err)
	}

	err = tx.QueryRow(ctx, queryInsertConsent,
		consent.ID,
		consent.HospitalID,
		consent.PatientKey,
		nullIfEmpty(consent.HMSPatientID),
		consent.Type,
		consent.Status,
		consent.Purposes,
		consent.ArtifactHash,
		nullIfEmpty(consent.IdempotencyKey),
	).Scan(&consent.CreatedAt, &consent.Version)

	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateIdempotency
		}
		return fmt.Errorf("repository.Insert: query failed: %w", err)
	}

	// Same transaction: the audit event lands atomically with the consent.
	if _, err := tx.Exec(ctx, queryInsertOutbox, outbox.ID, outbox.Payload); err != nil {
		return fmt.Errorf("repository.Insert: outbox write failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repository.Insert: commit failed: %w", err)
	}

	return nil
}

// GetLatestByPatientKey returns the most recent consent row for a patient
// (regardless of aggregate status), or nil if none exists. It returns the latest
// row even when fully withdrawn, because Check and Withdraw both need the current
// per-purpose map — the caller inspects Purposes to decide per purpose.
func (r *pgxConsentRepository) GetLatestByPatientKey(ctx context.Context, hospitalID string, patientKey string) (*model.Consent, error) {
	c, err := r.getOneConsent(ctx, hospitalID, queryGetLatestConsent, hospitalID, patientKey)
	if err != nil {
		return nil, fmt.Errorf("repository.GetLatestByPatientKey: %w", err)
	}
	return c, nil
}

// GetLatestByHMSPatientID returns the most recent consent row for a hospital's
// opaque HMS patient ID (the doctor/HMS access path), or nil if none exists.
func (r *pgxConsentRepository) GetLatestByHMSPatientID(ctx context.Context, hospitalID, hmsPatientID string) (*model.Consent, error) {
	c, err := r.getOneConsent(ctx, hospitalID, queryGetLatestByHMSPatientID, hospitalID, hmsPatientID)
	if err != nil {
		return nil, fmt.Errorf("repository.GetLatestByHMSPatientID: %w", err)
	}
	return c, nil
}

// GetByIdempotencyKey returns the CONSENT_GIVEN row for a capture idempotency key
// (session_id), or nil if none exists. Used to make capture replays return the
// original record instead of a duplicate.
func (r *pgxConsentRepository) GetByIdempotencyKey(ctx context.Context, hospitalID, key string) (*model.Consent, error) {
	c, err := r.getOneConsent(ctx, hospitalID, queryGetByIdempotencyKey, hospitalID, key)
	if err != nil {
		return nil, fmt.Errorf("repository.GetByIdempotencyKey: %w", err)
	}
	return c, nil
}

// insertChainedRow inserts a versioned consent row (withdrawal or renewal) and
// its audit outbox row in one transaction. Both callers share identical column
// order; only the query (row type) differs.
func (r *pgxConsentRepository) insertChainedRow(ctx context.Context, query string, consent *model.Consent, outbox *model.OutboxRecord) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, consent.HospitalID); err != nil {
		return err
	}

	err = tx.QueryRow(ctx, query,
		consent.ID,
		consent.HospitalID,
		consent.PatientKey,
		nullIfEmpty(consent.HMSPatientID),
		consent.Status,
		consent.Purposes,
		consent.PreviousID,
		consent.Version,
		consent.ArtifactHash,
	).Scan(&consent.CreatedAt)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}

	// Same transaction: the audit event lands atomically with the consent row.
	if _, err := tx.Exec(ctx, queryInsertOutbox, outbox.ID, outbox.Payload); err != nil {
		return fmt.Errorf("outbox write failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}
	return nil
}

func (r *pgxConsentRepository) InsertWithdrawn(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error {
	if err := r.insertChainedRow(ctx, queryInsertWithdrawn, consent, outbox); err != nil {
		return fmt.Errorf("repository.InsertWithdrawn: %w", err)
	}
	return nil
}

func (r *pgxConsentRepository) InsertRenewal(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error {
	if err := r.insertChainedRow(ctx, queryInsertRenewal, consent, outbox); err != nil {
		return fmt.Errorf("repository.InsertRenewal: %w", err)
	}
	return nil
}

// EnqueueAudit writes a standalone outbox row (no domain row to piggyback on,
// e.g. a consent check). The outbox table has no RLS, so no hospital context is
// needed.
func (r *pgxConsentRepository) EnqueueAudit(ctx context.Context, outbox *model.OutboxRecord) error {
	if _, err := r.pool.Exec(ctx, queryInsertOutbox, outbox.ID, outbox.Payload); err != nil {
		return fmt.Errorf("repository.EnqueueAudit: %w", err)
	}
	return nil
}

// FetchUnshippedOutbox returns up to limit undelivered outbox rows, oldest first.
func (r *pgxConsentRepository) FetchUnshippedOutbox(ctx context.Context, limit int) ([]model.OutboxRecord, error) {
	rows, err := r.pool.Query(ctx, queryFetchUnshippedOutbox, limit)
	if err != nil {
		return nil, fmt.Errorf("repository.FetchUnshippedOutbox: %w", err)
	}
	defer rows.Close()

	var out []model.OutboxRecord
	for rows.Next() {
		var rec model.OutboxRecord
		if err := rows.Scan(&rec.ID, &rec.Payload); err != nil {
			return nil, fmt.Errorf("repository.FetchUnshippedOutbox: scan failed: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.FetchUnshippedOutbox: rows error: %w", err)
	}
	return out, nil
}

// MarkOutboxShipped marks a row delivered.
func (r *pgxConsentRepository) MarkOutboxShipped(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, queryMarkOutboxShipped, id); err != nil {
		return fmt.Errorf("repository.MarkOutboxShipped: %w", err)
	}
	return nil
}

// MarkOutboxFailed records a delivery attempt failure (increments attempts).
func (r *pgxConsentRepository) MarkOutboxFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	if _, err := r.pool.Exec(ctx, queryMarkOutboxFailed, id, errMsg); err != nil {
		return fmt.Errorf("repository.MarkOutboxFailed: %w", err)
	}
	return nil
}
