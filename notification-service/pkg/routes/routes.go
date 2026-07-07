package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// Setup registers all routes for the notification-service onto the given Gin engine.
func Setup(r *gin.Engine, otpHandler *controller.OTPHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "notification-service"})
	})

	api := r.Group("/api")
	api.Use(middleware.JWTAuth(pubKey))
	{
		v1 := api.Group("/v1")
		{
			otp := v1.Group("/otp")
			{
				otp.POST("/send", otpHandler.Send)
				otp.POST("/verify", otpHandler.Verify)
			}
		}
	}
}
