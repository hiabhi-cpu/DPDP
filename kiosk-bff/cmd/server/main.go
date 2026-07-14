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

	"github.com/hiabhi-cpu/kiosk-bff/bootstrap"
	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
	"github.com/hiabhi-cpu/kiosk-bff/pkg/routes"
	"github.com/hiabhi-cpu/shared/hospitaljwt"
	"github.com/hiabhi-cpu/shared/logging"
)

func main() {
	logging.Setup("kiosk-bff")

	env := bootstrap.NewEnv()
	tokens := hospitaljwt.NewHospitalTokenClient(env.AuthServiceURL, env.HospitalAPIKey)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery(), gin.Logger())
	routes.Setup(r, routes.Deps{
		Claim: handlers.NewClaimHandler(
			env.NotificationServiceURL, env.IntegrationServiceURL, env.ConsentServiceURL, tokens),
		StaticDir: env.StaticDir,
	})

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{Addr: addr, Handler: r, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("kiosk-bff listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("kiosk-bff: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("kiosk-bff: stopped")
}
