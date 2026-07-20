package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hiabhi-cpu/shared/serviceauth"
)

// SessionVerifier confirms that a session_id presented with a capture,
// withdrawal, or grant is a live OTP-verified session for the same mobile AND
// the same patient. This is the link that makes the vault's otp_verified column
// truthful: no consent row is written unless notification-service actually
// verified an OTP for that patient within the session window.
//
// The patient half matters because a family shares one mobile: without it, an
// OTP issued for one member would authorize a consent named for another, and
// the pairing would rest on the caller's good manners rather than on a check.
type SessionVerifier interface {
	// Verify returns nil when the session is valid, ErrSessionNotVerified when
	// notification-service rejects it, and a wrapped error on transport failure
	// (fail closed — an unreachable verifier must never admit a consent).
	Verify(ctx context.Context, sessionID, mobile, hmsPatientID string) error
}

type httpSessionVerifier struct {
	baseURL string
	tokens  *serviceauth.Client
	client  *http.Client
}

// NewSessionVerifier builds a verifier that calls notification-service's
// /internal/v1/otp/session/validate with a cached service token.
func NewSessionVerifier(baseURL string, tokens *serviceauth.Client) SessionVerifier {
	return &httpSessionVerifier{
		baseURL: baseURL,
		tokens:  tokens,
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

type validateSessionRequest struct {
	SessionID    string `json:"session_id"`
	Mobile       string `json:"mobile"`
	HMSPatientID string `json:"hms_patient_id"`
}

func (v *httpSessionVerifier) Verify(ctx context.Context, sessionID, mobile, hmsPatientID string) error {
	token, err := v.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("SessionVerifier: get service token: %w", err)
	}

	body, err := json.Marshal(validateSessionRequest{
		SessionID:    sessionID,
		Mobile:       mobile,
		HMSPatientID: hmsPatientID,
	})
	if err != nil {
		return fmt.Errorf("SessionVerifier: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/internal/v1/otp/session/validate", v.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("SessionVerifier: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("SessionVerifier: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound, http.StatusBadRequest:
		return ErrSessionNotVerified
	default:
		return fmt.Errorf("SessionVerifier: unexpected status %d", resp.StatusCode)
	}
}
