//go:build integration

// Package audittest covers the audit_log read path against a real Postgres.
//
// It exists because of a specific near-miss: audit-service began storing
// patient_key as NULL rather than ” for events whose data principal cannot be
// resolved (CONSENT_MISSING_ACCESS_ATTEMPT — no consent row exists, so there is
// nothing to derive the key from, and ” would pollute idx_audit_patient_key,
// which is partial on IS NOT NULL).
//
// model.AuditEvent.PatientKey is a plain string, so pgx cannot scan NULL into
// it: without a COALESCE in the SELECT, ONE such row makes Find fail for the
// WHOLE page and the DPO's audit log returns 500. That is the exact surface the
// NULL change was made to protect, so it needs a test, not an assumption.
//
// Requires the same env as the consent-service suite:
//
//	TEST_ADMIN_DATABASE_URL (superuser, for seeding)
//	TEST_DATABASE_URL       (dpdp_app runtime role)
package audittest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiabhi-cpu/audit-service/pkg/audit/model"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/repository"
)

const testHospital = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

func connections(t *testing.T) (adminURL, appURL string) {
	t.Helper()
	adminURL = os.Getenv("TEST_ADMIN_DATABASE_URL")
	appURL = os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set TEST_ADMIN_DATABASE_URL (superuser) and TEST_DATABASE_URL (dpdp_app) to run the audit read-path suite")
	}
	return adminURL, appURL
}

// seedNullPatientKeyEvent writes the shape that breaks a naive scan: a real
// audit row whose data principal is unknown.
func seedNullPatientKeyEvent(t *testing.T, adminURL string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer conn.Close(ctx)

	// event_type is CHECK-constrained to the real vocabulary, so the row must be
	// a genuine CONSENT_MISSING_ACCESS_ATTEMPT — which is the event that actually
	// has no patient_key. A run-unique marker in details identifies our row.
	marker := "PA-" + uuid.New().String()[:8]
	_, err = conn.Exec(ctx, `
		INSERT INTO audit.audit_log
			(event_id, hospital_id, event_type, actor_id, actor_type, patient_key,
			 request_id, details, created_at)
		VALUES ($1, $2, 'CONSENT_MISSING_ACCESS_ATTEMPT', 'system', 'SYSTEM', NULL, $3, $4, now())`,
		uuid.New(), testHospital, uuid.New(),
		[]byte(`{"hms_patient_id":"`+marker+`","allowed":false}`),
	)
	if err != nil {
		t.Fatalf("seed null-patient_key event: %v", err)
	}
	return marker
}

// A NULL patient_key must read back as empty, not blow up the page.
func TestFindReadsEventsWithNullPatientKey(t *testing.T) {
	adminURL, appURL := connections(t)
	marker := seedNullPatientKeyEvent(t, adminURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	events, _, err := repository.New(pool).Find(ctx, model.AuditLogFilter{
		HospitalID: testHospital,
		EventType:  "CONSENT_MISSING_ACCESS_ATTEMPT",
		Page:       1,
		Limit:      200,
	})
	if err != nil {
		t.Fatalf("Find returned an error for a row with NULL patient_key — one unidentifiable "+
			"access attempt would 500 the DPO's whole audit page: %v", err)
	}

	var found *model.AuditEvent
	for i := range events {
		if events[i].Details["hms_patient_id"] == marker {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("seeded event (marker %s) not returned among %d rows", marker, len(events))
	}
	if found.PatientKey != "" {
		t.Fatalf("PatientKey = %q, want empty string for an unknown data principal", found.PatientKey)
	}
}
