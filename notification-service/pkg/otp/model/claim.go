package model

// ClaimSendRequest is the body for POST /internal/v1/otp/claim/send.
// hospital_id is taken from the JWT, not the body.
type ClaimSendRequest struct {
	Mobile string `json:"mobile" binding:"required,len=10"`
	Ref    string `json:"ref" binding:"required"`
}

// ClaimResolveRequest is the body for POST /internal/v1/otp/claim/resolve.
type ClaimResolveRequest struct {
	OTP string `json:"otp" binding:"required,len=6"`
}

// ClaimResolveResult is returned to a trusted internal caller (kiosk-bff).
type ClaimResolveResult struct {
	SessionID string `json:"session_id"`
	Mobile    string `json:"mobile"`
	Ref       string `json:"ref"`
}
