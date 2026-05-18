package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// SMSClient is the interface for sending OTP SMS messages.
// MockSMSClient prints to stdout; MSG91Client calls the real API.
type SMSClient interface {
	SendOTP(ctx context.Context, mobile, otp string) error
}

// ── MSG91 Client ──────────────────────────────────────────────────────────────

// MSG91Client sends real OTPs via MSG91 REST API.
type MSG91Client struct {
	authKey    string
	senderID   string
	templateID string
	httpClient *http.Client
}

// NewMSG91Client creates a production SMS client.
func NewMSG91Client(authKey, senderID, templateID string) *MSG91Client {
	return &MSG91Client{
		authKey:    authKey,
		senderID:   senderID,
		templateID: templateID,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type msg91OTPRequest struct {
	Template  string `json:"template_id"`
	Mobile    string `json:"mobile"`
	AuthKey   string `json:"authkey"`
	OTP       string `json:"otp"`
	SenderID  string `json:"sender"`
}

// SendOTP calls MSG91 Send OTP API.
func (c *MSG91Client) SendOTP(ctx context.Context, mobile, otp string) error {
	body := msg91OTPRequest{
		Template: c.templateID,
		Mobile:   "91" + mobile, // MSG91 requires country code prefix
		AuthKey:  c.authKey,
		OTP:      otp,
		SenderID: c.senderID,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("msg91: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.msg91.com/api/v5/otp", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("msg91: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("authkey", c.authKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("msg91: http call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("msg91: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// ── Mock Client (local dev) ───────────────────────────────────────────────────

// MockSMSClient prints OTPs to stdout. Activated when MSG91_AUTH_KEY=mock.
// NEVER use in production.
type MockSMSClient struct{}

// NewMockSMSClient creates a mock client for local development.
func NewMockSMSClient() *MockSMSClient {
	return &MockSMSClient{}
}

// SendOTP prints the OTP to stdout — for local dev and testing ONLY.
func (m *MockSMSClient) SendOTP(_ context.Context, mobile, otp string) error {
	// Mask most of the mobile number in logs
	masked := maskMobile(mobile)
	log.Printf("📱 [MOCK SMS] OTP for %s: %s  (expires in 5 minutes)\n", masked, otp)
	return nil
}

func maskMobile(mobile string) string {
	if len(mobile) < 4 {
		return "****"
	}
	return strings.Repeat("*", len(mobile)-4) + mobile[len(mobile)-4:]
}
