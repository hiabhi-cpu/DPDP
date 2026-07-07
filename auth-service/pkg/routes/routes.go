package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/controller"
)

// Setup registers all routes for the auth-service onto the given Gin engine.
func Setup(r *gin.Engine, authHandler *controller.AuthHandler) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "auth-service"})
	})

	v1 := r.Group("/v1")
	{
		auth := v1.Group("/auth")
		{
			auth.POST("/token", authHandler.IssueToken)
			auth.POST("/service-token", authHandler.IssueServiceToken)
		}
	}
}
