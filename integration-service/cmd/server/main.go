package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/integration-service/bootstrap"
	"github.com/hiabhi-cpu/integration-service/pkg/pending/controller"
	"github.com/hiabhi-cpu/integration-service/pkg/pending/repository"
	"github.com/hiabhi-cpu/integration-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/logging"
	"github.com/hiabhi-cpu/shared/middleware"
)

func main() {
	logging.Setup("integration-service")
	ctx := context.Background()
	env := bootstrap.NewEnv()

	redisClient := bootstrap.NewRedis(ctx, env.RedisURL)
	defer redisClient.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("integration-service: failed to load JWT public key: %v", err)
	}

	store := repository.NewRedisStore(redisClient)
	webhookHandler := controller.NewWebhookHandler(store)
	readHandler := controller.NewReadHandler(store)

	gin.SetMode(gin.ReleaseMode)

	// Internal read API (normal HTTP; hospital-JWT auth).
	internal := gin.New()
	_ = internal.SetTrustedProxies(nil)
	internal.Use(gin.Recovery(), gin.Logger())
	routes.SetupInternal(internal, readHandler, pubKey, redisClient)
	internalSrv := &http.Server{
		Addr: ":" + env.InternalPort, Handler: internal,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	// mTLS webhook listener (own port; client cert required + verified).
	webhook := gin.New()
	_ = webhook.SetTrustedProxies(nil)
	webhook.Use(gin.Recovery(), gin.Logger())
	routes.SetupWebhook(webhook, webhookHandler)
	tlsCfg, err := mtlsConfig(env)
	if err != nil {
		log.Fatalf("integration-service: mTLS config: %v", err)
	}
	webhookSrv := &http.Server{
		Addr: ":" + env.WebhookPort, Handler: webhook, TLSConfig: tlsCfg,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("integration-service: internal API on :%s", env.InternalPort)
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("integration-service: internal server error: %v", err)
		}
	}()
	go func() {
		log.Printf("integration-service: mTLS webhook on :%s", env.WebhookPort)
		// Certs are already in TLSConfig, so pass empty paths.
		if err := webhookSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("integration-service: webhook server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("integration-service: shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = internalSrv.Shutdown(shutdownCtx)
	_ = webhookSrv.Shutdown(shutdownCtx)
	log.Println("integration-service: stopped")
}

// mtlsConfig loads the server cert and the hospital CA, requiring a verified
// client cert on every webhook connection.
func mtlsConfig(env *bootstrap.Env) (*tls.Config, error) {
	serverCert, err := tls.LoadX509KeyPair(env.ServerCertPath, env.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	caPEM, err := os.ReadFile(env.HospitalCAPath)
	if err != nil {
		return nil, fmt.Errorf("read hospital CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("hospital CA PEM contained no certs")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
