package model

import "time"

// ConsentArtifact mirrors the consent.consent_vault table row.
// Fields map directly to DB columns for clean scan operations.
type ConsentArtifact struct {
	ID              string         `json:"id"               db:"id"`
	HospitalID      string         `json:"hospital_id"      db:"hospital_id"`
	PatientKey      string         `json:"-"                db:"patient_key"`       // never expose in API response
	HMSPatientID    string         `json:"hms_patient_id"   db:"hms_patient_id"`
	Type            string         `json:"type"             db:"type"`
	Status          string         `json:"status"           db:"status"`
	LegalBasis      string         `json:"legal_basis"      db:"legal_basis"`
	Purposes        []string       `json:"purposes"         db:"purposes"`
	NoticeVersion   string         `json:"notice_version"   db:"notice_version"`
	Language        string         `json:"language"         db:"language"`
	OTPVerified     bool           `json:"otp_verified"     db:"otp_verified"`
	StaffOverrideID string         `json:"staff_override_id,omitempty" db:"staff_override_id"`
	KioskID         string         `json:"kiosk_id,omitempty"          db:"kiosk_id"`
	PreviousID      string         `json:"previous_id,omitempty"       db:"previous_id"`
	Version         int            `json:"version"          db:"version"`
	ArtifactHash    string         `json:"artifact_hash"    db:"artifact_hash"`
	CreatedAt       time.Time      `json:"created_at"       db:"created_at"`
}

// ConsentStatus is returned by the consent check endpoint.
type ConsentStatus struct {
	Allowed   bool     `json:"allowed"`
	Reason    string   `json:"reason,omitempty"`    // populated when allowed=false
	ConsentID string   `json:"consent_id,omitempty"`
	Purposes  []string `json:"purposes,omitempty"`
	CapturedAt *time.Time `json:"captured_at,omitempty"`
}
