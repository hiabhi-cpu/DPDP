package controller

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/adapter"
)

// WebhookHandler receives HMS registration webhooks over mTLS.
type WebhookHandler struct {
	store PendingStore
}

func NewWebhookHandler(store PendingStore) *WebhookHandler {
	return &WebhookHandler{store: store}
}

// PatientRegistered handles POST /webhook/patient-registered.
// hospital_id is the client cert's CN — the TLS layer has already verified the
// cert chains to our hospital CA (RequireAndVerifyClientCert), so a valid,
// non-empty CN is an authenticated hospital identity.
func (h *WebhookHandler) PatientRegistered(c *gin.Context) {
	if c.Request.TLS == nil || len(c.Request.TLS.PeerCertificates) == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "client certificate required"})
		return
	}
	hospitalID := c.Request.TLS.PeerCertificates[0].Subject.CommonName
	if hospitalID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "client certificate has no hospital identity (empty CN)"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)) // 1 MB cap
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
		return
	}

	reg, err := adapter.FromBahmni(body, hospitalID, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration payload"})
		return
	}

	if err := h.store.Upsert(c.Request.Context(), reg); err != nil {
		log.Errorf("integration-service: upsert failed for hospital %s: %v", hospitalID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable, retry"})
		return
	}

	// Never log the mobile or name.
	log.Infof("integration-service: staged registration hospital=%s hms_patient_id=%s", hospitalID, reg.HMSPatientID)
	c.JSON(http.StatusOK, gin.H{"status": "staged"})
}
