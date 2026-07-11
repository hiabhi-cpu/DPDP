package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/consent-service/bootstrap"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/controller"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/outbox"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/service"
	"github.com/hiabhi-cpu/consent-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/logging"
	"github.com/hiabhi-cpu/shared/middleware"
	"github.com/hiabhi-cpu/shared/secrets"
	"github.com/hiabhi-cpu/shared/serviceauth"
)

// serviceName identifies this service when requesting a service token.
const serviceName = "consent-service"

func main() {
	logging.Setup("consent-service")

	ctx := context.Background()

	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("consent-service: failed to load JWT public key: %v", err)
	}

	secretsProvider, err := secrets.NewFromEnv()
	if err != nil {
		log.Fatalf("consent-service: failed to initialize secrets provider: %v", err)
	}

	repo := repository.New(db)

	// Service-token client shared by the audit shipper and the OTP session
	// verifier (both call /internal endpoints on sibling services).
	tokens := serviceauth.NewClient(env.AuthServiceURL, serviceName, env.ServiceTokenSecret)
	sessions := service.NewSessionVerifier(env.NotificationServiceURL, tokens)

	svc := service.NewConsentService(repo, secretsProvider, sessions)
	handler := controller.NewConsentHandler(svc)

	// Audit relay: ships transactionally-queued audit events to audit-service
	// using a cached service token from auth-service.
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
		log.Printf("consent-service listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("consent-service: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("consent-service: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("consent-service: forced shutdown: %v", err)
	}
	log.Println("consent-service: stopped")
}
