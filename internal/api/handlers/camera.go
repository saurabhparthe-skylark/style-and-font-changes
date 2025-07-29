package handlers

import (
	"kepler-worker/internal/worker"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type CameraHandler struct {
	worker *worker.Worker
}

func NewCameraHandler(w *worker.Worker) *CameraHandler {
	return &CameraHandler{worker: w}
}

type StartCameraRequest struct {
	StreamURL string   `json:"stream_url" binding:"required" example:"rtsp://wowzaec2demo.streamlock.net/vod/mp4:BigBuckBunny_115k.mp4"`
	Solutions []string `json:"solutions" binding:"required" example:"person_detection,ppe_detection"`
}

type CameraResponse struct {
	CameraID  string `json:"camera_id" example:"test-camera"`
	Status    string `json:"status" example:"started"`
	StreamURL string `json:"stream_url,omitempty" example:"rtsp://wowzaec2demo.streamlock.net/vod/mp4:BigBuckBunny_115k.mp4"`
	Message   string `json:"message,omitempty" example:"Camera started successfully"`
	Timestamp string `json:"timestamp,omitempty" example:"2025-07-28T19:45:51Z"`
}

type CameraListResponse struct {
	Cameras []CameraResponse `json:"cameras"`
	Total   int              `json:"total" example:"3"`
}

type CameraStatusResponse struct {
	CameraID       string                 `json:"camera_id" example:"test-camera"`
	Active         bool                   `json:"active" example:"true"`
	Status         string                 `json:"status" example:"running"`
	StreamInfo     map[string]interface{} `json:"stream_info"`
	ProcessingInfo map[string]interface{} `json:"processing_info"`
	WebRTCInfo     map[string]interface{} `json:"webrtc_info"`
	Uptime         string                 `json:"uptime,omitempty" example:"5m30s"`
	LastSeen       string                 `json:"last_seen,omitempty" example:"2025-07-28T19:45:51Z"`
}

// @Summary Start camera processing
// @Description Start processing for a camera with RTSP stream
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"test-camera"
// @Param camera body StartCameraRequest true "Camera configuration"
// @Success 200 {object} CameraResponse
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cameras/{id}/start [post]
func (h *CameraHandler) StartCamera(c *gin.Context) {
	cameraID := c.Param("id")

	// Validate camera ID
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Camera ID is required",
			"code":  "INVALID_CAMERA_ID",
		})
		return
	}

	var req StartCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request format: " + err.Error(),
			"code":  "INVALID_REQUEST",
		})
		return
	}

	// Validate stream URL
	if req.StreamURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Stream URL is required",
			"code":  "INVALID_STREAM_URL",
		})
		return
	}

	// Validate solutions
	if len(req.Solutions) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "At least one solution is required",
			"code":  "INVALID_SOLUTIONS",
		})
		return
	}

	// Check if camera already exists
	status := h.worker.GetCameraStatus()
	if _, exists := status[cameraID]; exists {
		c.JSON(http.StatusConflict, gin.H{
			"error":     "Camera already exists",
			"code":      "CAMERA_EXISTS",
			"camera_id": cameraID,
		})
		return
	}

	// Start the camera
	if err := h.worker.StartCamera(cameraID, req.StreamURL, req.Solutions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":     "Failed to start camera: " + err.Error(),
			"code":      "START_FAILED",
			"camera_id": cameraID,
		})
		return
	}

	response := CameraResponse{
		CameraID:  cameraID,
		Status:    "started",
		StreamURL: req.StreamURL,
		Message:   "Camera started successfully",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, response)
}

