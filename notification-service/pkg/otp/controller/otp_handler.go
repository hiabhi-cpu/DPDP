package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/service"
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
