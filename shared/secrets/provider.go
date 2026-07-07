// Package secrets provides an abstraction over secret key storage.
// In local dev, secrets are read from a JSON file.
// In production, they are fetched from AWS Secrets Manager (ap-south-1).
package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Provider defines the interface for fetching secrets needed for patient
// key computation. All implementations must be safe for concurrent use.
type Provider interface {
	// GetSystemSalt returns the global HMAC salt. This value must NEVER change
	// after the first consent is captured — changing it makes all existing
	// patient keys unresolvable.
	GetSystemSalt(ctx context.Context) (string, error)

	// GetHospitalKey returns the hospital-specific HMAC key for the given
	// hospitalID. Each hospital has a unique key stored in the secret store.
	// This key ensures hospital data isolation at the cryptographic level.
	GetHospitalKey(ctx context.Context, hospitalID string) (string, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// MockProvider — local development
// ─────────────────────────────────────────────────────────────────────────────

// mockConfig mirrors the JSON structure of local_hospital_keys.json
type mockConfig struct {
	SystemSalt   string            `json:"system_salt"`
	HospitalKeys map[string]string `json:"hospital_keys"`
}

// MockProvider reads secrets from a local JSON file. Used when
// AWS_SECRETS_MOCK=true in the environment. Thread-safe via sync.Once.
type MockProvider struct {
	once   sync.Once
	config mockConfig
	path   string
	err    error
}

// NewMockProvider creates a MockProvider that reads from the given file path.
// The file is loaded lazily on first use.
func NewMockProvider(filePath string) *MockProvider {
	return &MockProvider{path: filePath}
}

func (m *MockProvider) load() {
	m.once.Do(func() {
		data, err := os.ReadFile(m.path)
		if err != nil {
			m.err = fmt.Errorf("secrets.MockProvider: failed to read %q: %w", m.path, err)
			return
		}
		if err := json.Unmarshal(data, &m.config); err != nil {
			m.err = fmt.Errorf("secrets.MockProvider: failed to parse %q: %w", m.path, err)
		}
	})
}

func (m *MockProvider) GetSystemSalt(_ context.Context) (string, error) {
	m.load()
	if m.err != nil {
		return "", m.err
	}
	if m.config.SystemSalt == "" {
		return "", fmt.Errorf("secrets.MockProvider: system_salt is empty in %q", m.path)
	}
	return m.config.SystemSalt, nil
}

func (m *MockProvider) GetHospitalKey(_ context.Context, hospitalID string) (string, error) {
	m.load()
	if m.err != nil {
		return "", m.err
	}
	key, ok := m.config.HospitalKeys[hospitalID]
	if !ok || key == "" {
		return "", fmt.Errorf("secrets.MockProvider: no key found for hospital %q in %q", hospitalID, m.path)
	}
	return key, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AWSProvider — production (AWS Secrets Manager, ap-south-1)
// ─────────────────────────────────────────────────────────────────────────────

// AWSProvider fetches secrets from AWS Secrets Manager.
// Paths follow the convention:
//   - System salt:   /consentmanager/system/salt
//   - Hospital key:  /consentmanager/hospitals/{hospitalID}/key
//
// NOTE: This is a stub. Wire in aws-sdk-go-v2 when deploying to production.
type AWSProvider struct {
	region string
}

// NewAWSProvider creates a provider backed by AWS Secrets Manager in the
// given region. Must be ap-south-1 for DPDP data localisation compliance.
func NewAWSProvider(region string) *AWSProvider {
	return &AWSProvider{region: region}
}

func (a *AWSProvider) GetSystemSalt(_ context.Context) (string, error) {
	// TODO: implement aws-sdk-go-v2 GetSecretValue call
	// secretID := "/consentmanager/system/salt"
	return "", fmt.Errorf("secrets.AWSProvider: not yet implemented — use MockProvider for local dev")
}

func (a *AWSProvider) GetHospitalKey(_ context.Context, hospitalID string) (string, error) {
	// TODO: implement aws-sdk-go-v2 GetSecretValue call
	// secretID := fmt.Sprintf("/consentmanager/hospitals/%s/key", hospitalID)
	return "", fmt.Errorf("secrets.AWSProvider: not yet implemented — use MockProvider for local dev")
}

// ─────────────────────────────────────────────────────────────────────────────
// NewFromEnv — factory that picks provider based on AWS_SECRETS_MOCK env var
// ─────────────────────────────────────────────────────────────────────────────

// NewFromEnv returns a MockProvider if AWS_SECRETS_MOCK=true, otherwise an
// AWSProvider. Services call this once at startup.
//
// Required env vars:
//   - AWS_SECRETS_MOCK=true|false
//   - LOCAL_SECRETS_PATH=./secrets/local_hospital_keys.json  (mock only)
//   - AWS_REGION=ap-south-1                                  (AWS only)
func NewFromEnv() (Provider, error) {
	mock := os.Getenv("AWS_SECRETS_MOCK")
	if mock == "true" {
		path := os.Getenv("LOCAL_SECRETS_PATH")
		if path == "" {
			return nil, fmt.Errorf("secrets.NewFromEnv: LOCAL_SECRETS_PATH must be set when AWS_SECRETS_MOCK=true")
		}
		return NewMockProvider(path), nil
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "ap-south-1" // DPDP data localisation default
	}
	return NewAWSProvider(region), nil
}
