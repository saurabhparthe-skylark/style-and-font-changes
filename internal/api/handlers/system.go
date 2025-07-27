package handlers

import (
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

// SystemHandler handles system-related endpoints
type SystemHandler struct {
	WorkerID string
}

// NewSystemHandler creates a new system handler
func NewSystemHandler(workerID string) *SystemHandler {
	return &SystemHandler{
		WorkerID: workerID,
	}
}

// @Summary Get system stats
// @Description Get system statistics and performance metrics
// @Tags system
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /system/stats [get]
func (h *SystemHandler) GetStats(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"stats": gin.H{
			"worker_id":  h.WorkerID,
			"uptime":     time.Now().Unix(),
			"memory_mb":  m.Alloc / 1024 / 1024,
			"cpu_cores":  runtime.NumCPU(),
			"goroutines": runtime.NumGoroutine(),
			"go_version": runtime.Version(),
		},
		"timestamp": time.Now().Unix(),
	})
}

// @Summary Get debug info
// @Description Get debug information for troubleshooting
// @Tags system
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /system/debug [get]
func (h *SystemHandler) GetDebugInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"debug": gin.H{
			"worker_id":  h.WorkerID,
			"endpoints":  []string{"/health", "/cameras", "/webrtc", "/system"},
			"components": []string{"camera_manager", "webrtc_publisher"},
		},
		"timestamp": time.Now().Unix(),
	})
}
