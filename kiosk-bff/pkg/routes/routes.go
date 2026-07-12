package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
		// Serve built assets, index.html, and Vite public/ passthrough files
		// (manifest.webmanifest, favicon.svg, ...) under /kiosk/. Any request
		// under /kiosk that maps to a real file in StaticDir gets that file;
		// everything else (client routes) falls back to index.html so SPA
		// routing works.
		staticDir, err := filepath.Abs(d.StaticDir)
		if err != nil {
			staticDir = d.StaticDir
		}
		index := filepath.Join(staticDir, "index.html")
		serveIndex := func(c *gin.Context) { c.File(index) }
		serve := func(c *gin.Context) {
			rel := strings.TrimPrefix(c.Request.URL.Path, "/kiosk")
			candidate := filepath.Join(staticDir, rel) // filepath.Join cleans the result
			// ponytail: prefix check on the cleaned absolute path is the guard —
			// blocks /kiosk/../../etc/passwd-style escapes from StaticDir.
			if candidate == staticDir || strings.HasPrefix(candidate, staticDir+string(filepath.Separator)) {
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
					c.File(candidate)
					return
				}
			}
			serveIndex(c)
		}
		r.NoRoute(func(c *gin.Context) {
			if c.Request.Method == http.MethodGet &&
				(strings.HasPrefix(c.Request.URL.Path, "/kiosk/") || c.Request.URL.Path == "/kiosk") {
				serve(c)
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		})
	}
}
