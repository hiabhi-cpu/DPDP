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

	"github.com/hiabhi-cpu/notification-service/bootstrap"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/controller"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/service"
	"github.com/hiabhi-cpu/notification-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/middleware"
)

func main() {
	ctx := context.Background()

	env := bootstrap.NewEnv()

	redisClient := bootstrap.NewRedis(ctx, env.RedisURL)
	defer redisClient.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("notification-service: failed to load JWT public key: %v", err)
	}

	repo := repository.NewRedisStore(redisClient)
	
	var smsClient service.SMSClient
	if env.SMSProvider == "mock" {
		smsClient = service.NewMockSMSClient()
	} else {
		// Example MSG91 initialization
		smsClient = service.NewMockSMSClient() // replace with real client
	}

	svc := service.NewOTPService(repo, smsClient)
	handler := controller.NewOTPHandler(svc)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Trust no proxy by default: ClientIP() falls back to the socket address,
	// so a client cannot spoof the audit-trail IP via X-Forwarded-For. When a
	// load balancer fronts the service, list its CIDR here instead.
	_ = r.SetTrustedProxies(nil)
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
		log.Printf("notification-service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("notification-service: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("notification-service: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("notification-service: forced shutdown: %v", err)
	}
	log.Println("notification-service: stopped")
}
