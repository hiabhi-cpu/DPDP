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

	"github.com/hiabhi-cpu/admin-bff/bootstrap"
	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/handlers"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	"github.com/hiabhi-cpu/admin-bff/pkg/routes"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
	"github.com/hiabhi-cpu/shared/hospitaljwt"
	"github.com/hiabhi-cpu/shared/logging"
)

func main() {
	logging.Setup("admin-bff")

	ctx := context.Background()
	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()
	rdb := bootstrap.NewRedis(ctx, env.RedisURL)
	defer rdb.Close()

	cookieCfg := httpx.CookieConfig{Secure: env.CookieSecure}
	users := auth.NewUserRepository(db)
	store := session.NewRedisStore(rdb, env.SessionTTL)
	tokens := hospitaljwt.NewHospitalTokenClient(env.AuthServiceURL, env.HospitalAPIKey)

	deps := routes.Deps{
		Auth:      handlers.NewAuthHandler(users, store, env.SessionTTL, cookieCfg),
		Store:     store,
		Cookie:    cookieCfg,
		Consent:   handlers.NewProxy(env.ConsentServiceURL, tokens),
		Audit:     handlers.NewProxy(env.AuditServiceURL, tokens),
		Emergency: handlers.NewProxy(env.EmergencyServiceURL, tokens),
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery(), gin.Logger())
	routes.Setup(r, deps)

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{Addr: addr, Handler: r, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("admin-bff listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin-bff: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("admin-bff: stopped")
}
