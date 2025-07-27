package handlers

import (
	"kepler-worker/internal/worker"
	"net/http"

	"github.com/gin-gonic/gin"
)

type CameraHandler struct {
	worker *worker.Worker
}

func NewCameraHandler(w *worker.Worker) *CameraHandler {
	return &CameraHandler{worker: w}
}

type StartCameraRequest struct {
	StreamURL string   `json:"stream_url" binding:"required" example:"rtsp://camera.example.com/stream"`
	Solutions []string `json:"solutions" binding:"required" example:"person_detection,ppe_detection"`
}

type CameraResponse struct {
	CameraID  string `json:"camera_id" example:"cam1"`
	Status    string `json:"status" example:"active"`
	StreamURL string `json:"stream_url,omitempty" example:"rtsp://camera.example.com/stream"`
	Message   string `json:"message,omitempty" example:"Camera started successfully"`
}

type CameraListResponse struct {
	Cameras []CameraResponse `json:"cameras"`
	Total   int              `json:"total" example:"3"`
}

type CameraStatusResponse struct {
	CameraID       string                 `json:"camera_id" example:"cam1"`
	Active         bool                   `json:"active" example:"true"`
	StreamInfo     map[string]interface{} `json:"stream_info"`
	ProcessingInfo map[string]interface{} `json:"processing_info"`
	WebRTCInfo     map[string]interface{} `json:"webrtc_info"`
}

// @Summary Start camera processing
// @Description Start processing for a camera with RTSP stream
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"cam1"
// @Param camera body StartCameraRequest true "Camera configuration"
// @Success 200 {object} CameraResponse
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cameras/{id}/start [post]
func (h *CameraHandler) StartCamera(c *gin.Context) {
	cameraID := c.Param("id")

	var req StartCameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.worker.StartCamera(cameraID, req.StreamURL, req.Solutions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, CameraResponse{
		CameraID:  cameraID,
		Status:    "started",
		StreamURL: req.StreamURL,
		Message:   "Camera started successfully",
	})
}

// @Summary Stop camera processing
// @Description Stop processing for a camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"cam1"
// @Success 200 {object} CameraResponse
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cameras/{id}/stop [post]
func (h *CameraHandler) StopCamera(c *gin.Context) {
	cameraID := c.Param("id")

	if err := h.worker.StopCamera(cameraID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, CameraResponse{
		CameraID: cameraID,
		Status:   "stopped",
		Message:  "Camera stopped successfully",
	})
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

	c.JSON(http.StatusOK, CameraListResponse{
		Cameras: cameras,
		Total:   len(cameras),
	})
}

// @Summary Get camera status
// @Description Get detailed status of a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"cam1"
// @Success 200 {object} CameraStatusResponse
// @Failure 404 {object} map[string]string
// @Router /cameras/{id}/status [get]
func (h *CameraHandler) GetCameraStatus(c *gin.Context) {
	cameraID := c.Param("id")
	status := h.worker.GetCameraStatus()

	cameraStatus, exists := status[cameraID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
		return
	}

	if cameraInfo, ok := cameraStatus.(map[string]interface{}); ok {
		response := CameraStatusResponse{
			CameraID:       cameraID,
			Active:         getBoolValue(cameraInfo, "active", false),
			StreamInfo:     getMapValue(cameraInfo, "stream_info"),
			ProcessingInfo: getMapValue(cameraInfo, "processing_info"),
			WebRTCInfo:     getMapValue(cameraInfo, "webrtc_info"),
		}
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid camera status format"})
	}
}

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
