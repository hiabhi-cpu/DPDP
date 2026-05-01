package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "auth-service",
			"status":  "ok",
			"port":    9006,
		})
	})

	r.Run(":9006")
}
