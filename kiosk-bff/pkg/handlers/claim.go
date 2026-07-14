package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// ClaimHandler drives the code-only kiosk: resolve a code into a verified
// session (+ the patient's name), and forward capture then mark the staged
// record DONE. All downstream calls carry the hospital JWT.
type ClaimHandler struct {
	notificationBase string
	integrationBase  string
	consentBase      string
	token            hospitaljwt.TokenProvider
	client           *http.Client
}

func NewClaimHandler(notificationBase, integrationBase, consentBase string, token hospitaljwt.TokenProvider) *ClaimHandler {
	return &ClaimHandler{
		notificationBase: notificationBase,
		integrationBase:  integrationBase,
		consentBase:      consentBase,
		token:            token,
		client:           &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *ClaimHandler) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	tok, err := h.token.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.client.Do(req)
}

// Resolve handles POST /kiosk/api/claim/resolve {otp}. Resolves the code to a
// verified session, looks up the patient's name, returns the identity the
// consent step needs. The raw mobile is included (the walk-in flow already put
// a mobile in the browser; the kiosk resets on done).
func (h *ClaimHandler) Resolve(c *gin.Context) {
	otpBody, err := io.ReadAll(io.LimitReader(c.Request.Body, 4096))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	resp, err := h.do(c.Request.Context(), http.MethodPost, h.notificationBase+"/internal/v1/otp/claim/resolve", otpBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "code service unavailable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Pipe the generic error (401 "code not recognized" / 429) straight back.
		rb, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Data(resp.StatusCode, ct, rb)
		return
	}
	var claim struct {
		SessionID string `json:"session_id"`
		Mobile    string `json:"mobile"`
		Ref       string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad code response"})
		return
	}

	// Best-effort name lookup — an outage here must not fail a verified resolve.
	name := ""
	if reg, err := h.do(c.Request.Context(), http.MethodGet, h.integrationBase+"/internal/v1/registrations/"+claim.Ref, nil); err == nil {
		if reg.StatusCode == http.StatusOK {
			var r struct {
				Name string `json:"name"`
			}
			if json.NewDecoder(reg.Body).Decode(&r) == nil {
				name = r.Name
			}
		}
		reg.Body.Close()
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id":     claim.SessionID,
		"mobile":         claim.Mobile,
		"name":           name,
		"hms_patient_id": claim.Ref,
	})
}

// Capture handles POST /kiosk/api/consent/capture. Forwards the body to
// consent-service; on a 201 carrying an hms_patient_id, marks the staged record
// DONE (best-effort). Pipes the capture response back either way.
func (h *ClaimHandler) Capture(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	resp, err := h.do(c.Request.Context(), http.MethodPost, h.consentBase+"/api/v1/consent/capture", body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "consent service unavailable"})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusCreated {
		var req struct {
			HMSPatientID string `json:"hms_patient_id"`
		}
		if json.Unmarshal(body, &req) == nil && req.HMSPatientID != "" {
			// Detached + Background context: the DONE mark is best-effort bookkeeping
			// and must not add latency to (or be cancelled by) the patient's response.
			go h.markDone(req.HMSPatientID)
		}
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, respBody)
}

func (h *ClaimHandler) markDone(hms string) {
	sr, err := h.do(context.Background(), http.MethodPost, h.integrationBase+"/internal/v1/registrations/"+hms+"/status",
		[]byte(`{"status":"DONE"}`))
	if err != nil {
		log.Warnf("kiosk-bff: DONE status update failed for hms=%s: %v", hms, err)
		return
	}
	if sr.StatusCode != http.StatusOK {
		log.Warnf("kiosk-bff: DONE status update returned %d for hms=%s", sr.StatusCode, hms)
	}
	sr.Body.Close()
}
