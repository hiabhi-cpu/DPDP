package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// Setup registers all routes for the consent-service onto the given Gin engine.
func Setup(r *gin.Engine, consentHandler *controller.ConsentHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "consent-service"})
	})

	api := r.Group("/api")
	api.Use(middleware.JWTAuth(pubKey))
	{
		v1 := api.Group("/v1")
		{
			consent := v1.Group("/consent")
			{
				consent.POST("/capture", consentHandler.Capture)
				consent.POST("/check", consentHandler.Check)
				consent.POST("/withdraw", consentHandler.Withdraw)
				consent.POST("/grant", consentHandler.Grant)
				consent.GET("/stats", consentHandler.Stats)
			}
		}
	}
}
