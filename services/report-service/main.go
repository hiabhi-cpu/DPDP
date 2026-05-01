package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "report-service",
			"status":  "ok",
			"port":    9005,
		})
	})

	r.Run(":9005")
}
