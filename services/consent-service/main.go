package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/hiabhi-cpu/DPDP/consent-service/handlers"
	"github.com/hiabhi-cpu/DPDP/consent-service/service"
	sharedsecrets "github.com/hiabhi-cpu/DPDP/shared/secrets"
)

func main() {
	_ = godotenv.Load()

	dbURL := requireEnv("DATABASE_URL")
	port := getEnv("CONSENT_SERVICE_PORT", "9000")
	auditURL := getEnv("AUDIT_SERVICE_URL", "http://localhost:9001")
	systemSalt := requireEnv("SYSTEM_SALT")
	localSecretsPath := getEnv("LOCAL_SECRETS_PATH", "./secrets/local_hospital_keys.json")
	awsMock := getEnv("AWS_SECRETS_MOCK", "true") == "true"

	// ── Connect to PostgreSQL ──────────────────────────────────────────────────
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("❌ consent-service: DB connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ consent-service: DB ping: %v", err)
	}
	log.Println("✅ consent-service: connected to PostgreSQL")

	// ── Secrets provider ──────────────────────────────────────────────────────
	var secretsProvider sharedsecrets.Provider
	if awsMock {
		secretsProvider, err = sharedsecrets.NewMockProvider(systemSalt, localSecretsPath)
	} else {
		secretsProvider, err = sharedsecrets.NewAWSProvider(context.Background())
	}
	if err != nil {
		log.Fatalf("❌ consent-service: secrets provider: %v", err)
	}

	// ── Wire dependencies ─────────────────────────────────────────────────────
	consentSvc := service.NewConsentService(pool, secretsProvider, auditURL)
	consentHandler := handlers.NewConsentHandler(consentSvc)

	// ── Build router ──────────────────────────────────────────────────────────
	r := gin.Default()
	r.GET("/health", handlers.HealthHandler)

	v1 := r.Group("/v1")
	{
		// All consent endpoints require a valid hospital JWT
		// JWT middleware reads public key from env (shared with auth-service)
		consent := v1.Group("/consent")
		{
			consent.POST("/capture", consentHandler.Capture)
			consent.GET("/check", consentHandler.Check)
			consent.POST("/withdraw", consentHandler.Withdraw)
		}
	}

	// ── Start server ──────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("🚀 consent-service running on :%s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ consent-service: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx) //nolint:errcheck
	log.Println("✅ consent-service stopped cleanly")
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
		log.Fatalf("❌ consent-service: required env var %q is not set", key)
	}
	return v
}
