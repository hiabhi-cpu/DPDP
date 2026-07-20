package model

import "time"

// SendOTPRequest is the body for POST /api/otp/v1/send
type SendOTPRequest struct {
	Mobile string `json:"mobile" binding:"required,len=10"`
}

// SendOTPResponse is the response for a successful send
type SendOTPResponse struct {
	ReferenceID string    `json:"reference_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// VerifyOTPRequest is the body for POST /api/otp/v1/verify
type VerifyOTPRequest struct {
	ReferenceID string `json:"reference_id" binding:"required"`
	OTP         string `json:"otp" binding:"required,len=6"`
	Mobile      string `json:"mobile" binding:"required,len=10"`
}

// SessionState is what gets stored in Redis after successful verification.
// Ref is the HMS patient ID the OTP was issued for. It is what makes the session
// name a PERSON rather than a phone: a family shares one mobile, so the mobile
// alone cannot say who is consenting.
type SessionState struct {
	Mobile    string    `json:"mobile"`
	Ref       string    `json:"ref"`
	Verified  bool      `json:"verified"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ValidateSessionRequest is the body for POST /internal/v1/otp/session/validate.
type ValidateSessionRequest struct {
	SessionID    string `json:"session_id" binding:"required"`
	Mobile       string `json:"mobile" binding:"required,len=10"`
	HMSPatientID string `json:"hms_patient_id" binding:"required"`
}
