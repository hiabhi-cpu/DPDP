package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// ReceptionHandler orchestrates "send code": read the staged patient's mobile,
// fire the OTP claim, mark the record CODE_SENT. All downstream calls carry the
// hospital JWT; the mobile is read server-side and never returned to the browser.
type ReceptionHandler struct {
	integrationBase  string
	notificationBase string
	token            hospitaljwt.TokenProvider
	client           *http.Client
}

func NewReceptionHandler(integrationBase, notificationBase string, token hospitaljwt.TokenProvider) *ReceptionHandler {
	return &ReceptionHandler{
		integrationBase:  integrationBase,
		notificationBase: notificationBase,
		token:            token,
		client:           &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *ReceptionHandler) do(ctx *gin.Context, method, url string, body any) (*http.Response, error) {
	tok, err := h.token.Token(ctx.Request.Context())
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx.Request.Context(), method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.client.Do(req)
}

// SendCode handles POST /api/v1/reception/registrations/:hms/send-code.
func (h *ReceptionHandler) SendCode(c *gin.Context) {
	sess := c.MustGet(bffmw.CtxUser).(session.Session)
	hms := c.Param("hms")

	// 1. Read the staged patient (raw mobile — stays server-side).
	resp, err := h.do(c, http.MethodGet, h.integrationBase+"/internal/v1/registrations/"+hms, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "no staged registration"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}
	var reg struct {
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil || reg.Mobile == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}

	// 2. Fire the OTP claim (hospital_id from the JWT; ref = hms).
	cr, err := h.do(c, http.MethodPost, h.notificationBase+"/internal/v1/otp/claim/send",
		map[string]string{"mobile": reg.Mobile, "ref": hms})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not send code"})
		return
	}
	defer cr.Body.Close()
	if cr.StatusCode == http.StatusTooManyRequests {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "please wait before resending"})
		return
	}
	if cr.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not send code"})
		return
	}

	// 3. Mark CODE_SENT (best-effort — the code is already out). Log on failure
	// so a queue item stuck at PENDING is diagnosable; the reception UI still
	// gets "sent" because the patient has the code.
	sr, err := h.do(c, http.MethodPost, h.integrationBase+"/internal/v1/registrations/"+hms+"/status",
		map[string]string{"status": "CODE_SENT"})
	if err != nil {
		log.Warnf("reception: CODE_SENT status update failed for hms=%s: %v", hms, err)
	} else {
		if sr.StatusCode != http.StatusOK {
			log.Warnf("reception: CODE_SENT status update returned %d for hms=%s", sr.StatusCode, hms)
		}
		sr.Body.Close()
	}
	// ponytail: hospital scoping is enforced downstream by the hospital JWT, and
	// admin-bff is single-tenant (one HOSPITAL_API_KEY), so sess.HospitalID always
	// matches the minted token. If admin-bff ever goes multi-tenant, assert that match here.
	_ = sess
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}
