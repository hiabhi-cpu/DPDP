package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Env holds all configuration for auth-service loaded from environment variables.
// Fail-fast: the service refuses to start if any required variable is missing.
type Env struct {
	Port               string
	DatabaseURL        string
	JWTPrivateKeyPath  string
	JWTPublicKeyPath   string
	JWTExpiryHours     time.Duration
	ServiceTokenSecret string
	ServiceTokenExpiry time.Duration
}

// NewEnv loads and validates all required environment variables.
// Panics immediately if any required variable is missing or invalid —
// we never want a misconfigured service to silently start and fail at runtime.
func NewEnv() *Env {
	e := &Env{
		Port:              mustGet("AUTH_SERVICE_PORT"),
		DatabaseURL:       mustGet("DATABASE_URL"),
		JWTPrivateKeyPath: mustGet("JWT_PRIVATE_KEY_PATH"),
		JWTPublicKeyPath:  mustGet("JWT_PUBLIC_KEY_PATH"),
	}

	hoursStr := getOrDefault("JWT_EXPIRY_HOURS", "24")
	hours, err := strconv.Atoi(hoursStr)
	if err != nil || hours <= 0 {
		panic(fmt.Sprintf("bootstrap: JWT_EXPIRY_HOURS must be a positive integer, got %q", hoursStr))
	}
	e.JWTExpiryHours = time.Duration(hours) * time.Hour

	e.ServiceTokenSecret = mustGet("SERVICE_TOKEN_SECRET")

	minsStr := getOrDefault("SERVICE_TOKEN_EXPIRY_MINUTES", "10")
	mins, err := strconv.Atoi(minsStr)
	if err != nil || mins <= 0 {
		panic(fmt.Sprintf("bootstrap: SERVICE_TOKEN_EXPIRY_MINUTES must be a positive integer, got %q", minsStr))
	}
	e.ServiceTokenExpiry = time.Duration(mins) * time.Minute

	return e
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
