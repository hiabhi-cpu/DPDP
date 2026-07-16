package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
	"github.com/hiabhi-cpu/shared/secrets"
)

// Sentinel errors — returned by the service, mapped to HTTP status in the
// controller via errors.Is. Never match on err.Error() strings.
var (
	ErrActiveConsentExists = errors.New("active consent already exists")
	ErrNoActiveConsent     = errors.New("no active consent found")
	// ErrNoConsentToRenew — grant requires an existing consent chain to extend;
	// a first-time consent must go through Capture.
	ErrNoConsentToRenew = errors.New("no existing consent to grant onto")
	// ErrSessionNotVerified — the session_id is not a live OTP-verified session
	// for this mobile. No consent operation may proceed without one.
	ErrSessionNotVerified = errors.New("otp session not verified")
)

// Check reasons (plan §11): distinguish never-granted from withdrawn.
const (
	reasonNoConsent        = "no_consent"
	reasonConsentWithdrawn = "consent_withdrawn"
)

// DefaultStatsWindowDays is the activity window used when the request omits or
// passes a non-positive window_days.
const DefaultStatsWindowDays = 30

// ConsentService defines the business logic contract for consent operations.
type ConsentService interface {
	// Capture returns the consent and created=true for a new record, or the
	// existing record with created=false for an idempotent replay (same session_id).
	Capture(ctx context.Context, hospitalID, ip string, req *model.CaptureConsentRequest) (consent *model.Consent, created bool, err error)
	// Check answers whether a specific purpose is currently allowed for a patient.
	Check(ctx context.Context, hospitalID, ip string, req *model.CheckConsentRequest) (*model.CheckConsentResponse, error)
	// ActiveMobiles returns the subset of mobiles with at least one active purpose,
	// in input order. Batch and purpose-agnostic — the reception queue's
	// "already consented?" question. Writes no audit event; see the impl.
	ActiveMobiles(ctx context.Context, hospitalID string, mobiles []string) ([]string, error)
	Withdraw(ctx context.Context, hospitalID, ip string, req *model.WithdrawConsentRequest) error
	// Grant extends an existing chain to (re-)grant purposes. changed=false means
	// every requested purpose was already active (no new row written).
	Grant(ctx context.Context, hospitalID, ip string, req *model.GrantConsentRequest) (consent *model.Consent, changed bool, err error)
	// Stats returns hospital-scoped aggregate consent statistics. A non-positive
	// windowDays falls back to DefaultStatsWindowDays.
	Stats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error)
}

type consentService struct {
	repo            repository.ConsentRepository
	secretsProvider secrets.Provider
	sessions        SessionVerifier
}

// NewConsentService creates a ConsentService. Audit events are written to the
// transactional outbox (via repo) and shipped asynchronously by the relay — the
// service no longer calls audit-service directly. sessions gates every write
// operation on a live OTP-verified session.
func NewConsentService(repo repository.ConsentRepository, sp secrets.Provider, sessions SessionVerifier) ConsentService {
	return &consentService{
		repo:            repo,
		secretsProvider: sp,
		sessions:        sessions,
	}
}

// patientKeyFor computes the hospital-scoped patient key, surfacing secret-fetch
// errors instead of silently deriving a wrong key.
func (s *consentService) patientKeyFor(ctx context.Context, hospitalID, mobile string) (string, error) {
	sysSalt, err := s.secretsProvider.GetSystemSalt(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get system salt: %w", err)
	}
	hospKey, err := s.secretsProvider.GetHospitalKey(ctx, hospitalID)
	if err != nil {
		return "", fmt.Errorf("failed to get hospital key: %w", err)
	}
	return sharedcrypto.ComputePatientKey(mobile, sysSalt, hospKey), nil
}

// canonicalPurposeMap renders a purpose-state map deterministically as
// "purpose=STATE" pairs, key-sorted and comma-joined. Go map iteration order is
// random, so hashing fmt.Sprintf("%v", m) would produce an unverifiable hash.
func canonicalPurposeMap(m map[string]model.PurposeState) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+string(m[k]))
	}
	return strings.Join(pairs, ",")
}

