package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all environment-driven configuration for auth-service.
// Loaded once at startup via Load() and passed through the app via dependency injection.
type Config struct {
	// Server
	Port string

	// Database
	DatabaseURL string

	// JWT
	JWTSecret      string
	JWTExpiryHours int
}

// Load reads the .env file (if present) and populates a Config struct.
// Falls back to real environment variables if .env is absent (production behaviour).
func Load() (*Config, error) {
	// Try to load .env from the project root (two levels up from this service).
	// Silently ignore if the file doesn't exist — in production, vars come from the env directly.
	_ = godotenv.Load("../../.env")

	cfg := &Config{
		Port:        getEnv("AUTH_SERVICE_PORT", "9006"),
		DatabaseURL: getEnv("DATABASE_URL", ""),
		JWTSecret:   getEnv("JWT_SECRET", ""),
	}

	// Validate required fields
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required but not set")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required but not set")
	}

	hours, err := strconv.Atoi(getEnv("JWT_EXPIRY_HOURS", "24"))
	if err != nil {
		return nil, fmt.Errorf("JWT_EXPIRY_HOURS must be a valid integer: %w", err)
	}
	cfg.JWTExpiryHours = hours

	return cfg, nil
}

// getEnv returns the value of an environment variable or a fallback default.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
