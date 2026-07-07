// Package serviceauth provides a small client that obtains and caches an
// internal service-to-service JWT from auth-service. Callers (e.g. the
// consent-service audit relay) use the returned token as a Bearer credential on
// /internal endpoints protected by middleware.InternalServiceAuth.
package serviceauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// refreshWindow is how long before expiry we proactively fetch a new token, so
// an in-flight request never carries a token that expires mid-call.
const refreshWindow = 60 * time.Second

// tokenRequest / tokenResponse mirror auth-service's POST /v1/auth/service-token.
type tokenRequest struct {
	ServiceName  string `json:"service_name"`
	ClientSecret string `json:"client_secret"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Client fetches and caches a service token. Safe for concurrent use.
type Client struct {
	authURL      string
	serviceName  string
	clientSecret string
	httpClient   *http.Client

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// NewClient builds a service-token client. authURL is the base URL of
// auth-service (e.g. http://localhost:9006); serviceName identifies the caller
// (e.g. "consent-service"); clientSecret is the shared bootstrap secret.
func NewClient(authURL, serviceName, clientSecret string) *Client {
	return &Client{
		authURL:      authURL,
		serviceName:  serviceName,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Token returns a currently-valid service token, fetching a fresh one when the
// cache is empty or inside the refresh window.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedToken != "" && time.Until(c.expiresAt) > refreshWindow {
		return c.cachedToken, nil
	}

	body, err := json.Marshal(tokenRequest{
		ServiceName:  c.serviceName,
		ClientSecret: c.clientSecret,
	})
	if err != nil {
		return "", fmt.Errorf("serviceauth: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/auth/service-token", c.authURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("serviceauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("serviceauth: request auth-service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("serviceauth: auth-service returned status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("serviceauth: decode response: %w", err)
	}
	if tr.Token == "" {
		return "", fmt.Errorf("serviceauth: auth-service returned empty token")
	}

	c.cachedToken = tr.Token
	c.expiresAt = tr.ExpiresAt
	return c.cachedToken, nil
}