// hashTimestamp returns the consent row's creation time in the exact form used
// inside the artifact hash. The time is truncated to microseconds first because
// timestamptz stores microseconds — an untruncated value would never re-verify
// after a DB round-trip.
func hashTimestamp(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

// buildOutbox serializes an audit event into an outbox record. The record ID is
// the event's EventID, which becomes audit_log.event_id for idempotency.
func buildOutbox(event AuditEvent) (*model.OutboxRecord, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal audit event: %w", err)
	}
	return &model.OutboxRecord{ID: event.EventID, Payload: payload}, nil
}

func (s *consentService) Capture(ctx context.Context, hospitalID, ip string, req *model.CaptureConsentRequest) (*model.Consent, bool, error) {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	// Idempotent replay: a capture with a session_id we've already recorded
	// returns the original record rather than erroring or duplicating.
	if req.SessionID != "" {
		if prev, err := s.repo.GetByIdempotencyKey(ctx, hospitalID, req.SessionID); err != nil {
			return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
		} else if prev != nil {
			return prev, false, nil
		}
	}

	// The session check runs AFTER the idempotency replay (a replay returns the
	// original row even once its session has expired) but BEFORE any write: a
	// consent row must never exist without a real OTP verification behind it.
	if err := s.sessions.Verify(ctx, req.SessionID, req.Mobile, req.HMSPatientID); err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	// Re-grant of a withdrawn purpose is out of scope for now: capture is blocked
	// only while some purpose is still active. After a full withdrawal (no active
	// purposes) a fresh capture is allowed.
	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: failed to check existing: %w", err)
	}
	if existing != nil && existing.AnyActive() {
		return nil, false, ErrActiveConsentExists
	}

	// Build the per-purpose status map — every requested purpose starts ACTIVE.
	purposes := make(map[string]model.PurposeState, len(req.Purposes))
	for _, p := range req.Purposes {
		purposes[p] = model.PurposeActive
	}

	// The hash is computed over the same created_at that is stored on the row
	// (truncated to timestamptz's microsecond precision), so anyone can later
	// recompute and compare it — that recomputability IS the tamper evidence.
	consentID := uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	// hms_patient_id is hashed alongside patient_key because the PAIR is the
	// data principal. patient_key alone names a household (families share a
	// mobile), so a hash binding only it would attest that "the household
	// consented" — and would still verify after the field naming the actual
	// person was altered.
	artifactHash := sharedcrypto.ComputeArtifactHash(
		consentID.String(),
		hospitalID,
		patientKey,
		req.HMSPatientID,
		canonicalPurposeMap(purposes),
		hashTimestamp(createdAt),
	)

	consent := &model.Consent{
		ID:             consentID,
		HospitalID:     hospitalID,
		PatientKey:     patientKey,
		HMSPatientID:   req.HMSPatientID,
		Type:           model.TypeConsentGiven,
		Status:         model.StatusActive,
		Purposes:       purposes,
		CreatedAt:      createdAt,
		ArtifactHash:   artifactHash,
		IdempotencyKey: req.SessionID,
	}

	// actor_id is the hashed patient key — never the raw mobile.
	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_GRANTED",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &consentID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		// ponytail: hms_patient_id rides in details JSONB rather than its own
		// audit_log column. idx_audit_patient_key narrows to the household (a
		// handful of rows), then this field picks the individual — fine at pilot
		// scale. If per-household audit volume grows, promote it to a column
		// with its own index.
		Details: map[string]any{
			"purposes":       req.Purposes,
			"session_id":     req.SessionID,
			"hms_patient_id": req.HMSPatientID,
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	if err := s.repo.Insert(ctx, consent, outbox); err != nil {
		// Lost a race with a concurrent identical capture — return the winner.
		if errors.Is(err, repository.ErrDuplicateIdempotency) && req.SessionID != "" {
			if prev, ferr := s.repo.GetByIdempotencyKey(ctx, hospitalID, req.SessionID); ferr == nil && prev != nil {
				return prev, false, nil
			}
		}
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	return consent, true, nil
}

func (s *consentService) Check(ctx context.Context, hospitalID, ip string, req *model.CheckConsentRequest) (*model.CheckConsentResponse, error) {
	// Identity is the HMS patient ID. patientKey is read off the found row and
	// used only for the audit record — it is never a lookup input here, because
	// a mobile-derived key names a household rather than a patient.
	latest, err := s.repo.GetLatestByHMSPatientID(ctx, hospitalID, req.HMSPatientID)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}
	var patientKey string
	if latest != nil {
		patientKey = latest.PatientKey
	}

	// Resolve the state of the requested purpose from the latest row's map.
	var resp model.CheckConsentResponse
	switch {
	case latest != nil && latest.Purposes[req.Purpose] == model.PurposeActive:
		resp.Allowed = true
		resp.ConsentID = &latest.ID
	case latest != nil && latest.Purposes[req.Purpose] == model.PurposeWithdrawn:
		resp.Reason = reasonConsentWithdrawn
	default:
		resp.Reason = reasonNoConsent // no history, or purpose never granted
	}

	// DATA_ACCESSED when allowed; otherwise a missing/withdrawn access attempt.
	eventType := "DATA_ACCESSED"
	if !resp.Allowed {
		eventType = "CONSENT_MISSING_ACCESS_ATTEMPT"
	}

	details := map[string]any{
		"purpose":        req.Purpose,
		"allowed":        resp.Allowed,
		"reason":         resp.Reason,
		"hms_patient_id": req.HMSPatientID,
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  eventType,
		ActorID:    "system",
		ActorType:  "SYSTEM",
		PatientKey: patientKey,
		ConsentID:  resp.ConsentID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details:    details,
	})
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}
	if err := s.repo.EnqueueAudit(ctx, outbox); err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}

	return &resp, nil
}

