package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// Setup registers all routes for the emergency-service onto the given Gin engine.
func Setup(r *gin.Engine, h *controller.EmergencyHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "emergency-service"})
	})

	api := r.Group("/api")
	api.Use(middleware.JWTAuth(pubKey))
	{
		v1 := api.Group("/v1")
		{
			// The override lives under /consent to match the product plan's
			// documented path (POST /v1/consent/emergency-override).
			v1.POST("/consent/emergency-override", h.Override)

			emergency := v1.Group("/emergency")
			{
				emergency.GET("/pending", h.Pending)
				emergency.POST("/:id/review", h.Review)
			}
		}
	}
}
