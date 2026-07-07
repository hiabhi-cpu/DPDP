package bootstrap

import (
	"fmt"
	"os"
)

// Env holds all configuration for audit-service loaded from environment variables.
type Env struct {
	Port             string
	DatabaseURL      string
	JWTPublicKeyPath string
}

// NewEnv loads and validates all required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:             mustGet("AUDIT_SERVICE_PORT"),
		DatabaseURL:      mustGet("DATABASE_URL"),
		JWTPublicKeyPath: mustGet("JWT_PUBLIC_KEY_PATH"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
