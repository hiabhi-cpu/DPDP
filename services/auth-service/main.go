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
	db "github.com/hiabhi-cpu/DPDP/auth-service/db/sqlc"
	"github.com/hiabhi-cpu/DPDP/auth-service/internal/handler"
	"github.com/hiabhi-cpu/DPDP/auth-service/internal/repository"
	"github.com/hiabhi-cpu/DPDP/auth-service/internal/service"
)

func main() {
	// ─── 1. Load config from .env ──────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	// ─── 2. Connect to PostgreSQL via pgx connection pool ─────────────────────
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ Database ping failed: %v", err)
	}
	log.Println("✅ Connected to PostgreSQL")

	// ─── 3. Wire layers (Dependency Injection) ─────────────────────────────────
	// SQLC queries → Repository → Service → Handler
	queries := db.New(pool)

	userRepo := repository.NewUserRepository(queries)
	tokenRepo := repository.NewTokenRepository(queries)

	authSvc := service.NewAuthService(userRepo, tokenRepo, cfg)
	authHandler := handler.NewAuthHandler(authSvc)

	// ─── 4. Set up Gin router ──────────────────────────────────────────────────
	r := gin.Default()
	handler.SetupRoutes(r, authHandler)

	// ─── 5. Start server with graceful shutdown ────────────────────────────────
	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	// Run server in background goroutine
	go func() {
		log.Printf("🚀 auth-service running on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server error: %v", err)
		}
	}()

	// Block until OS signal received (Ctrl+C or SIGTERM from Docker/k8s)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down auth-service...")

	// Give in-flight requests up to 5s to complete
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("❌ Forced shutdown: %v", err)
	}

	log.Println("✅ auth-service stopped cleanly")
}
