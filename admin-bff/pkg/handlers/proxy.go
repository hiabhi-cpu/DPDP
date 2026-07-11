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

// Proxy forwards a request to one downstream service, attaching the hospital JWT.
type Proxy struct {
	base   string
	token  hospitaljwt.TokenProvider
	client *http.Client
}

// NewProxy builds a Proxy for the given downstream base URL.
func NewProxy(base string, token hospitaljwt.TokenProvider) *Proxy {
	return &Proxy{base: base, token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

// bearer fetches a hospital JWT or writes 502 and reports failure.
func (p *Proxy) bearer(c *gin.Context) (string, bool) {
	tok, err := p.token.Token(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth upstream unavailable"})
		return "", false
	}
	return tok, true
}

// pipe copies a downstream response back to the client (status + body).
func pipe(c *gin.Context, resp *http.Response) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("pipe: error reading downstream response body: %v", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, body)
}

// ForwardGet proxies a GET to base+path, preserving the query string and adding
// the Bearer hospital JWT. downstreamPath is the target service's route.
func (p *Proxy) ForwardGet(c *gin.Context, downstreamPath string) {
	tok, ok := p.bearer(c)
	if !ok {
		return
	}
	url := p.base + downstreamPath
	if raw := c.Request.URL.RawQuery; raw != "" {
		url += "?" + raw
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad upstream request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := p.client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
		return
	}
	pipe(c, resp)
}

// ForwardReview proxies POST /api/emergency/:id/review, injecting reviewer_id from
// the authenticated session so it is never client-supplied free text.
func (p *Proxy) ForwardReview(c *gin.Context) {
	v, ok := c.Get(bffmw.CtxUser)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	sess := v.(session.Session)

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	if body == nil {
		body = map[string]any{}
	}
	body["reviewer_id"] = sess.Email // server-injected identity

	payload, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad review payload"})
		return
	}

	tok, ok := p.bearer(c)
	if !ok {
		return
	}
	id := c.Param("id")
	url := fmt.Sprintf("%s/api/v1/emergency/%s/review", p.base, id)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad upstream request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
		return
	}
	pipe(c, resp)
}
