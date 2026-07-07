package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxEmergencyRepository struct {
	pool *pgxpool.Pool
}

// New returns an EmergencyRepository backed by a pgx connection pool.
func New(pool *pgxpool.Pool) EmergencyRepository {
	return &pgxEmergencyRepository{pool: pool}
}

// setHospitalContext sets the transaction-local RLS session variable every
// hospital-isolated policy checks. Parameterized (NEVER interpolated) and
// UUID-validated so a malformed claim fails fast. Load-bearing for isolation.
func setHospitalContext(ctx context.Context, tx pgx.Tx, hospitalID string) error {
	if _, err := uuid.Parse(hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: invalid hospital_id %q: %w", hospitalID, err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.hospital_id', $1, true)", hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: failed to set RLS context: %w", err)
	}
	return nil
}

// nullIfEmpty returns nil for an empty string so the column is stored NULL.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *pgxEmergencyRepository) InsertAccess(ctx context.Context, rec *model.AccessRecord, outbox *model.OutboxRecord) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repository.InsertAccess: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, rec.HospitalID); err != nil {
		return fmt.Errorf("repository.InsertAccess: %w", err)
	}

	// 1. Immutable evidence row in the consent ledger.
	var createdAt any
	if err := tx.QueryRow(ctx, queryInsertAccessVault,
		rec.AccessID,
		rec.HospitalID,
		nullIfEmpty(rec.PatientKey),
		nullIfEmpty(rec.HMSPatientID),
		rec.DoctorID,
		rec.EmergencyReason,
		nullIfEmpty(rec.ClinicalNote),
		rec.DPODeadline,
		rec.ArtifactHash,
	).Scan(&createdAt); err != nil {
		return fmt.Errorf("repository.InsertAccess: vault insert: %w", err)
	}

	// 2. Mutable DPO review-queue row.
	if _, err := tx.Exec(ctx, queryInsertReview,
		rec.AccessID,
		rec.HospitalID,
		rec.EmergencyRef,
		rec.DoctorID,
		rec.EmergencyReason,
		nullIfEmpty(rec.ClinicalNote),
		nullIfEmpty(rec.HMSPatientID),
		rec.DPODeadline,
	); err != nil {
		return fmt.Errorf("repository.InsertAccess: review insert: %w", err)
	}

	// 3. Audit event — same transaction, so access + audit land atomically.
	if _, err := tx.Exec(ctx, queryInsertOutbox, outbox.ID, outbox.Payload); err != nil {
		return fmt.Errorf("repository.InsertAccess: outbox insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repository.InsertAccess: commit: %w", err)
	}
	return nil
}

func (r *pgxEmergencyRepository) ListPending(ctx context.Context, hospitalID string, limit int) ([]model.ReviewItem, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("repository.ListPending: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, hospitalID); err != nil {
		return nil, fmt.Errorf("repository.ListPending: %w", err)
	}

	rows, err := tx.Query(ctx, queryListPending, hospitalID, limit)
	if err != nil {
		return nil, fmt.Errorf("repository.ListPending: query: %w", err)
	}
	defer rows.Close()

	var out []model.ReviewItem
	for rows.Next() {
		var it model.ReviewItem
		var note, hms, reviewedBy sql.NullString
		var reviewedAt sql.NullTime
		if err := rows.Scan(
			&it.AccessID, &it.EmergencyRef, &it.DoctorID, &it.EmergencyReason,
			&note, &hms, &it.ReviewStatus, &it.DPODeadline, &it.Overdue,
			&reviewedBy, &reviewedAt, &it.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("repository.ListPending: scan: %w", err)
		}
		it.ClinicalNote = note.String
		it.HMSPatientID = hms.String
		it.ReviewedBy = reviewedBy.String
		if reviewedAt.Valid {
			it.ReviewedAt = &reviewedAt.Time
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.ListPending: rows: %w", err)
	}
	return out, nil
}

func (r *pgxEmergencyRepository) RecordReview(ctx context.Context, hospitalID string, accessID uuid.UUID, status, reviewerID string, outbox *model.OutboxRecord) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("repository.RecordReview: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, hospitalID); err != nil {
		return false, fmt.Errorf("repository.RecordReview: %w", err)
	}

	tag, err := tx.Exec(ctx, queryRecordReview, accessID, hospitalID, status, reviewerID)
	if err != nil {
		return false, fmt.Errorf("repository.RecordReview: update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Not found under this hospital, or already reviewed. No audit written.
		return false, nil
	}

	if _, err := tx.Exec(ctx, queryInsertOutbox, outbox.ID, outbox.Payload); err != nil {
		return false, fmt.Errorf("repository.RecordReview: outbox insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("repository.RecordReview: commit: %w", err)
	}
	return true, nil
}

// FetchUnshippedOutbox returns up to limit undelivered outbox rows, oldest first.
func (r *pgxEmergencyRepository) FetchUnshippedOutbox(ctx context.Context, limit int) ([]model.OutboxRecord, error) {
	rows, err := r.pool.Query(ctx, queryFetchUnshippedOutbox, limit)
	if err != nil {
		return nil, fmt.Errorf("repository.FetchUnshippedOutbox: %w", err)
	}
	defer rows.Close()

	var out []model.OutboxRecord
	for rows.Next() {
		var rec model.OutboxRecord
		if err := rows.Scan(&rec.ID, &rec.Payload); err != nil {
			return nil, fmt.Errorf("repository.FetchUnshippedOutbox: scan: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.FetchUnshippedOutbox: rows: %w", err)
	}
	return out, nil
}

func (r *pgxEmergencyRepository) MarkOutboxShipped(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx, queryMarkOutboxShipped, id); err != nil {
		return fmt.Errorf("repository.MarkOutboxShipped: %w", err)
	}
	return nil
}

func (r *pgxEmergencyRepository) MarkOutboxFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	if _, err := r.pool.Exec(ctx, queryMarkOutboxFailed, id, errMsg); err != nil {
		return fmt.Errorf("repository.MarkOutboxFailed: %w", err)
	}
	return nil
}
