package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// Setup registers all routes for the audit-service onto the given Gin engine.
func Setup(r *gin.Engine, auditHandler *controller.AuditHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "audit-service"})
	})

	internal := r.Group("/internal")
	internal.Use(middleware.InternalServiceAuth(pubKey))
	{
		audit := internal.Group("/audit")
		{
			// Requires a valid auth-service-issued service token (role=service).
			// Consent-service's audit relay calls this.
			audit.POST("/log", auditHandler.LogEvent)
		}
	}

	api := r.Group("/api")
	api.Use(middleware.JWTAuth(pubKey))
	{
		v1 := api.Group("/v1")
		{
			audit := v1.Group("/audit")
			{
				// Fetches logs for the hospital identified by the JWT.
				audit.GET("/logs", auditHandler.GetLogs)
			}
		}
	}
}
