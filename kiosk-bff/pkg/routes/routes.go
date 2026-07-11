package routes

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
)

// Deps bundles what the routes need.
type Deps struct {
	OTP       *handlers.Proxy
	Consent   *handlers.Proxy
	StaticDir string // built PWA; empty disables static serving (dev uses the Vite server)
}

// Setup registers all kiosk-bff routes.
func Setup(r *gin.Engine, d Deps) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "kiosk-bff"})
	})

	api := r.Group("/kiosk/api")
	{
		api.POST("/otp/send", func(c *gin.Context) { d.OTP.ForwardPost(c, "/api/v1/otp/send") })
		api.POST("/otp/verify", func(c *gin.Context) { d.OTP.ForwardPost(c, "/api/v1/otp/verify") })
		api.POST("/consent/capture", func(c *gin.Context) { d.Consent.ForwardPost(c, "/api/v1/consent/capture") })
	}

	if d.StaticDir != "" {
		// Serve built assets and index.html under /kiosk/. SPA fallback: any
		// unmatched /kiosk/* GET returns index.html so client routing works.
		r.Static("/kiosk/assets", filepath.Join(d.StaticDir, "assets"))
		index := filepath.Join(d.StaticDir, "index.html")
		serveIndex := func(c *gin.Context) { c.File(index) }
		r.GET("/kiosk", serveIndex)
		r.NoRoute(func(c *gin.Context) {
			if c.Request.Method == http.MethodGet &&
				len(c.Request.URL.Path) >= 6 && c.Request.URL.Path[:6] == "/kiosk" {
				serveIndex(c)
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		})
	}
}
