package secrets

import "context"

// Provider is the interface all services use to fetch secrets.
// Local dev uses MockProvider (reads from local JSON file).
// Production uses AWSProvider (reads from AWS Secrets Manager ap-south-1).
//
// Services should store a Provider instance and call it per-request —
// AWS provider uses caching internally to avoid rate limits.
type Provider interface {
	// GetHospitalKey returns the hospital-specific HMAC key for a given hospital UUID.
	// Stored in AWS Secrets Manager at: /consentmanager/hospitals/{hospitalID}/key
	// This key is combined with SYSTEM_SALT to produce patient_key hashes.
	GetHospitalKey(ctx context.Context, hospitalID string) (string, error)

	// GetSystemSalt returns the global SYSTEM_SALT used in all patient key hashes.
	// Stored in AWS Secrets Manager at: /consentmanager/system/salt
	// This value NEVER changes after the first consent is captured — rotation
	// requires recomputing all existing patient_key hashes.
	GetSystemSalt(ctx context.Context) (string, error)
}
