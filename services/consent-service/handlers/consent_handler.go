package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/DPDP/consent-service/service"
)

// ConsentHandler handles consent capture, check, and withdrawal HTTP endpoints.
type ConsentHandler struct {
	svc *service.ConsentService
}

// NewConsentHandler creates a ConsentHandler.
func NewConsentHandler(svc *service.ConsentService) *ConsentHandler {
	return &ConsentHandler{svc: svc}
}

// ── POST /v1/consent/capture ──────────────────────────────────────────────────

type CaptureRequest struct {
	Mobile        string   `json:"mobile"         binding:"required"`
	HMSPatientID  string   `json:"hms_patient_id"`
	Purposes      []string `json:"purposes"       binding:"required"`
	Language      string   `json:"language"`
	NoticeVersion string   `json:"notice_version"`
	OTPVerified   bool     `json:"otp_verified"`
	KioskID       string   `json:"kiosk_id"`
}

// Capture handles POST /v1/consent/capture.
// hospital_id is extracted from JWT context — never from the request body.
func (h *ConsentHandler) Capture(c *gin.Context) {
	hospitalID := mustGetHospitalID(c)

	var req CaptureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Language == "" {
		req.Language = "en"
	}
	if req.NoticeVersion == "" {
		req.NoticeVersion = "v1.0"
	}

	artifact, err := h.svc.Capture(c.Request.Context(), service.CaptureRequest{
		HospitalID:    hospitalID,
		Mobile:        req.Mobile,
		HMSPatientID:  req.HMSPatientID,
		Purposes:      req.Purposes,
		Language:      req.Language,
		NoticeVersion: req.NoticeVersion,
		OTPVerified:   req.OTPVerified,
		KioskID:       req.KioskID,
		RequestID:     c.GetHeader("X-Request-ID"),
		ActorIP:       c.ClientIP(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "consent capture failed"})
		return
	}

	c.JSON(http.StatusCreated, artifact)
}

// ── GET /v1/consent/check ─────────────────────────────────────────────────────

// Check handles GET /v1/consent/check.
// Used by HMS to check if a doctor can access patient data.
func (h *ConsentHandler) Check(c *gin.Context) {
	hospitalID := mustGetHospitalID(c)

	hmsPatientID := c.Query("hms_patient_id")
	purpose := c.Query("purpose")
	doctorID := c.Query("doctor_id")

	if hmsPatientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hms_patient_id query param is required"})
		return
	}

	status, err := h.svc.Check(c.Request.Context(), service.CheckRequest{
		HospitalID:   hospitalID,
		HMSPatientID: hmsPatientID,
		Purpose:      purpose,
		DoctorID:     doctorID,
		RequestID:    c.GetHeader("X-Request-ID"),
		ActorIP:      c.ClientIP(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "consent check failed"})
		return
	}

	c.JSON(http.StatusOK, status)
}

// ── POST /v1/consent/withdraw ─────────────────────────────────────────────────

type WithdrawRequest struct {
	Mobile       string   `json:"mobile"        binding:"required"`
	HMSPatientID string   `json:"hms_patient_id"`
	Purposes     []string `json:"purposes"` // empty = withdraw all
}

// Withdraw handles POST /v1/consent/withdraw.
func (h *ConsentHandler) Withdraw(c *gin.Context) {
	hospitalID := mustGetHospitalID(c)

	var req WithdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.Withdraw(c.Request.Context(), service.WithdrawRequest{
		HospitalID:   hospitalID,
		Mobile:       req.Mobile,
		HMSPatientID: req.HMSPatientID,
		Purposes:     req.Purposes,
		RequestID:    c.GetHeader("X-Request-ID"),
		ActorIP:      c.ClientIP(),
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "withdrawal failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "withdrawn", "message": "consent withdrawal recorded"})
}

// HealthHandler handles GET /health.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"service": "consent-service", "status": "ok"})
}

// mustGetHospitalID extracts hospital_id from Gin context (set by JWT middleware).
func mustGetHospitalID(c *gin.Context) string {
	id, _ := c.Get("hospital_id")
	return id.(string)
}
