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

// chunkSize mirrors consent-service's own binding cap on this endpoint
// (Mobiles field tagged binding:"...,max=200,..." in
// consent-service/pkg/consent/model/consent.go). consent-service rejects the
// WHOLE batch with 400 above that cap, and store.List's 72h Redis window is
// unbounded — it can hold far more than 200 records once a hospital has been
// live a few days. So the sender, not just the trust boundary, must respect
// the cap: split into chunks here and merge the results.
const chunkSize = 200

// lookupTimeout bounds the ENTIRE chunked lookup in ActiveMobiles, not any one
// request: a large queue can take several chunks, and http.Client.Timeout
// alone only caps each request individually, letting the total run well past
// it. This must stay comfortably under admin-bff's 10s proxy timeout
// (admin-bff/pkg/handlers/proxy.go, NewProxy) so a slow consent-service
// degrades to an unbadged reception board (fail-open) rather than admin-bff's
// proxy timing out and the board coming back empty.
const lookupTimeout = 3 * time.Second

// NewClient returns a Client for consent-service at baseURL.
//
// The 3s per-request timeout is a backstop against a single hung request; it
// does not bound the overall chunked lookup — that's lookupTimeout, applied
// once across all chunks in ActiveMobiles. On either the per-request or
// overall deadline, the caller fails open and renders the queue unbadged.
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
//
// mobiles is deduped before chunking (two staged records — e.g. family members —
// can share a mobile) and sent in batches of at most chunkSize, merging the
// per-chunk results into one map. The map is keyed by mobile, so deduping the
// request cannot lose information: the caller looks up consented[r.Mobile] per
// row, and a mobile present in any chunk's response ends up true in the merge.
func (c *Client) ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	seen := make(map[string]bool, len(mobiles))
	unique := make([]string, 0, len(mobiles))
	for _, m := range mobiles {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
	}

	active := make(map[string]bool, len(unique))
	for start := 0; start < len(unique); start += chunkSize {
		end := start + chunkSize
		if end > len(unique) {
			end = len(unique)
		}
		got, err := c.activeMobilesChunk(ctx, authHeader, unique[start:end])
		if err != nil {
			return nil, err
		}
		for m := range got {
			active[m] = true
		}
	}
	return active, nil
}

// activeMobilesChunk sends one request for at most chunkSize mobiles.
func (c *Client) activeMobilesChunk(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error) {
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
