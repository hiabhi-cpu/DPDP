package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/handlers"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// Deps bundles what the routes need.
type Deps struct {
	Auth      *handlers.AuthHandler
	Store     session.Store
	Cookie    httpx.CookieConfig
	Consent   *handlers.Proxy
	Audit     *handlers.Proxy
	Emergency *handlers.Proxy

	Integration *handlers.Proxy
	Reception   *handlers.ReceptionHandler
}

// Setup registers all BFF routes.
func Setup(r *gin.Engine, d Deps) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "admin-bff"})
	})

	api := r.Group("/api")
	api.Use(httpx.CSRF(d.Cookie))
	{
		// Public auth endpoints. GET /csrf seeds the double-submit token so the
		// SPA can send X-CSRF-Token on its first mutating request (login).
		api.GET("/csrf", d.Auth.CSRFToken)
		api.POST("/session", d.Auth.Login)
		api.DELETE("/session", d.Auth.Logout)

		// Authenticated endpoints.
		authed := api.Group("")
		authed.Use(bffmw.RequireSession(d.Store))
		{
			authed.GET("/me", d.Auth.Me)

			staff := authed.Group("")
			staff.Use(bffmw.RequireRole("admin", "dpo"))
			{
				staff.GET("/consent/stats", func(c *gin.Context) { d.Consent.ForwardGet(c, "/api/v1/consent/stats") })
				staff.GET("/audit/logs", func(c *gin.Context) { d.Audit.ForwardGet(c, "/api/v1/audit/logs") })
				staff.GET("/emergency/pending", func(c *gin.Context) { d.Emergency.ForwardGet(c, "/api/v1/emergency/pending") })
				staff.POST("/emergency/:id/review", d.Emergency.ForwardReview)
			}

			reception := authed.Group("/reception")
			reception.Use(bffmw.RequireRole("reception"))
			{
				reception.GET("/registrations", func(c *gin.Context) { d.Integration.ForwardGet(c, "/internal/v1/registrations") })
				reception.POST("/registrations/:hms/send-code", d.Reception.SendCode)
			}
		}
	}
}
