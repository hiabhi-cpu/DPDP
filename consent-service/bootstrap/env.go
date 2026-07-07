package bootstrap

import (
	"fmt"
	"os"
)

// Env holds all configuration for consent-service loaded from environment variables.
type Env struct {
	Port                   string
	DatabaseURL            string
	JWTPublicKeyPath       string
	AuditServiceURL        string
	NotificationServiceURL string
	AuthServiceURL         string
	ServiceTokenSecret     string
}

// NewEnv loads and validates all required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:                   mustGet("CONSENT_SERVICE_PORT"),
		DatabaseURL:            mustGet("DATABASE_URL"),
		JWTPublicKeyPath:       mustGet("JWT_PUBLIC_KEY_PATH"),
		AuditServiceURL:        mustGet("AUDIT_SERVICE_URL"),
		NotificationServiceURL: mustGet("NOTIFICATION_SERVICE_URL"),
		AuthServiceURL:         mustGet("AUTH_SERVICE_URL"),
		ServiceTokenSecret:     mustGet("SERVICE_TOKEN_SECRET"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
