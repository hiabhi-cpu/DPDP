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
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/DPDP/notification-service/handlers"
	notifservice "github.com/hiabhi-cpu/DPDP/notification-service/service"
	"github.com/hiabhi-cpu/DPDP/notification-service/store"
	sharedsecrets "github.com/hiabhi-cpu/DPDP/shared/secrets"
)

func main() {
	_ = godotenv.Load()

	port := getEnv("NOTIFICATION_SERVICE_PORT", "9004")
	dbURL := requireEnv("DATABASE_URL")
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379/0")
	msg91AuthKey := getEnv("MSG91_AUTH_KEY", "mock")
	msg91SenderID := getEnv("MSG91_SENDER_ID", "DPDPCM")
	msg91TemplateID := getEnv("MSG91_TEMPLATE_ID", "")
	systemSalt := requireEnv("SYSTEM_SALT")
	localSecretsPath := getEnv("LOCAL_SECRETS_PATH", "./secrets/local_hospital_keys.json")
	awsMock := getEnv("AWS_SECRETS_MOCK", "true") == "true"

	// ── PostgreSQL ────────────────────────────────────────────────────────────
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("❌ notification-service: DB connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ notification-service: DB ping: %v", err)
	}
	log.Println("✅ notification-service: connected to PostgreSQL")

	// ── Redis ─────────────────────────────────────────────────────────────────
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("❌ notification-service: invalid REDIS_URL: %v", err)
	}
	redisClient := redis.NewClient(redisOpts)
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("❌ notification-service: Redis ping failed: %v", err)
	}
	log.Println("✅ notification-service: connected to Redis")
	defer redisClient.Close()

	// ── Secrets provider ──────────────────────────────────────────────────────
	var secretsProvider sharedsecrets.Provider
	if awsMock {
		secretsProvider, err = sharedsecrets.NewMockProvider(systemSalt, localSecretsPath)
	} else {
		secretsProvider, err = sharedsecrets.NewAWSProvider(context.Background())
	}
	if err != nil {
		log.Fatalf("❌ notification-service: secrets provider: %v", err)
	}

	// ── SMS client (mock or real MSG91) ───────────────────────────────────────
	var smsClient notifservice.SMSClient
	if msg91AuthKey == "mock" {
		log.Println("⚠️  notification-service: using MOCK SMS client — OTPs will print to stdout")
		smsClient = notifservice.NewMockSMSClient()
	} else {
		smsClient = notifservice.NewMSG91Client(msg91AuthKey, msg91SenderID, msg91TemplateID)
		log.Println("✅ notification-service: using MSG91 SMS client")
	}

	// ── Wire dependencies ─────────────────────────────────────────────────────
	redisStore := store.NewRedisOTPStore(redisClient)
	otpSvc := notifservice.NewOTPService(redisStore, smsClient, secretsProvider, pool)
	otpHandler := handlers.NewOTPHandler(otpSvc)

	// ── Router ────────────────────────────────────────────────────────────────
	r := gin.Default()
	r.GET("/health", handlers.HealthHandler)

	v1 := r.Group("/v1")
	{
		v1.POST("/otp/send", otpHandler.SendOTP)
		v1.POST("/otp/verify", otpHandler.VerifyOTP)
	}

	// ── Start server ──────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("🚀 notification-service running on :%s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ notification-service: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx) //nolint:errcheck
	log.Println("✅ notification-service stopped cleanly")
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
		log.Fatalf("❌ notification-service: required env var %q is not set", key)
	}
	return v
}
