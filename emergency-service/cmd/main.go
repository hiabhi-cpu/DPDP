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

	"github.com/hiabhi-cpu/emergency-service/bootstrap"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/controller"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/outbox"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/repository"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/service"
	"github.com/hiabhi-cpu/emergency-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/middleware"
	"github.com/hiabhi-cpu/shared/secrets"
	"github.com/hiabhi-cpu/shared/serviceauth"
)

// serviceName identifies this service when requesting a service token.
const serviceName = "emergency-service"

func main() {
	ctx := context.Background()

	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("emergency-service: failed to load JWT public key: %v", err)
	}

	secretsProvider, err := secrets.NewFromEnv()
	if err != nil {
		log.Fatalf("emergency-service: failed to initialize secrets provider: %v", err)
	}

	repo := repository.New(db)
	svc := service.NewEmergencyService(repo, secretsProvider)
	handler := controller.NewEmergencyHandler(svc)

	// Audit relay: ships transactionally-queued audit events to audit-service
	// using a cached service token from auth-service.
	tokens := serviceauth.NewClient(env.AuthServiceURL, serviceName, env.ServiceTokenSecret)
	shipper := service.NewAuditShipper(env.AuditServiceURL, tokens)
	relay := outbox.NewRelay(repo, shipper, 2*time.Second, 100)

	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	go relay.Run(relayCtx)

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
		log.Printf("emergency-service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("emergency-service: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("emergency-service: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("emergency-service: forced shutdown: %v", err)
	}
	log.Println("emergency-service: stopped")
}
