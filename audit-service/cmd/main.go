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
	_ "github.com/joho/godotenv/autoload"

	"github.com/hiabhi-cpu/audit-service/bootstrap"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/controller"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/repository"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/service"
	"github.com/hiabhi-cpu/audit-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/middleware"
)

func main() {
	ctx := context.Background()

	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("audit-service: failed to load JWT public key: %v", err)
	}

	repo := repository.New(db)
	svc := service.New(repo)
	handler := controller.New(svc)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	routes.Setup(r, handler, pubKey)

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("audit-service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("audit-service: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("audit-service: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("audit-service: forced shutdown: %v", err)
	}
	log.Println("audit-service: stopped")
}
