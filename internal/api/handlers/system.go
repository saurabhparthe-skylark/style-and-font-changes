package handlers

import (
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

type SystemHandler struct {
	WorkerID  string
	StartTime time.Time
}

func NewSystemHandler(workerID string) *SystemHandler {
	return &SystemHandler{
		WorkerID:  workerID,
		StartTime: time.Now(),
	}
}

type SystemStatsResponse struct {
	WorkerID   string `json:"worker_id" example:"worker-1"`
	Uptime     string `json:"uptime" example:"2h30m15s"`
	MemoryMB   uint64 `json:"memory_mb" example:"45"`
	CPUCores   int    `json:"cpu_cores" example:"8"`
	Goroutines int    `json:"goroutines" example:"25"`
	GoVersion  string `json:"go_version" example:"go1.21.0"`
}

// @Summary Get system statistics
// @Description Get system performance metrics and status
// @Tags system
// @Accept json
// @Produce json
// @Success 200 {object} SystemStatsResponse
// @Router /system/stats [get]
func (h *SystemHandler) GetStats(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	uptime := time.Since(h.StartTime)

	c.JSON(http.StatusOK, SystemStatsResponse{
		WorkerID:   h.WorkerID,
		Uptime:     uptime.String(),
		MemoryMB:   m.Alloc / 1024 / 1024,
		CPUCores:   runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
		GoVersion:  runtime.Version(),
	})
}
