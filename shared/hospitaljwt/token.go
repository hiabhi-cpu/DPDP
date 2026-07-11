package hospitaljwt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// refreshWindow proactively refreshes before expiry so an in-flight request never
// carries a token that expires mid-call.
const refreshWindow = 60 * time.Second

// TokenProvider yields a currently-valid hospital JWT.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// HospitalTokenClient exchanges the hospital API key for a hospital JWT via
// auth-service POST /v1/auth/token, caching until near expiry. Safe for concurrent use.
type HospitalTokenClient struct {
	authURL    string
	apiKey     string
	httpClient *http.Client

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// NewHospitalTokenClient builds the client. authURL is auth-service's base URL;
// apiKey is the raw hospital API key held server-side.
func NewHospitalTokenClient(authURL, apiKey string) *HospitalTokenClient {
	return &HospitalTokenClient{
		authURL:    authURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

type issueTokenRequest struct {
	APIKey string `json:"api_key"`
}

type issueTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Token returns a valid hospital JWT, fetching a fresh one when the cache is empty
// or inside the refresh window.
func (c *HospitalTokenClient) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedToken != "" && time.Until(c.expiresAt) > refreshWindow {
		return c.cachedToken, nil
	}

	body, err := json.Marshal(issueTokenRequest{APIKey: c.apiKey})
	if err != nil {
		return "", fmt.Errorf("auth: marshal token request: %w", err)
	}
	url := fmt.Sprintf("%s/v1/auth/token", c.authURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: request auth-service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: auth-service returned status %d", resp.StatusCode)
	}

	var tr issueTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("auth: decode token response: %w", err)
	}
	if tr.Token == "" {
		return "", fmt.Errorf("auth: auth-service returned empty token")
	}
	c.cachedToken = tr.Token
	c.expiresAt = tr.ExpiresAt
	return c.cachedToken, nil
}
