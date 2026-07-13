package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/service"
	"github.com/hiabhi-cpu/shared/middleware"
)

// OTPHandler holds HTTP handlers for the OTP domain.
type OTPHandler struct {
	svc service.OTPService
}

// NewOTPHandler creates an OTPHandler.
func NewOTPHandler(svc service.OTPService) *OTPHandler {
	return &OTPHandler{svc: svc}
}

// Send handles POST /api/otp/v1/send
func (h *OTPHandler) Send(c *gin.Context) {
	var req model.SendOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	resp, err := h.svc.Send(c.Request.Context(), req.Mobile)
	if err != nil {
		if errors.Is(err, service.ErrTooManyRequests) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many OTP requests — try again later"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send OTP"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// Verify handles POST /api/otp/v1/verify
func (h *OTPHandler) Verify(c *gin.Context) {
	var req model.VerifyOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	sessionID, err := h.svc.Verify(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidOTP) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired OTP"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify OTP"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"session_id": sessionID})
}

// ValidateSession handles POST /internal/v1/otp/session/validate — called by
// consent-service (with a service token) to confirm a capture's session_id is a
// live, OTP-verified session for the same mobile before any consent is written.
func (h *OTPHandler) ValidateSession(c *gin.Context) {
	var req model.ValidateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id and mobile are required"})
		return
	}

	if err := h.svc.ValidateSession(c.Request.Context(), req.SessionID, req.Mobile); err != nil {
		if errors.Is(err, service.ErrSessionNotVerified) {
			c.JSON(http.StatusNotFound, gin.H{"verified": false})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"verified": true})
}

// ClaimSend handles POST /internal/v1/otp/claim/send (hospital-JWT). Reception
// fires a code to a staged patient; hospital_id comes from the token.
func (h *OTPHandler) ClaimSend(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req model.ClaimSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mobile and ref are required"})
		return
	}
	resp, err := h.svc.SendClaim(c.Request.Context(), hospitalID, req.Mobile, req.Ref)
	if err != nil {
		if errors.Is(err, service.ErrTooManyRequests) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests — try again shortly"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send code"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ClaimResolve handles POST /internal/v1/otp/claim/resolve (hospital-JWT). The
// kiosk submits only the code; returns a verified session + the opaque ref.
func (h *OTPHandler) ClaimResolve(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req model.ClaimResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "otp is required"})
		return
	}
	res, err := h.svc.ResolveClaim(c.Request.Context(), hospitalID, req.OTP)
	if err != nil {
		if errors.Is(err, service.ErrTooManyRequests) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts — try again shortly"})
			return
		}
		// Generic — no enumeration signal.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "code not recognized"})
		return
	}
	c.JSON(http.StatusOK, res)
}
