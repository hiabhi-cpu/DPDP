package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// MockProvider reads hospital keys from a local JSON file and SYSTEM_SALT from the
// environment. Used when AWS_SECRETS_MOCK=true (local development only).
//
// File format (secrets/local_hospital_keys.json):
//
//	{
//	  "a1b2c3d4-e5f6-7890-abcd-ef1234567890": "hospital-specific-secret-key",
//	  ...
//	}
type MockProvider struct {
	systemSalt  string
	keysFilePath string

	mu          sync.RWMutex
	hospitalKeys map[string]string // hospitalID → key (loaded once, cached)
}

// NewMockProvider creates a MockProvider.
// systemSalt is read from SYSTEM_SALT env var.
// keysFilePath is the path to the local JSON keys file (LOCAL_SECRETS_PATH env var).
func NewMockProvider(systemSalt, keysFilePath string) (*MockProvider, error) {
	if systemSalt == "" {
		return nil, fmt.Errorf("secrets.MockProvider: SYSTEM_SALT env var is required")
	}
	if keysFilePath == "" {
		return nil, fmt.Errorf("secrets.MockProvider: LOCAL_SECRETS_PATH env var is required")
	}

	p := &MockProvider{
		systemSalt:   systemSalt,
		keysFilePath: keysFilePath,
	}

	// Load keys eagerly at startup — fail fast if file is missing
	if err := p.loadKeys(); err != nil {
		return nil, err
	}

	return p, nil
}

func (p *MockProvider) loadKeys() error {
	data, err := os.ReadFile(p.keysFilePath)
	if err != nil {
		return fmt.Errorf("secrets.MockProvider: failed to read keys file %q: %w", p.keysFilePath, err)
	}

	var keys map[string]string
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("secrets.MockProvider: invalid JSON in keys file: %w", err)
	}

	p.mu.Lock()
	p.hospitalKeys = keys
	p.mu.Unlock()

	return nil
}

// GetHospitalKey returns the hospital-specific key from the local JSON file.
func (p *MockProvider) GetHospitalKey(_ context.Context, hospitalID string) (string, error) {
	p.mu.RLock()
	key, ok := p.hospitalKeys[hospitalID]
	p.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("secrets.MockProvider: no key found for hospital %q — add it to %s", hospitalID, p.keysFilePath)
	}
	return key, nil
}

// GetSystemSalt returns the SYSTEM_SALT value loaded at startup.
func (p *MockProvider) GetSystemSalt(_ context.Context) (string, error) {
	return p.systemSalt, nil
}
