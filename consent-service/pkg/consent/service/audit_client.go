package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/shared/serviceauth"
)

// AuditEvent is the payload shipped to audit-service. It must match
// audit-service's model.AuditEvent JSON shape. EventID is the idempotency key
// (the outbox row id) so at-least-once delivery never duplicates a row.
type AuditEvent struct {
	EventID    uuid.UUID      `json:"event_id"`
	HospitalID string         `json:"hospital_id"`
	EventType  string         `json:"event_type"`
	ActorID    string         `json:"actor_id"`
	ActorType  string         `json:"actor_type"`
	PatientKey string         `json:"patient_key"`
	ConsentID  *uuid.UUID     `json:"consent_id,omitempty"`
	RequestID  uuid.UUID      `json:"request_id"`
	IPAddress  string         `json:"ip_address"`
	Details    map[string]any `json:"details"`
}

// AuditShipper delivers a marshaled audit event to audit-service. Used by the
// relay, not on the request hot path.
type AuditShipper interface {
	Ship(ctx context.Context, payload []byte) error
}

type httpAuditShipper struct {
	baseURL string
	tokens  *serviceauth.Client
	client  *http.Client
}

// NewAuditShipper builds a shipper that authenticates to audit-service with a
// cached service token from tokens.
func NewAuditShipper(baseURL string, tokens *serviceauth.Client) AuditShipper {
	return &httpAuditShipper{
		baseURL: baseURL,
		tokens:  tokens,
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

func (s *httpAuditShipper) Ship(ctx context.Context, payload []byte) error {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("AuditShipper: get service token: %w", err)
	}

	url := fmt.Sprintf("%s/internal/audit/log", s.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("AuditShipper: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("AuditShipper: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("AuditShipper: unexpected status %d", resp.StatusCode)
	}
	return nil
}
