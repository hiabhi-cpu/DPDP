package config

import (
	"crypto/rsa"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
)

// Config holds all configuration for the auth-service.
// Loaded once at startup — fail fast if anything is missing.
type Config struct {
	// Server
	Port string

	// Database
	DatabaseURL string

	// JWT RS256
	PrivateKey     *rsa.PrivateKey
	PublicKey      *rsa.PublicKey
	TokenExpiry    time.Duration
}

// Load reads environment variables (and optionally a .env file) and returns Config.
// Any missing required variable causes a fatal error — we fail fast at startup.
func Load() (*Config, error) {
	// Load .env if present (ignored if not found — production uses real env vars)
	_ = godotenv.Load()

	cfg := &Config{}

	// ── Server ────────────────────────────────────────────────────────────────
	cfg.Port = getEnv("AUTH_SERVICE_PORT", "9006")

	// ── Database ──────────────────────────────────────────────────────────────
	cfg.DatabaseURL = requireEnv("DATABASE_URL")

	// ── JWT RS256 Keys ────────────────────────────────────────────────────────
	privatePEM, err := os.ReadFile(requireEnv("JWT_PRIVATE_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("config: cannot read JWT private key: %w\n  Hint: run `make gen-keys` to generate RSA keys", err)
	}
	cfg.PrivateKey, err = jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("config: invalid JWT private key PEM: %w", err)
	}

	publicPEM, err := os.ReadFile(requireEnv("JWT_PUBLIC_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("config: cannot read JWT public key: %w", err)
	}
	cfg.PublicKey, err = jwt.ParseRSAPublicKeyFromPEM(publicPEM)
	if err != nil {
		return nil, fmt.Errorf("config: invalid JWT public key PEM: %w", err)
	}

	// ── Token expiry ──────────────────────────────────────────────────────────
	hoursStr := getEnv("JWT_EXPIRY_HOURS", "24")
	hours, err := strconv.Atoi(hoursStr)
	if err != nil {
		return nil, fmt.Errorf("config: invalid JWT_EXPIRY_HOURS %q: %w", hoursStr, err)
	}
	cfg.TokenExpiry = time.Duration(hours) * time.Hour

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("config: required environment variable %q is not set", key))
	}
	return v
}
