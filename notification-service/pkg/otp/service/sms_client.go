package service

import (
	"context"
	"log"
	"strings"
)

// SMSClient abstracts the delivery mechanism for SMS.
type SMSClient interface {
	SendOTP(ctx context.Context, mobile, otp string) error
}

type mockSMSClient struct{}

// NewMockSMSClient creates a mock client that prints the OTP to stdout.
// Used for local development and CI.
func NewMockSMSClient() SMSClient {
	return &mockSMSClient{}
}

func (m *mockSMSClient) SendOTP(_ context.Context, mobile, otp string) error {
	// Never log the full mobile number — logs are not a PII store. The OTP is
	// printed because this mock IS the delivery channel in local dev.
	log.Printf("📱 [MOCK SMS] To: %s | OTP: %s", maskMobile(mobile), otp)
	return nil
}

// maskMobile keeps only the last 4 digits (e.g. "******7890").
func maskMobile(mobile string) string {
	if len(mobile) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(mobile)-4) + mobile[len(mobile)-4:]
}
