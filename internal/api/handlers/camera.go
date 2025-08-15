package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/camera"
)

type CameraHandler struct {
	cameraManager *camera.CameraManager
}

func NewCameraHandler(cameraManager *camera.CameraManager) *CameraHandler {
	return &CameraHandler{cameraManager: cameraManager}
}

// StartCamera godoc
// @Summary Start a camera stream
// @Description Start streaming from a camera with the specified configuration
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera body models.CameraRequest true "Camera configuration"
// @Success 200 {object} models.CameraResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras [post]
func (h *CameraHandler) StartCamera(c *gin.Context) {
	var req models.CameraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Msg("invalid_request_body")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cameraManager.StartCamera(&req); err != nil {
		log.Error().Err(err).Str("camera_id", req.CameraID).Msg("start_camera_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	camera, err := h.cameraManager.GetCamera(req.CameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", req.CameraID).Msg("get_camera_after_start_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Camera started but failed to get details"})
		return
	}
	log.Info().Str("camera_id", req.CameraID).Str("url", req.URL).Strs("projects", req.Projects).Msg("camera_started")
	c.JSON(http.StatusOK, camera)
}

// StopCamera godoc
// @Summary Stop a camera stream
// @Description Stop streaming from the specified camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id}/stop [post]
func (h *CameraHandler) StopCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	if err := h.cameraManager.StopCamera(cameraID); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("stop_camera_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Info().Str("camera_id", cameraID).Msg("camera_stopped")
	c.JSON(http.StatusOK, gin.H{"message": "Camera stopped successfully"})
}

// GetCamera godoc
// @Summary Get camera details
// @Description Get detailed information about a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.CameraResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Router /cameras/{camera_id} [get]
func (h *CameraHandler) GetCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	camera, err := h.cameraManager.GetCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("camera_not_found")
		c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
		return
	}
	c.JSON(http.StatusOK, camera)
}

// ListCameras godoc
// @Summary List all cameras
// @Description Get a list of all cameras with their current status
// @Tags cameras
// @Accept json
// @Produce json
// @Success 200 {object} object{cameras=[]models.CameraResponse,count=int}
// @Router /cameras [get]
func (h *CameraHandler) ListCameras(c *gin.Context) {
	cameras := h.cameraManager.GetCameras()
	c.JSON(http.StatusOK, cameras)
}

// GetCameraStats godoc
// @Summary Get camera statistics
// @Description Get overall camera statistics including active and total count
// @Tags cameras
// @Accept json
// @Produce json
// @Success 200 {object} object{active_cameras=int,total_cameras=int,status=string}
// @Router /cameras/stats [get]
func (h *CameraHandler) GetCameraStats(c *gin.Context) {
	active, total := h.cameraManager.GetStats()
	c.JSON(http.StatusOK, gin.H{"active_cameras": active, "total_cameras": total, "status": "healthy"})
}

// UpdateCameraAI godoc
// @Summary Update camera AI configuration
// @Description Update AI processing settings for a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Param ai_config body models.AIConfigRequest true "AI configuration"
// @Success 200 {object} models.AIConfigResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id}/ai [put]
func (h *CameraHandler) UpdateCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	var req models.AIConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("invalid_ai_config_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cameraManager.UpdateAIConfig(cameraID, &req); err != nil {
		if err.Error() == "camera not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("update_camera_ai_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	aiConfig, err := h.cameraManager.GetAIConfig(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_ai_config_after_update_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI config updated but failed to get details"})
		return
	}
	log.Info().Str("camera_id", cameraID).Bool("ai_enabled", aiConfig.AIEnabled).Str("ai_endpoint", aiConfig.AIEndpoint).Strs("projects", aiConfig.Projects).Msg("camera_ai_config_updated")
	c.JSON(http.StatusOK, aiConfig)
}

// GetCameraAI godoc
// @Summary Get camera AI configuration
// @Description Get AI processing settings for a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.AIConfigResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id}/ai [get]
func (h *CameraHandler) GetCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	aiConfig, err := h.cameraManager.GetAIConfig(cameraID)
	if err != nil {
		if err.Error() == "camera not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_camera_ai_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, aiConfig)
}

// ToggleCameraAI godoc
// @Summary Toggle camera AI processing
// @Description Enable or disable AI processing for a specific camera
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.AIConfigResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id}/ai/toggle [post]
func (h *CameraHandler) ToggleCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	// Get current config to toggle AI enabled
	currentConfig, err := h.cameraManager.GetAIConfig(cameraID)
	if err != nil {
		if err.Error() == "camera not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_current_ai_config_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Toggle AI enabled flag
	newEnabled := !currentConfig.AIEnabled
	toggleReq := &models.AIConfigRequest{
		AIEnabled: &newEnabled,
	}

	if err := h.cameraManager.UpdateAIConfig(cameraID, toggleReq); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("toggle_camera_ai_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	aiConfig, err := h.cameraManager.GetAIConfig(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_ai_config_after_toggle_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI toggled but failed to get details"})
		return
	}
	log.Info().Str("camera_id", cameraID).Bool("ai_enabled", aiConfig.AIEnabled).Msg("camera_ai_toggled")
	c.JSON(http.StatusOK, aiConfig)
}

// UpdateCamera godoc
// @Summary Update camera settings dynamically
// @Description Update any camera settings dynamically without restart - supports URL changes, AI config, recording control, status management (start/stop/pause), and project updates
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Param settings body models.CameraUpdateRequest true "Camera update settings (all fields optional)"
// @Success 200 {object} models.CameraResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id} [put]
func (h *CameraHandler) UpdateCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	var req models.CameraUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("invalid_camera_update_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.cameraManager.UpdateCameraSettings(cameraID, &req); err != nil {
		if err.Error() == fmt.Sprintf("camera %s not found", cameraID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("update_camera_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return updated camera details
	camera, err := h.cameraManager.GetCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_camera_after_update_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Camera updated but failed to get details"})
		return
	}

	log.Info().Str("camera_id", cameraID).Msg("camera_updated")
	c.JSON(http.StatusOK, camera)
}
