package handlers

import (
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services"
)

type WorkerHandler struct {
	cfg       *config.Config
	container *services.ServiceContainer
}

func NewWorkerHandler(cfg *config.Config, container *services.ServiceContainer) *WorkerHandler {
	return &WorkerHandler{
		cfg:       cfg,
		container: container,
	}
}

type WorkerInfoResponse struct {
	WorkerID     string       `json:"worker_id"`
	Version      string       `json:"version"`
	Environment  string       `json:"environment"`
	Port         int          `json:"port"`
	StartTime    time.Time    `json:"start_time"`
	Capabilities []string     `json:"capabilities"`
	Config       WorkerConfig `json:"config"`
}

type WorkerConfig struct {
	MaxCameras        int           `json:"max_cameras"`
	ProcessingWorkers int           `json:"processing_workers"`
	TargetFPS         int           `json:"target_fps"`
	BufferSize        int           `json:"buffer_size"`
	RTSPTimeout       time.Duration `json:"rtsp_timeout"`
	MaxRetries        int           `json:"max_retries"`
}

type ShutdownRequest struct {
	Force bool `json:"force,omitempty"`
}

type ShutdownResponse struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

var startTime = time.Now()

// GetInfo godoc
// @Summary Get worker information
// @Description Get detailed information about the worker service
// @Tags worker
// @Accept json
// @Produce json
// @Success 200 {object} WorkerInfoResponse
// @Router /worker/info [get]
func (h *WorkerHandler) GetInfo(c *gin.Context) {
	capabilities := []string{
		"rtsp_streaming",
		"ai_detection",
		"webrtc_publishing",
		"frame_processing",
		"multi_camera",
	}

	response := WorkerInfoResponse{
		WorkerID:     h.cfg.WorkerID,
		Version:      h.cfg.Version,
		Environment:  h.cfg.Environment,
		Port:         h.cfg.Port,
		StartTime:    startTime,
		Capabilities: capabilities,
		Config: WorkerConfig{
			MaxCameras:        h.cfg.MaxCameras,
			ProcessingWorkers: h.cfg.ProcessingWorkers,
			TargetFPS:         h.cfg.MaxFPSNoAI,      // Use MaxFPSNoAI as target FPS
			BufferSize:        h.cfg.FrameBufferSize, // Use FrameBufferSize
			RTSPTimeout:       h.cfg.RTSPTimeout,
			MaxRetries:        h.cfg.MaxRetries,
		},
	}

	c.JSON(http.StatusOK, response)
}

// Shutdown godoc
// @Summary Shutdown worker
// @Description Gracefully shutdown the worker service
// @Tags worker
// @Accept json
// @Produce json
// @Param shutdown body ShutdownRequest false "Shutdown options"
// @Success 200 {object} ShutdownResponse
// @Failure 500 {object} ErrorResponse
// @Router /worker/shutdown [post]
func (h *WorkerHandler) Shutdown(c *gin.Context) {
	var req ShutdownRequest
	c.ShouldBindJSON(&req) // Optional body

	response := ShutdownResponse{
		Status:    "shutting_down",
		Message:   "Worker shutdown initiated",
		Timestamp: time.Now(),
	}

	c.JSON(http.StatusOK, response)

	// Initiate shutdown in a goroutine to allow response to be sent
	go func() {
		if req.Force {
			// Force shutdown immediately
			time.Sleep(100 * time.Millisecond) // Allow response to be sent
			os.Exit(0)
		} else {
			// Graceful shutdown
			time.Sleep(100 * time.Millisecond) // Allow response to be sent
			process, _ := os.FindProcess(os.Getpid())
			process.Signal(syscall.SIGTERM)
		}
	}()
}
