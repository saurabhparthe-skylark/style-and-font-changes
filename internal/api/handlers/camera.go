package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/services/camera"
)

type CameraHandler struct {
	cameraManager *camera.CameraManager
}

func NewCameraHandler(cameraManager *camera.CameraManager) *CameraHandler {
	return &CameraHandler{
		cameraManager: cameraManager,
	}
}

// StartCamera starts a camera stream
// @Summary Start a camera stream
// @Description Start streaming from a camera with optional AI projects
// @Tags cameras
// @Accept json
// @Produce json
// @Param request body camera.CameraRequest true "Camera configuration"
// @Success 200 {object} camera.CameraResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /cameras [post]
func (h *CameraHandler) StartCamera(c *gin.Context) {
	var req camera.CameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Msg("Invalid request body")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.cameraManager.StartCamera(&req)
	if err != nil {
		log.Error().Err(err).Str("camera_id", req.CameraID).Msg("Failed to start camera")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get camera details
	camera, err := h.cameraManager.GetCamera(req.CameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", req.CameraID).Msg("Failed to get camera details")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Camera started but failed to get details"})
		return
	}

	log.Info().
		Str("camera_id", req.CameraID).
		Str("url", req.URL).
		Strs("projects", req.Projects).
		Msg("Camera started successfully")

	c.JSON(http.StatusOK, camera)
}

// StopCamera stops a camera stream
// @Summary Stop a camera stream
// @Description Stop streaming from a camera
// @Tags cameras
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} SuccessResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /cameras/{camera_id}/stop [post]
func (h *CameraHandler) StopCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	err := h.cameraManager.StopCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop camera")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Info().Str("camera_id", cameraID).Msg("Camera stopped successfully")
	c.JSON(http.StatusOK, gin.H{"message": "Camera stopped successfully"})
}

// GetCamera gets camera details
// @Summary Get camera details
// @Description Get details of a specific camera
// @Tags cameras
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} camera.CameraResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /cameras/{camera_id} [get]
func (h *CameraHandler) GetCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	camera, err := h.cameraManager.GetCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Camera not found")
		c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
		return
	}

	c.JSON(http.StatusOK, camera)
}

// ListCameras lists all cameras
// @Summary List all cameras
// @Description Get list of all cameras with their details
// @Tags cameras
// @Success 200 {array} camera.CameraResponse
// @Router /cameras [get]
func (h *CameraHandler) ListCameras(c *gin.Context) {
	cameras := h.cameraManager.ListCameras()
	c.JSON(http.StatusOK, gin.H{
		"cameras": cameras,
		"count":   len(cameras),
	})
}

// GetCameraStats gets camera statistics
// @Summary Get camera statistics
// @Description Get statistics about active cameras
// @Tags cameras
// @Success 200 {object} map[string]interface{}
// @Router /cameras/stats [get]
func (h *CameraHandler) GetCameraStats(c *gin.Context) {
	active, total := h.cameraManager.GetStats()

	c.JSON(http.StatusOK, gin.H{
		"active_cameras": active,
		"total_cameras":  total,
		"status":         "healthy",
	})
}
