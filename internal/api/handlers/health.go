package handlers

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services"
)

type HealthHandler struct {
	cfg       *config.Config
	container *services.ServiceContainer
	startTime time.Time
}

func NewHealthHandler(cfg *config.Config, container *services.ServiceContainer) *HealthHandler {
	return &HealthHandler{
		cfg:       cfg,
		container: container,
		startTime: time.Now(),
	}
}

type HealthResponse struct {
	Status    string    `json:"status"`
	WorkerID  string    `json:"worker_id"`
	Port      int       `json:"port"`
	Uptime    string    `json:"uptime"`
	Timestamp time.Time `json:"timestamp"`
}

type StatusResponse struct {
	WorkerID   string            `json:"worker_id"`
	Port       int               `json:"port"`
	ProcessID  int               `json:"process_id"`
	Status     string            `json:"status"`
	Uptime     string            `json:"uptime"`
	Memory     MemoryStats       `json:"memory"`
	Goroutines int               `json:"goroutines"`
	Cameras    CameraStats       `json:"cameras"`
	Services   map[string]string `json:"services"`
	Timestamp  time.Time         `json:"timestamp"`
}

type MemoryStats struct {
	Alloc      uint64 `json:"alloc"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
	NumGC      uint32 `json:"num_gc"`
}

type CameraStats struct {
	Active int `json:"active"`
	Total  int `json:"total"`
}

type MetricsResponse struct {
	WorkerID  string                 `json:"worker_id"`
	Metrics   map[string]interface{} `json:"metrics"`
	Timestamp time.Time              `json:"timestamp"`
}

type TestMessageResponse struct {
	Status     string    `json:"status"`
	WorkerID   string    `json:"worker_id"`
	Subject    string    `json:"subject"`
	Message    string    `json:"message"`
	Timestamp  time.Time `json:"timestamp"`
	NATSStatus string    `json:"nats_status"`
	MessageID  string    `json:"message_id"`
}

// Check godoc
// @Summary Health check
// @Description Get worker health status
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} HealthResponse
// @Router /health [get]
func (h *HealthHandler) Check(c *gin.Context) {
	uptime := time.Since(h.startTime).String()

	response := HealthResponse{
		Status:    "healthy",
		WorkerID:  h.cfg.WorkerID,
		Port:      h.cfg.Port,
		Uptime:    uptime,
		Timestamp: time.Now(),
	}

	c.JSON(http.StatusOK, response)
}

// Status godoc
// @Summary Detailed status
// @Description Get detailed worker status including memory and camera stats
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} StatusResponse
// @Router /status [get]
func (h *HealthHandler) Status(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	uptime := time.Since(h.startTime).String()

	// Camera Manager Health
	cameraStats := CameraStats{}
	if h.container.CameraManager != nil {
		active, total := h.container.CameraManager.GetStats()
		cameraStats.Active = active
		cameraStats.Total = total
	}

	// Service status
	services := map[string]string{
		"camera_manager":         getServiceStatus(h.container.CameraManager != nil),
		"postprocessing_service": getServiceStatus(h.container.PostProcessingSvc != nil),
		"message_service":        getServiceStatus(h.container.MessageSvc != nil),
	}

	response := StatusResponse{
		WorkerID:   h.cfg.WorkerID,
		Port:       h.cfg.Port,
		ProcessID:  runtime.GOMAXPROCS(0),
		Status:     "running",
		Uptime:     uptime,
		Goroutines: runtime.NumGoroutine(),
		Memory: MemoryStats{
			Alloc:      m.Alloc,
			TotalAlloc: m.TotalAlloc,
			Sys:        m.Sys,
			NumGC:      m.NumGC,
		},
		Cameras:   cameraStats,
		Services:  services,
		Timestamp: time.Now(),
	}

	c.JSON(http.StatusOK, response)
}

// Metrics godoc
// @Summary System metrics
// @Description Get system performance metrics
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} MetricsResponse
// @Router /metrics [get]
func (h *HealthHandler) Metrics(c *gin.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Force garbage collection for accurate memory stats
	runtime.GC()
	runtime.ReadMemStats(&m)

	metrics := map[string]interface{}{
		"memory": map[string]interface{}{
			"alloc_mb":         float64(m.Alloc) / 1024 / 1024,
			"total_alloc_mb":   float64(m.TotalAlloc) / 1024 / 1024,
			"sys_mb":           float64(m.Sys) / 1024 / 1024,
			"heap_alloc_mb":    float64(m.HeapAlloc) / 1024 / 1024,
			"heap_sys_mb":      float64(m.HeapSys) / 1024 / 1024,
			"heap_idle_mb":     float64(m.HeapIdle) / 1024 / 1024,
			"heap_inuse_mb":    float64(m.HeapInuse) / 1024 / 1024,
			"heap_released_mb": float64(m.HeapReleased) / 1024 / 1024,
			"stack_inuse_mb":   float64(m.StackInuse) / 1024 / 1024,
			"num_gc":           m.NumGC,
			"gc_cpu_fraction":  m.GCCPUFraction,
			"next_gc_mb":       float64(m.NextGC) / 1024 / 1024,
		},
		"runtime": map[string]interface{}{
			"goroutines": runtime.NumGoroutine(),
			"cgocalls":   runtime.NumCgoCall(),
			"gomaxprocs": runtime.GOMAXPROCS(0),
		},
		"uptime_seconds": time.Since(h.startTime).Seconds(),
	}

	// Add camera metrics if available
	if h.container.CameraManager != nil {
		active, total := h.container.CameraManager.GetStats()
		metrics["cameras"] = map[string]interface{}{
			"active": active,
			"total":  total,
		}
	}

	response := MetricsResponse{
		WorkerID:  h.cfg.WorkerID,
		Metrics:   metrics,
		Timestamp: time.Now(),
	}

	c.JSON(http.StatusOK, response)
}

// TestNATS godoc
// @Summary Test NATS messaging
// @Description Send a test message to NATS to verify messaging is working
// @Tags health
// @Accept json
// @Produce json
// @Success 200 {object} TestMessageResponse
// @Failure 500 {object} TestMessageResponse
// @Router /test/nats [post]
func (h *HealthHandler) TestNATS(c *gin.Context) {
	// Generate unique message ID
	messageID := fmt.Sprintf("test-%d-%s", time.Now().Unix(), h.cfg.WorkerID)

	// Create test message
	testMessage := map[string]interface{}{
		"id":        messageID,
		"worker_id": h.cfg.WorkerID,
		"type":      "test",
		"message":   "NATS connectivity test",
		"timestamp": time.Now(),
		"data": map[string]interface{}{
			"test_number": time.Now().Unix(),
			"environment": h.cfg.Environment,
		},
	}

	// Check if messaging service is available
	natsStatus := "disconnected"
	if h.container.MessageSvc != nil && h.container.MessageSvc.IsConnected() {
		natsStatus = "connected"
	}

	response := TestMessageResponse{
		WorkerID:   h.cfg.WorkerID,
		Subject:    h.cfg.AlertsSubject,
		Message:    "Test message sent successfully",
		Timestamp:  time.Now(),
		NATSStatus: natsStatus,
		MessageID:  messageID,
	}

	// Try to send the message if NATS is connected
	if h.container.MessageSvc != nil && h.container.MessageSvc.IsConnected() {
		err := h.container.MessageSvc.Publish(h.cfg.AlertsSubject, testMessage)
		if err != nil {
			response.Status = "error"
			response.Message = fmt.Sprintf("Failed to send test message: %v", err)
			c.JSON(http.StatusInternalServerError, response)
			return
		}

		response.Status = "success"
		response.Message = "Test message sent successfully to NATS"
	} else {
		response.Status = "error"
		response.Message = "NATS messaging service is not connected"
		c.JSON(http.StatusServiceUnavailable, response)
		return
	}

	c.JSON(http.StatusOK, response)
}

func getServiceStatus(isHealthy bool) string {
	if isHealthy {
		return "healthy"
	}
	return "unhealthy"
}
