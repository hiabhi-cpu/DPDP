package bootstrap

import (
	"fmt"
	"os"
)

// Env holds integration-service configuration from environment variables.
type Env struct {
	InternalPort     string // internal read API (hospital-JWT auth)
	WebhookPort      string // mTLS webhook listener
	RedisURL         string
	JWTPublicKeyPath string // verifies hospital JWTs on /internal
	ServerCertPath   string // mTLS server cert
	ServerKeyPath    string // mTLS server key
	HospitalCAPath   string // CA that signs hospital client certs
}

func NewEnv() *Env {
	return &Env{
		InternalPort:     mustGet("INTEGRATION_SERVICE_PORT"),
		WebhookPort:      mustGet("INTEGRATION_WEBHOOK_PORT"),
		RedisURL:         mustGet("REDIS_URL"),
		JWTPublicKeyPath: mustGet("JWT_PUBLIC_KEY_PATH"),
		ServerCertPath:   mustGet("MTLS_SERVER_CERT"),
		ServerKeyPath:    mustGet("MTLS_SERVER_KEY"),
		HospitalCAPath:   mustGet("MTLS_HOSPITAL_CA"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
