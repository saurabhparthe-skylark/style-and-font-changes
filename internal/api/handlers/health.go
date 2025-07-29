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
