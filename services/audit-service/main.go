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
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"github.com/hiabhi-cpu/DPDP/audit-service/handlers"
	"github.com/hiabhi-cpu/DPDP/audit-service/service"
)

func main() {
	_ = godotenv.Load()

	dbURL := requireEnv("DATABASE_URL")
	port := getEnv("AUDIT_SERVICE_PORT", "9001")

	// ── Connect to PostgreSQL via sqlx ────────────────────────────────────────
	db, err := sqlx.Connect("postgres", dbURL)
	if err != nil {
		log.Fatalf("❌ audit-service: cannot connect to database: %v", err)
	}
	defer db.Close()
	log.Println("✅ audit-service: connected to PostgreSQL")

	// ── Wire dependencies ─────────────────────────────────────────────────────
	auditSvc := service.NewAuditService(db)
	auditHandler := handlers.NewAuditHandler(auditSvc)

	// ── Build router ──────────────────────────────────────────────────────────
	r := gin.Default()
	r.GET("/health", handlers.HealthHandler)

	// Internal endpoint — called by other services only (no public exposure)
	internal := r.Group("/internal")
	{
		internal.POST("/audit/log", auditHandler.Log)
	}

	// Public (hospital-scoped, requires JWT from other services) — Phase 1
	v1 := r.Group("/v1")
	{
		v1.GET("/audit/logs", auditHandler.GetLogs)
	}

	// ── Start server with graceful shutdown ───────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("🚀 audit-service running on :%s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ audit-service: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx) //nolint:errcheck
	log.Println("✅ audit-service stopped cleanly")
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
		log.Fatalf("❌ audit-service: required env var %q is not set", key)
	}
	return v
}
