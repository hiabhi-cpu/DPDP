package bootstrap

import (
	"fmt"
	"os"
)

// Env holds all configuration for notification-service loaded from environment variables.
type Env struct {
	Port             string
	RedisURL         string
	JWTPublicKeyPath string
	SMSProvider      string
	MSG91AuthKey     string
	MSG91TemplateID  string
}

// NewEnv loads and validates all required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:             mustGet("NOTIFICATION_SERVICE_PORT"),
		RedisURL:         mustGet("REDIS_URL"),
		JWTPublicKeyPath: mustGet("JWT_PUBLIC_KEY_PATH"),
		SMSProvider:      getOrDefault("SMS_PROVIDER", "mock"),
		MSG91AuthKey:     getOrDefault("MSG91_AUTH_KEY", "mock"),
		MSG91TemplateID:  getOrDefault("MSG91_TEMPLATE_ID", "mock"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}

func getOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
