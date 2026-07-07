package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/service"
	"github.com/hiabhi-cpu/shared/middleware"
)

type ConsentHandler struct {
	svc service.ConsentService
}

func NewConsentHandler(svc service.ConsentService) *ConsentHandler {
	return &ConsentHandler{svc: svc}
}

// Capture handles POST /api/consent/v1/capture
func (h *ConsentHandler) Capture(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.CaptureConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	consent, created, err := h.svc.Capture(c.Request.Context(), hospitalID, c.ClientIP(), &req)
	if err != nil {
		if errors.Is(err, service.ErrActiveConsentExists) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrSessionNotVerified) {
			c.JSON(http.StatusForbidden, gin.H{"error": "otp session invalid or expired — verify OTP first"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to capture consent"})
		return
	}

	// 201 for a new record; 200 for an idempotent replay of the same session_id.
	if created {
		c.JSON(http.StatusCreated, consent)
		return
	}
	c.JSON(http.StatusOK, consent)
}

// Check handles POST /api/consent/v1/check. Mobile is read from the JSON body,
// not the URL, to keep raw mobile numbers out of access logs.
func (h *ConsentHandler) Check(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.CheckConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "purpose is required"})
		return
	}
	// Identify the patient by exactly one of hms_patient_id (doctor/HMS path) or
	// mobile (kiosk/portal path).
	hasMobile := req.Mobile != ""
	hasHMS := req.HMSPatientID != ""
	if hasMobile == hasHMS {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide exactly one of mobile or hms_patient_id"})
		return
	}
	if hasMobile && len(req.Mobile) != 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mobile must be 10 digits"})
		return
	}

	resp, err := h.svc.Check(c.Request.Context(), hospitalID, c.ClientIP(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check consent"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// Withdraw handles POST /api/consent/v1/withdraw
func (h *ConsentHandler) Withdraw(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.WithdrawConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	err := h.svc.Withdraw(c.Request.Context(), hospitalID, c.ClientIP(), &req)
	if err != nil {
		if errors.Is(err, service.ErrNoActiveConsent) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrSessionNotVerified) {
			c.JSON(http.StatusForbidden, gin.H{"error": "otp session invalid or expired — verify OTP first"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to withdraw consent"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "withdrawn"})
}

// Grant handles POST /api/consent/v1/grant — re-grant a withdrawn purpose or add
// a new one to an existing consent chain.
func (h *ConsentHandler) Grant(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.GrantConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload"})
		return
	}

	consent, changed, err := h.svc.Grant(c.Request.Context(), hospitalID, c.ClientIP(), &req)
	if err != nil {
		if errors.Is(err, service.ErrNoConsentToRenew) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrSessionNotVerified) {
			c.JSON(http.StatusForbidden, gin.H{"error": "otp session invalid or expired — verify OTP first"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to grant consent"})
		return
	}

	// 201 when a renewal row was appended; 200 when it was a no-op (already active).
	if changed {
		c.JSON(http.StatusCreated, consent)
		return
	}
	c.JSON(http.StatusOK, consent)
}
