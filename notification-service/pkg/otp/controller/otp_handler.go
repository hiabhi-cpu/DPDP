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
