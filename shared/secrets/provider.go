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
// NewFromEnv — factory (mock only for now; AWS Secrets Manager lands in Phase 3)
// ─────────────────────────────────────────────────────────────────────────────

// NewFromEnv returns a MockProvider when AWS_SECRETS_MOCK=true. The AWS Secrets
// Manager provider is not built yet (Phase 3), so any other value is a
// configuration error rather than a silent stub.
//
// Required env vars (mock):
//   - AWS_SECRETS_MOCK=true
//   - LOCAL_SECRETS_PATH=./secrets/local_hospital_keys.json
func NewFromEnv() (Provider, error) {
	if os.Getenv("AWS_SECRETS_MOCK") != "true" {
		return nil, fmt.Errorf("secrets.NewFromEnv: AWS Secrets Manager not wired yet — set AWS_SECRETS_MOCK=true for local dev")
	}
	path := os.Getenv("LOCAL_SECRETS_PATH")
	if path == "" {
		return nil, fmt.Errorf("secrets.NewFromEnv: LOCAL_SECRETS_PATH must be set when AWS_SECRETS_MOCK=true")
	}
	return NewMockProvider(path), nil
}