// ActiveMobiles returns the subset of mobiles that currently have at least one
// active consent purpose, in input order.
//
// Unlike Check, this writes NO audit event, and that is deliberate. Check audits
// because a doctor reading patient data is a data access. This is an operational
// lookup: the reception queue polls it on a 5-second timer to decide whether to
// badge a row, revealing nothing reception cannot already see on the screen in
// front of them. Auditing a timer would add ~240 rows/minute of noise and bury
// the real access log — that is less accountability, not more.
func (s *consentService) ActiveMobiles(ctx context.Context, hospitalID string, mobiles []string) ([]string, error) {
	keys := make([]string, 0, len(mobiles))
	mobileToKey := make(map[string]string, len(mobiles))
	for _, m := range mobiles {
		k, err := s.patientKeyFor(ctx, hospitalID, m)
		if err != nil {
			return nil, fmt.Errorf("ConsentService.ActiveMobiles: %w", err)
		}
		keys = append(keys, k)
		mobileToKey[m] = k
	}

	activeKeys, err := s.repo.ActivePatientKeys(ctx, hospitalID, keys)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.ActiveMobiles: %w", err)
	}

	// Walk `mobiles`, not `activeKeys`: Go map order is random, and the response
	// should be deterministic.
	active := make([]string, 0, len(activeKeys))
	for _, m := range mobiles {
		if activeKeys[mobileToKey[m]] {
			active = append(active, m)
		}
	}
	return active, nil
}

