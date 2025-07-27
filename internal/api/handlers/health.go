package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler handles health-related endpoints
type HealthHandler struct {
	WorkerID string
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(workerID string) *HealthHandler {
	return &HealthHandler{
		WorkerID: workerID,
	}
}

// @Summary Health check
// @Description Check if the worker is healthy and responsive
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /health [get]
func (h *HealthHandler) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"worker_id": h.WorkerID,
		"timestamp": time.Now().Unix(),
	})
}

// @Summary Worker information
// @Description Get basic worker information and status
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router / [get]
func (h *HealthHandler) WorkerInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"worker_id": h.WorkerID,
		"status":    "running",
		"timestamp": time.Now().Unix(),
		"version":   "1.0.0",
		"capabilities": []string{
			"rtsp_processing",
			"webrtc_streaming",
			"ai_detection",
		},
	})
}
