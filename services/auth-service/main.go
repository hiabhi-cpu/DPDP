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

	"github.com/hiabhi-cpu/DPDP/auth-service/config"
	"github.com/hiabhi-cpu/DPDP/auth-service/db"
	"github.com/hiabhi-cpu/DPDP/auth-service/handlers"
	"github.com/hiabhi-cpu/DPDP/auth-service/service"
)

func main() {
	// ── 1. Load config (fails fast if keys or env vars are missing) ────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ auth-service config error: %v", err)
	}

	// ── 2. Connect to PostgreSQL ───────────────────────────────────────────────
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("❌ auth-service: cannot connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ auth-service: database ping failed: %v", err)
	}
	log.Println("✅ auth-service: connected to PostgreSQL")

	// ── 3. Wire dependencies ───────────────────────────────────────────────────
	repo := db.NewHospitalRepository(pool)
	svc := service.NewAuthService(cfg.PrivateKey, cfg.PublicKey, cfg.TokenExpiry)
	authHandler := handlers.NewAuthHandler(svc, repo)

	// ── 4. Build router ────────────────────────────────────────────────────────
	r := gin.Default()

	// Health check (no auth required — used by load balancer / docker healthcheck)
	r.GET("/health", handlers.HealthHandler)

	// Public API: issue JWT from API key
	v1 := r.Group("/v1")
	{
		v1.POST("/auth/token", authHandler.IssueToken)
	}

	// ── 5. Start server with graceful shutdown ─────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("🚀 auth-service running on :%s\n", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ auth-service server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 auth-service: graceful shutdown...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("❌ auth-service: forced shutdown: %v", err)
	}
	log.Println("✅ auth-service stopped cleanly")
}
