// Package adapter maps HMS-specific registration payloads onto our neutral
// PendingRegistration. Bahmni is the first (and currently only) adapter; a
// source-based dispatch is added when a second HMS lands (P4). The envelope
// below is the documented contract we require the hospital's Bahmni webhook to
// emit; the exact Bahmni-side wiring (module/Atom-feed) is confirmed at pilot.
package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// bahmniPayload is the JSON shape we accept on POST /webhook/patient-registered.
type bahmniPayload struct {
	PatientID   string `json:"patientId"`
	GivenName   string `json:"givenName"`
	FamilyName  string `json:"familyName"`
	PhoneNumber string `json:"phoneNumber"`
	Birthdate   string `json:"birthdate"`
}

// FromBahmni parses and validates a Bahmni registration payload. hospitalID is
// supplied by the caller (from the mTLS client cert), never read from the body.
func FromBahmni(body []byte, hospitalID string, now time.Time) (model.PendingRegistration, error) {
	var p bahmniPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.PendingRegistration{}, fmt.Errorf("adapter: invalid JSON: %w", err)
	}
	if p.PatientID == "" {
		return model.PendingRegistration{}, fmt.Errorf("adapter: patientId is required")
	}
	mobile := normalizeMobile(p.PhoneNumber)
	if len(mobile) != 10 {
		return model.PendingRegistration{}, fmt.Errorf("adapter: phoneNumber must normalize to 10 digits, got %d", len(mobile))
	}
	name := strings.TrimSpace(p.GivenName + " " + p.FamilyName)
	if name == "" {
		return model.PendingRegistration{}, fmt.Errorf("adapter: name is required")
	}
	return model.PendingRegistration{
		HospitalID:   hospitalID,
		HMSPatientID: p.PatientID,
		Name:         name,
		Mobile:       mobile,
		DOB:          strings.TrimSpace(p.Birthdate),
		RegisteredAt: now.UTC().Format(time.RFC3339),
	}, nil
}

// normalizeMobile strips non-digits and returns the last 10 digits, so
// "+91 98765 43210" and "9876543210" both become "9876543210".
func normalizeMobile(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return digits
}
