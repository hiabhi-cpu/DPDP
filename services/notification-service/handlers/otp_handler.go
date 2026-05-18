package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/DPDP/notification-service/service"
)

// OTPHandler handles OTP send and verify endpoints.
type OTPHandler struct {
	otpSvc *service.OTPService
}

// NewOTPHandler creates an OTPHandler.
func NewOTPHandler(otpSvc *service.OTPService) *OTPHandler {
	return &OTPHandler{otpSvc: otpSvc}
}

// SendOTPRequest is the body for POST /v1/otp/send.
type SendOTPRequest struct {
	Mobile     string `json:"mobile"      binding:"required"`
	HospitalID string `json:"hospital_id" binding:"required"`
	Purpose    string `json:"purpose"     binding:"required"`
}

// SendOTP handles POST /v1/otp/send.
func (h *OTPHandler) SendOTP(c *gin.Context) {
	var req SendOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.otpSvc.SendOTP(c.Request.Context(), req.HospitalID, req.Mobile, req.Purpose)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send OTP"})
		return
	}

	// NOTE: raw mobile is NOT echoed back in response
	c.JSON(http.StatusOK, gin.H{
		"session_id": result.SessionID,
		"expires_at": result.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		"expires_in": 300, // seconds
	})
}

// VerifyOTPRequest is the body for POST /v1/otp/verify.
type VerifyOTPRequest struct {
	HospitalID string `json:"hospital_id" binding:"required"`
	Mobile     string `json:"mobile"      binding:"required"`
	OTP        string `json:"otp"         binding:"required"`
}

// VerifyOTP handles POST /v1/otp/verify.
func (h *OTPHandler) VerifyOTP(c *gin.Context) {
	var req VerifyOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.otpSvc.VerifyOTP(c.Request.Context(), req.HospitalID, req.Mobile, req.OTP)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OTP verification failed"})
		return
	}

	if result.Locked {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"verified": false,
			"locked":   true,
			"message":  result.Message,
		})
		return
	}

	if !result.Verified {
		c.JSON(http.StatusUnauthorized, gin.H{
			"verified": false,
			"message":  result.Message,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"verified": true,
		"message":  "OTP verified successfully",
	})
}

// HealthHandler handles GET /health.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"service": "notification-service", "status": "ok"})
}
