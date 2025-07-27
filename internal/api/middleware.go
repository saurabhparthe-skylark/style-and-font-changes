package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) setupMiddleware() {
	s.router.Use(gin.Recovery())

	s.router.Use(s.loggingMiddleware())

	s.router.Use(corsMiddleware())
}

func (s *Server) loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		// TODO: Use proper logger from worker
		// For now, just use gin's default logging
		_ = latency
		_ = status
		_ = method
		_ = path
	}
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