func (s *consentService) Withdraw(ctx context.Context, hospitalID, ip string, req *model.WithdrawConsentRequest) error {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	// A withdrawal mutates legal state — it needs the same OTP proof as capture.
	if err := s.sessions.Verify(ctx, req.SessionID, req.Mobile, req.HMSPatientID); err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}
	if existing == nil || !existing.AnyActive() {
		return ErrNoActiveConsent
	}

	// Determine the target set: the requested purposes, or all currently-active
	// purposes when none are specified (withdraw-all).
	targets := req.Purposes
	if len(targets) == 0 {
		for p, st := range existing.Purposes {
			if st == model.PurposeActive {
				targets = append(targets, p)
			}
		}
	}

	// Carry the current map forward and flip the targeted, currently-active
	// purposes to WITHDRAWN. Only actually-active purposes count as "withdrawn".
	newMap := make(map[string]model.PurposeState, len(existing.Purposes))
	for p, st := range existing.Purposes {
		newMap[p] = st
	}
	var withdrawn []string
	for _, p := range targets {
		if newMap[p] == model.PurposeActive {
			newMap[p] = model.PurposeWithdrawn
			withdrawn = append(withdrawn, p)
		}
	}
	if len(withdrawn) == 0 {
		// Nothing among the requested purposes was active to withdraw.
		return ErrNoActiveConsent
	}

	newID := uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	// Same field order as Capture — the pair identifies the data principal.
	artifactHash := sharedcrypto.ComputeArtifactHash(
		newID.String(),
		hospitalID,
		patientKey,
		req.HMSPatientID,
		canonicalPurposeMap(newMap),
		hashTimestamp(createdAt),
	)

	withdrawnConsent := &model.Consent{
		ID:           newID,
		HospitalID:   hospitalID,
		PatientKey:   patientKey,
		HMSPatientID: existing.HMSPatientID, // carry forward so latest row stays resolvable by HMS ID
		Type:         model.TypeWithdrawal,
		Purposes:     newMap,
		CreatedAt:    createdAt,
		PreviousID:   &existing.ID,
		Version:      existing.Version + 1,
		ArtifactHash: artifactHash,
	}
	// Aggregate status: still ACTIVE if any purpose remains active (partial
	// withdrawal), else WITHDRAWN.
	withdrawnConsent.Status = withdrawnConsent.AggregateStatus()

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_WITHDRAWN",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &newID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"session_id":         req.SessionID,
			"previous_id":        existing.ID,
			"withdrawn_purposes": withdrawn,
			"remaining_status":   withdrawnConsent.Status,
			"hms_patient_id":     req.HMSPatientID,
		},
	})
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	if err := s.repo.InsertWithdrawn(ctx, withdrawnConsent, outbox); err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	return nil
}

// Grant extends an existing consent chain: it re-grants withdrawn purposes and/or
// adds new ones, appending a CONSENT_RENEWAL row. It is the mirror of Withdraw.
// If every requested purpose is already active it is a no-op (changed=false) and
// no row is written — which also makes repeated grants safe (option A idempotency).
func (s *consentService) Grant(ctx context.Context, hospitalID, ip string, req *model.GrantConsentRequest) (*model.Consent, bool, error) {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	// A grant creates new processing permission — same OTP proof as capture.
	if err := s.sessions.Verify(ctx, req.SessionID, req.Mobile, req.HMSPatientID); err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}
	if existing == nil {
		// No chain to extend — first-time consent must go through Capture.
		return nil, false, ErrNoConsentToRenew
	}

	// Carry the current map forward and flip the requested, not-already-active
	// purposes to ACTIVE.
	newMap := make(map[string]model.PurposeState, len(existing.Purposes)+len(req.Purposes))
	for p, st := range existing.Purposes {
		newMap[p] = st
	}
	var granted []string
	for _, p := range req.Purposes {
		if newMap[p] != model.PurposeActive {
			newMap[p] = model.PurposeActive
			granted = append(granted, p)
		}
	}
	if len(granted) == 0 {
		// Everything requested is already active — no-op, return current state.
		return existing, false, nil
	}

	newID := uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	// Same field order as Capture — the pair identifies the data principal.
	artifactHash := sharedcrypto.ComputeArtifactHash(
		newID.String(),
		hospitalID,
		patientKey,
		req.HMSPatientID,
		canonicalPurposeMap(newMap),
		hashTimestamp(createdAt),
	)

	renewed := &model.Consent{
		ID:           newID,
		HospitalID:   hospitalID,
		PatientKey:   patientKey,
		HMSPatientID: existing.HMSPatientID, // carry forward
		Type:         model.TypeRenewal,
		Status:       model.StatusActive, // any granted purpose ⇒ ACTIVE
		Purposes:     newMap,
		CreatedAt:    createdAt,
		PreviousID:   &existing.ID,
		Version:      existing.Version + 1,
		ArtifactHash: artifactHash,
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_GRANTED",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &newID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"session_id":       req.SessionID,
			"previous_id":      existing.ID,
			"granted_purposes": granted,
			"renewal":          true,
			"hms_patient_id":   req.HMSPatientID,
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	if err := s.repo.InsertRenewal(ctx, renewed, outbox); err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	return renewed, true, nil
}

func (s *consentService) Stats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error) {
	if windowDays <= 0 {
		windowDays = DefaultStatsWindowDays
	}
	stats, err := s.repo.GetStats(ctx, hospitalID, windowDays)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Stats: %w", err)
	}
	return stats, nil
}
