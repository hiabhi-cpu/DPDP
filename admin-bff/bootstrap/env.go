package bootstrap

import (
	"fmt"
	"os"
	"time"
)

// Env holds all configuration for admin-bff loaded from environment variables.
type Env struct {
	Port                string
	DatabaseURL         string
	RedisURL            string
	HospitalAPIKey      string // raw hospital API key — server-side secret, never sent to the browser
	AuthServiceURL      string
	ConsentServiceURL   string
	AuditServiceURL     string
	EmergencyServiceURL string
	SessionTTL          time.Duration
	CookieSecure        bool   // false for local http dev, true in production
	StaticDir           string // path to built SPA; empty disables static serving
	AllowedOrigin       string // dev SPA origin for CSRF/allow checks, e.g. http://localhost:5173
}

// NewEnv loads and validates all required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:                mustGet("ADMIN_BFF_PORT"),
		DatabaseURL:         mustGet("DATABASE_URL"),
		RedisURL:            mustGet("REDIS_URL"),
		HospitalAPIKey:      mustGet("HOSPITAL_API_KEY"),
		AuthServiceURL:      mustGet("AUTH_SERVICE_URL"),
		ConsentServiceURL:   mustGet("CONSENT_SERVICE_URL"),
		AuditServiceURL:     mustGet("AUDIT_SERVICE_URL"),
		EmergencyServiceURL: mustGet("EMERGENCY_SERVICE_URL"),
		SessionTTL:          getDurationDefault("SESSION_TTL", 8*time.Hour),
		CookieSecure:        os.Getenv("COOKIE_SECURE") == "true",
		StaticDir:           os.Getenv("STATIC_DIR"),
		AllowedOrigin:       os.Getenv("ALLOWED_ORIGIN"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}

func getDurationDefault(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: %s must be a Go duration (e.g. 8h): %v", key, err))
	}
	return d
}
