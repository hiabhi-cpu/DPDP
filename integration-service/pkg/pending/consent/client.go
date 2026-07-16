// Package consent talks to consent-service's batch "already consented?" lookup,
// so the reception queue can badge returning patients rather than sending them to
// a kiosk whose capture will 409.
package consent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client calls consent-service's POST /api/v1/consent/active.
type Client struct {
	baseURL string
	client  *http.Client
}

// NewClient returns a Client for consent-service at baseURL.
//
// The 3s timeout is deliberately shorter than the reception queue's 5s poll
// interval: a slow consent-service must not make list requests pile up. On
// timeout the caller fails open and renders the queue unbadged.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, client: &http.Client{Timeout: 3 * time.Second}}
}

// ActiveMobiles returns a set of the mobiles that currently have an active
// consent. Mobiles go in the body, never the URL, so raw mobiles never reach an
// access log.
//
// authHeader is the caller's hospital JWT, forwarded verbatim. integration-service
// and consent-service verify the same auth-service key and the token carries no
// audience claim, so the token that authorised this request already authorises the
// downstream call — no second credential, and no privilege gained: admin-bff's
// token can call consent-service directly anyway.
func (c *Client) ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error) {
	body, err := json.Marshal(map[string][]string{"mobiles": mobiles})
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/consent/active", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: new request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("consent.ActiveMobiles: status %d", resp.StatusCode)
	}

	var out struct {
		Active []string `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: decode: %w", err)
	}

	active := make(map[string]bool, len(out.Active))
	for _, m := range out.Active {
		active[m] = true
	}
	return active, nil
}
