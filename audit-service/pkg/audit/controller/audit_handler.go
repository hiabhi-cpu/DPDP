package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/audit-service/pkg/audit/model"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/service"
	"github.com/hiabhi-cpu/shared/middleware"
)

// maxPageLimit caps GET /logs page size so a single request cannot dump the table.
const maxPageLimit = 200

// AuditHandler holds HTTP handlers for the audit domain.
type AuditHandler struct {
	svc service.AuditService
}

// New creates an AuditHandler.
func New(svc service.AuditService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// LogEvent handles POST /internal/audit/log.
// Protected by internal network policies, or possibly JWT if services issue tokens to each other.
func (h *AuditHandler) LogEvent(c *gin.Context) {
	var event model.AuditEvent
	if err := c.ShouldBindJSON(&event); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// event_id is the idempotency key — required so ON CONFLICT can dedupe replays.
	if event.EventID == uuid.Nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "event_id is required"})
		return
	}

	// The caller is an authenticated internal service (InternalServiceAuth); it
	// supplies hospital_id in the payload.
	if err := h.svc.LogEvent(c.Request.Context(), &event); err != nil {
		// Log the real cause; never echo internal errors to the caller.
		log.Printf("audit-service: LogEvent failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to log event"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "logged"})
}

// GetLogs handles GET /api/audit/v1/logs.
// Protected by JWT middleware. Hospital ID is extracted from the JWT context.
func (h *AuditHandler) GetLogs(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	// Clamp pagination: a bad or hostile value must not become a negative
	// OFFSET (500) or an unbounded LIMIT (table dump in one response).
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 {
		limit = 50
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	eventType := c.Query("event_type")

	filter := model.AuditLogFilter{
		HospitalID: hospitalID,
		EventType:  eventType,
		Page:       page,
		Limit:      limit,
	}

	pageData, err := h.svc.GetLogs(c.Request.Context(), filter)
	if err != nil {
		log.Printf("audit-service: GetLogs failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch logs"})
		return
	}

	c.JSON(http.StatusOK, pageData)
}
