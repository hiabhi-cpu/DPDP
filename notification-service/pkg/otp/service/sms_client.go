package service

import (
	"context"
	"log"
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
	log.Printf("📱 [MOCK SMS] To: %s | OTP: %s", mobile, otp)
	return nil
}
