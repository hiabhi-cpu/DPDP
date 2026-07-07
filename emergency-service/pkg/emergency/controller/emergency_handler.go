package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/model"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/service"
	"github.com/hiabhi-cpu/shared/middleware"
)

type EmergencyHandler struct {
	svc service.EmergencyService
}

func NewEmergencyHandler(svc service.EmergencyService) *EmergencyHandler {
	return &EmergencyHandler{svc: svc}
}

const clinicalNoteMax = 200

// Override handles POST /api/v1/consent/emergency-override. It NEVER blocks
// emergency access — a well-formed request always returns allowed:true. Only a
// malformed request (missing/invalid fields) is rejected.
func (h *EmergencyHandler) Override(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.EmergencyOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "doctor_id, emergency_reason and clinical_note are required"})
		return
	}
	if len(req.ClinicalNote) > clinicalNoteMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clinical_note must be 200 characters or fewer"})
		return
	}

	resp, err := h.svc.Override(c.Request.Context(), hospitalID, c.ClientIP(), &req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidReason) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record emergency access"})
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// Pending handles GET /api/v1/emergency/pending — the DPO review queue.
func (h *EmergencyHandler) Pending(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	items, err := h.svc.Pending(c.Request.Context(), hospitalID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list pending reviews"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"pending": items, "total": len(items)})
}

// Review handles POST /api/v1/emergency/:id/review — a DPO decision.
func (h *EmergencyHandler) Review(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	accessID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid access id"})
		return
	}

	var req model.ReviewDecisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "decision and reviewer_id are required"})
		return
	}

	err = h.svc.Review(c.Request.Context(), hospitalID, c.ClientIP(), accessID, &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidDecision):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrReviewNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record review"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "reviewed", "access_id": accessID, "decision": req.Decision})
}
