package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// SetupInternal registers the hospital-JWT-scoped read API on the internal engine.
func SetupInternal(r *gin.Engine, read *controller.ReadHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "integration-service"})
	})
	grp := r.Group("/internal/v1")
	grp.Use(middleware.JWTAuth(pubKey)) // sets middleware.CtxHospitalID from the hospital JWT
	{
		grp.GET("/registrations", read.List)
		grp.GET("/registrations/:hms_patient_id", read.Get)
	}
}

// SetupWebhook registers the mTLS webhook on the webhook engine.
func SetupWebhook(r *gin.Engine, webhook *controller.WebhookHandler) {
	r.POST("/webhook/patient-registered", webhook.PatientRegistered)
}
