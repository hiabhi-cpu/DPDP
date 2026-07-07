package repository

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// parseIP converts a client IP string into a value pgx can encode to INET.
// Returns nil for empty/unparseable input so the column is stored NULL rather
// than failing the insert.
func parseIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}

type pgxAuditRepository struct {
	pool *pgxpool.Pool
}

// New returns an AuditRepository backed by a pgx connection pool.
func New(pool *pgxpool.Pool) AuditRepository {
	return &pgxAuditRepository{pool: pool}
}

// setHospitalContext sets the transaction-local RLS session variable that the
// audit_log policy checks. Parameterized (never string-interpolated) and
// UUID-validated so a malformed claim fails fast instead of reaching SQL.
func setHospitalContext(ctx context.Context, tx pgx.Tx, hospitalID string) error {
	if _, err := uuid.Parse(hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: invalid hospital_id %q: %w", hospitalID, err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.hospital_id', $1, true)", hospitalID); err != nil {
		return fmt.Errorf("setHospitalContext: failed to set RLS context: %w", err)
	}
	return nil
}

func (r *pgxAuditRepository) Insert(ctx context.Context, event *model.AuditEvent) error {
	// The table audit.events has an RLS policy checking app.hospital_id
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repository.Insert: failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Enforce Tenant Isolation using RLS
	if err := setHospitalContext(ctx, tx, event.HospitalID); err != nil {
		return fmt.Errorf("repository.Insert: %w", err)
	}

	err = tx.QueryRow(ctx, queryInsertEvent,
		event.EventID,
		event.HospitalID,
		event.EventType,
		event.ActorID,
		event.ActorType,
		event.PatientKey,
		event.ConsentID,
		event.RequestID,
		parseIP(event.IPAddress),
		event.Details,
	).Scan(&event.ID, &event.CreatedAt)

	if err != nil {
		// ON CONFLICT DO NOTHING returns no row when the event was already
		// ingested — a replayed delivery. That is idempotent success, not failure.
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		return fmt.Errorf("repository.Insert: query failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repository.Insert: commit failed: %w", err)
	}

	return nil
}

func (r *pgxAuditRepository) Find(ctx context.Context, filter model.AuditLogFilter) ([]model.AuditEvent, int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("repository.Find: failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, filter.HospitalID); err != nil {
		return nil, 0, fmt.Errorf("repository.Find: %w", err)
	}

	args := []interface{}{filter.HospitalID}
	query := queryFindEventsBase
	countQuery := queryCountEventsBase

	argCount := 2
	if filter.EventType != "" {
		clause := fmt.Sprintf(" AND event_type = $%d", argCount)
		query += clause
		countQuery += clause
		args = append(args, filter.EventType)
		argCount++
	}

	// Get total count
	var total int
	err = tx.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("repository.Find: count query failed: %w", err)
	}

	// Pagination
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argCount, argCount+1)
	offset := (filter.Page - 1) * filter.Limit
	args = append(args, filter.Limit, offset)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("repository.Find: query failed: %w", err)
	}
	defer rows.Close()

	var events []model.AuditEvent
	for rows.Next() {
		var e model.AuditEvent
		if err := rows.Scan(
			&e.ID,
			&e.HospitalID,
			&e.EventType,
			&e.ActorID,
			&e.ActorType,
			&e.PatientKey,
			&e.ConsentID,
			&e.RequestID,
			&e.IPAddress,
			&e.Details,
			&e.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("repository.Find: scan failed: %w", err)
		}
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("repository.Find: rows error: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("repository.Find: commit failed: %w", err)
	}

	return events, total, nil
}
