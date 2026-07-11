package handlers

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// Proxy forwards a request to one downstream service, attaching the hospital JWT.
// The JWT and API key stay server-side — only the downstream body is piped back.
type Proxy struct {
	base   string
	token  hospitaljwt.TokenProvider
	client *http.Client
}

// NewProxy builds a Proxy for the given downstream base URL.
func NewProxy(base string, token hospitaljwt.TokenProvider) *Proxy {
	return &Proxy{base: base, token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

// ForwardPost proxies the incoming POST body to base+downstreamPath with the
// Bearer hospital JWT attached, then pipes the downstream status and body back.
func (p *Proxy) ForwardPost(c *gin.Context, downstreamPath string) {
	tok, err := p.token.Token(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth upstream unavailable"})
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, p.base+downstreamPath, c.Request.Body)
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
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("kiosk proxy: read downstream body: %v", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, body)
}

// StubProvider returns a TokenProvider that always yields tok (test helper).
func StubProvider(tok string) hospitaljwt.TokenProvider { return stubProvider(tok) }

type stubProvider string

func (s stubProvider) Token(_ context.Context) (string, error) { return string(s), nil }