// @Summary Stop camera processing
// @Description Stop processing for a camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"test-camera"
// @Success 200 {object} CameraResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cameras/{id}/stop [post]
func (h *CameraHandler) StopCamera(c *gin.Context) {
	cameraID := c.Param("id")

	// Validate camera ID
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Camera ID is required",
			"code":  "INVALID_CAMERA_ID",
		})
		return
	}

	// Check if camera exists
	status := h.worker.GetCameraStatus()
	if _, exists := status[cameraID]; !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error":     "Camera not found",
			"code":      "CAMERA_NOT_FOUND",
			"camera_id": cameraID,
		})
		return
	}

	if err := h.worker.StopCamera(cameraID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":     "Failed to stop camera: " + err.Error(),
			"code":      "STOP_FAILED",
			"camera_id": cameraID,
		})
		return
	}

	response := CameraResponse{
		CameraID:  cameraID,
		Status:    "stopped",
		Message:   "Camera stopped successfully",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, response)
}

// @Summary List all cameras
// @Description Get list of all cameras and their status
// @Tags cameras
// @Accept json
// @Produce json
// @Success 200 {object} CameraListResponse
// @Router /cameras [get]
func (h *CameraHandler) ListCameras(c *gin.Context) {
	status := h.worker.GetCameraStatus()

	cameras := make([]CameraResponse, 0, len(status))
	for id, info := range status {
		if cameraInfo, ok := info.(map[string]interface{}); ok {
			cameras = append(cameras, CameraResponse{
				CameraID: id,
				Status:   getStringValue(cameraInfo, "status", "unknown"),
			})
		}
	}

	response := CameraListResponse{
		Cameras: cameras,
		Total:   len(cameras),
	}

	c.JSON(http.StatusOK, response)
}

// @Summary Get camera status
// @Description Get detailed status of a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"test-camera"
// @Success 200 {object} CameraStatusResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /cameras/{id}/status [get]
func (h *CameraHandler) GetCameraStatus(c *gin.Context) {
	cameraID := c.Param("id")

	// Validate camera ID
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Camera ID is required",
			"code":  "INVALID_CAMERA_ID",
		})
		return
	}

	status := h.worker.GetCameraStatus()
	cameraStatus, exists := status[cameraID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{
			"error":     "Camera not found",
			"code":      "CAMERA_NOT_FOUND",
			"camera_id": cameraID,
		})
		return
	}

	if cameraInfo, ok := cameraStatus.(map[string]interface{}); ok {
		response := CameraStatusResponse{
			CameraID:       cameraID,
			Active:         getBoolValue(cameraInfo, "active", false),
			Status:         getStringValue(cameraInfo, "status", "unknown"),
			StreamInfo:     getMapValue(cameraInfo, "stream_info"),
			ProcessingInfo: getMapValue(cameraInfo, "processing_info"),
			WebRTCInfo:     getMapValue(cameraInfo, "webrtc_info"),
		}

		// Add uptime calculation if start time is available
		if startTime, ok := cameraInfo["start_time"].(time.Time); ok {
			uptime := time.Since(startTime)
			response.Uptime = formatDuration(uptime)
		}

		// Add last seen timestamp
		response.LastSeen = time.Now().UTC().Format(time.RFC3339)

		c.JSON(http.StatusOK, gin.H{
			"camera": response,
			"streaming_status": gin.H{
				"rtsp_processing":      "implemented",
				"ai_detection":         "implemented",
				"ffmpeg_republishing":  "implemented",
				"mediamtx_integration": "implemented",
				"output_formats": gin.H{
					"rtsp":   "available",
					"hls":    "available",
					"webrtc": "available",
				},
			},
			"method": "ffmpeg_republishing",
			"note":   "Camera processes RTSP input and republishes to MediaMTX via FFmpeg for multi-format output",
		})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":     "Invalid camera status format",
			"code":      "INVALID_STATUS_FORMAT",
			"camera_id": cameraID,
		})
	}
}

// Helper functions
func getStringValue(m map[string]interface{}, key, defaultValue string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return defaultValue
}

func getBoolValue(m map[string]interface{}, key string, defaultValue bool) bool {
	if val, ok := m[key].(bool); ok {
		return val
	}
	return defaultValue
}

func getMapValue(m map[string]interface{}, key string) map[string]interface{} {
	if val, ok := m[key].(map[string]interface{}); ok {
		return val
	}
	return make(map[string]interface{})
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Hour).String()
}
