package model

// PendingRegistration is a short-lived, pre-consent staging record created from
// an HMS "patient registered" webhook. It holds identity only — no consent is
// implied. Stored in Redis with a TTL; never written to the vault.
type PendingRegistration struct {
	HospitalID   string `json:"hospital_id"`    // from the mTLS client cert, never the body
	HMSPatientID string `json:"hms_patient_id"`
	Name         string `json:"name"`
	Mobile       string `json:"mobile"`         // raw; masked whenever surfaced in a list
	DOB          string `json:"dob,omitempty"`  // optional; feeds the later §9 age-gate
	RegisteredAt string `json:"registered_at"` // RFC3339, when we received the webhook
}
