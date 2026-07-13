package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/repository"
	"github.com/hiabhi-cpu/shared/middleware"
)

// ReadHandler serves the internal, hospital-scoped read API.
type ReadHandler struct {
	store PendingStore
}

func NewReadHandler(store PendingStore) *ReadHandler {
	return &ReadHandler{store: store}
}

// listItem is the masked shape returned by List (no raw mobile on a list).
type listItem struct {
	HMSPatientID string `json:"hms_patient_id"`
	Name         string `json:"name"`
	Mobile       string `json:"mobile"` // masked
	RegisteredAt string `json:"registered_at"`
	Status       string `json:"status"`
}

// List handles GET /internal/v1/registrations — pending records for the
// hospital in the JWT. Mobiles are masked; the reception queue only needs to
// recognise a patient, not read their number.
func (h *ReadHandler) List(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	recs, err := h.store.List(c.Request.Context(), hospitalID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	items := make([]listItem, 0, len(recs))
	for _, r := range recs {
		items = append(items, listItem{
			HMSPatientID: r.HMSPatientID,
			Name:         r.Name,
			Mobile:       maskMobile(r.Mobile),
			RegisteredAt: r.RegisteredAt,
			Status:       r.Status,
		})
	}
	c.JSON(http.StatusOK, items)
}

// Get handles GET /internal/v1/registrations/:hms_patient_id — one record with
// the RAW mobile (a trusted consumer needs it to send the OTP).
func (h *ReadHandler) Get(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	reg, err := h.store.Get(c.Request.Context(), hospitalID, c.Param("hms_patient_id"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	if reg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending registration"})
		return
	}
	c.JSON(http.StatusOK, reg)
}

// setStatusRequest is the body for POST .../status.
type setStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// SetStatus handles POST /internal/v1/registrations/:hms_patient_id/status.
// PENDING is only ever set by the webhook, so callers may set CODE_SENT or DONE.
func (h *ReadHandler) SetStatus(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req setStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}
	if req.Status != "CODE_SENT" && req.Status != "DONE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be CODE_SENT or DONE"})
		return
	}
	err := h.store.SetStatus(c.Request.Context(), hospitalID, c.Param("hms_patient_id"), req.Status)
	if err == repository.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending registration"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": req.Status})
}

// maskMobile keeps the first 2 and last 4 digits: 9876543210 -> 98****3210.
func maskMobile(m string) string {
	if len(m) != 10 {
		return "****"
	}
	return m[:2] + "****" + m[6:]
}
