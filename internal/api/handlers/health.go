package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type HealthHandler struct {
	WorkerID string
}

func NewHealthHandler(workerID string) *HealthHandler {
	return &HealthHandler{WorkerID: workerID}
}

type HealthResponse struct {
	Status   string `json:"status" example:"healthy"`
	WorkerID string `json:"worker_id" example:"worker-1"`
}

type WorkerInfoResponse struct {
	WorkerID     string   `json:"worker_id" example:"worker-1"`
	Status       string   `json:"status" example:"running"`
	Version      string   `json:"version" example:"1.0.0"`
	Capabilities []string `json:"capabilities"`
}

// @Summary Health check
// @Description Check if the worker is healthy and responsive
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} HealthResponse
// @Router /health [get]
func (h *HealthHandler) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{
		Status:   "healthy",
		WorkerID: h.WorkerID,
	})
}

// @Summary Worker information
// @Description Get basic worker information and capabilities
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} WorkerInfoResponse
// @Router / [get]
func (h *HealthHandler) WorkerInfo(c *gin.Context) {
	c.JSON(http.StatusOK, WorkerInfoResponse{
		WorkerID: h.WorkerID,
		Status:   "running",
		Version:  "1.0.0",
		Capabilities: []string{
			"rtsp_processing",
			"webrtc_streaming",
			"ai_detection",
		},
	})
}
