package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload" // auto-loads .env file

	"github.com/golang-jwt/jwt/v5"
	"github.com/hiabhi-cpu/auth-service/bootstrap"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/controller"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/repository"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/service"
	"github.com/hiabhi-cpu/auth-service/pkg/routes"
)

func main() {
	ctx := context.Background()

	// ── 1. Load configuration ─────────────────────────────────────────────────
	env := bootstrap.NewEnv()

	// ── 2. Connect to PostgreSQL ──────────────────────────────────────────────
	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()

	// ── 3. Load RSA keys ──────────────────────────────────────────────────────
	privKeyData, err := os.ReadFile(env.JWTPrivateKeyPath)
	if err != nil {
		log.Fatalf("auth-service: failed to read private key from %q: %v", env.JWTPrivateKeyPath, err)
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privKeyData)
	if err != nil {
		log.Fatalf("auth-service: failed to parse RSA private key: %v", err)
	}

	// ── 4. Wire dependencies ─────────────────────────────────────────────────
	repo := repository.New(db)
	svc := service.New(repo, privateKey, env.JWTExpiryHours, env.ServiceTokenSecret, env.ServiceTokenExpiry)
	handler := controller.New(svc)

	// ── 5. Set up Gin router ─────────────────────────────────────────────────
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Trust no proxy by default: ClientIP() falls back to the socket address,
	// so a client cannot spoof the audit-trail IP via X-Forwarded-For. When a
	// load balancer fronts the service, list its CIDR here instead.
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	routes.Setup(r, handler)

	// ── 6. Start HTTP server with graceful shutdown ───────────────────────────
	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("auth-service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("auth-service: server error: %v", err)
		}
	}()

	// Wait for interrupt signal (Ctrl+C or SIGTERM from Docker/k8s)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("auth-service: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("auth-service: forced shutdown: %v", err)
	}
	log.Println("auth-service: stopped")
}
