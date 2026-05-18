package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/DPDP/audit-service/model"
	"github.com/hiabhi-cpu/DPDP/audit-service/service"
)

// AuditHandler handles internal audit log writes.
// This endpoint is NOT publicly exposed — it's called only by other services.
type AuditHandler struct {
	svc *service.AuditService
}

// NewAuditHandler creates an AuditHandler.
func NewAuditHandler(svc *service.AuditService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// LogRequest is the body for POST /internal/audit/log.
type LogRequest struct {
	HospitalID string            `json:"hospital_id" binding:"required"`
	EventType  model.EventType   `json:"event_type"  binding:"required"`
	ActorID    string            `json:"actor_id"`
	ActorType  model.ActorType   `json:"actor_type"  binding:"required"`
	PatientKey string            `json:"patient_key"`
	ConsentID  string            `json:"consent_id"`
	RequestID  string            `json:"request_id"`
	IPAddress  string            `json:"ip_address"`
	Details    map[string]any    `json:"details"`
}

// Log handles POST /internal/audit/log.
// Always returns 202 Accepted — audit failures should never block clinical workflows.
func (h *AuditHandler) Log(c *gin.Context) {
	var req LogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	event := model.AuditEvent{
		HospitalID: req.HospitalID,
		EventType:  req.EventType,
		ActorID:    req.ActorID,
		ActorType:  req.ActorType,
		PatientKey: req.PatientKey,
		ConsentID:  req.ConsentID,
		RequestID:  req.RequestID,
		IPAddress:  req.IPAddress,
		Details:    req.Details,
	}

	// Log asynchronously — never block the caller
	go func() {
		if err := h.svc.LogEvent(c.Request.Context(), event); err != nil {
			// In production: send to CloudWatch alarm
			// For now: structured log to stderr
			c.Error(err) //nolint
		}
	}()

	// Return 202 immediately — the caller doesn't wait for the DB write
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// GetLogs handles GET /v1/audit/logs — paginated, hospital-scoped.
func (h *AuditHandler) GetLogs(c *gin.Context) {
	// TODO: implement in Phase 1 — needed for admin dashboard
	// Will read hospital_id from JWT middleware context
	c.JSON(http.StatusNotImplemented, gin.H{"error": "not yet implemented"})
}

// HealthHandler handles GET /health.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"service": "audit-service", "status": "ok"})
}
