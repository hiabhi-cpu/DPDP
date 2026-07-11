package bootstrap

import (
	"fmt"
	"os"
)

// Env holds all configuration for kiosk-bff loaded from environment variables.
type Env struct {
	Port                   string
	HospitalAPIKey         string // raw hospital API key — server-side secret, never sent to the browser
	AuthServiceURL         string
	NotificationServiceURL string
	ConsentServiceURL      string
	StaticDir              string // path to the built PWA; empty disables static serving
}

// NewEnv loads and validates required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:                   mustGet("KIOSK_BFF_PORT"),
		HospitalAPIKey:         mustGet("HOSPITAL_API_KEY"),
		AuthServiceURL:         mustGet("AUTH_SERVICE_URL"),
		NotificationServiceURL: mustGet("NOTIFICATION_SERVICE_URL"),
		ConsentServiceURL:      mustGet("CONSENT_SERVICE_URL"),
		StaticDir:              os.Getenv("STATIC_DIR"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
