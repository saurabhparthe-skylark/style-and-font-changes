package handlers

import (
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

func (h *CameraHandler) ListCameras(c *gin.Context) {
	cameras := h.cameraManager.ListCameras()
	c.JSON(http.StatusOK, gin.H{"cameras": cameras, "count": len(cameras)})
}

func (h *CameraHandler) GetCameraStats(c *gin.Context) {
	active, total := h.cameraManager.GetStats()
	c.JSON(http.StatusOK, gin.H{"active_cameras": active, "total_cameras": total, "status": "healthy"})
}

type AIConfigRequest struct {
	AIEnabled  *bool    `json:"ai_enabled,omitempty"`
	AIEndpoint *string  `json:"ai_endpoint,omitempty"`
	AITimeout  *string  `json:"ai_timeout,omitempty"`
	Projects   []string `json:"projects,omitempty"`
}

type AIConfigResponse struct {
	CameraID         string   `json:"camera_id"`
	AIEnabled        bool     `json:"ai_enabled"`
	AIEndpoint       string   `json:"ai_endpoint"`
	AITimeout        string   `json:"ai_timeout"`
	Projects         []string `json:"projects"`
	AIProcessingTime string   `json:"ai_processing_time"`
	LastAIError      string   `json:"last_ai_error,omitempty"`
	AIDetectionCount int64    `json:"ai_detection_count"`
}

func (h *CameraHandler) UpdateCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	var req AIConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("invalid_ai_config_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cameraManager.UpdateCameraAI(cameraID, &models.AIConfigRequest{AIEnabled: req.AIEnabled, AIEndpoint: req.AIEndpoint, AITimeout: req.AITimeout, Projects: req.Projects}); err != nil {
		if err.Error() == "camera not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("update_camera_ai_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	aiConfig, err := h.cameraManager.GetCameraAI(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_ai_config_after_update_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI config updated but failed to get details"})
		return
	}
	log.Info().Str("camera_id", cameraID).Bool("ai_enabled", aiConfig.AIEnabled).Str("ai_endpoint", aiConfig.AIEndpoint).Strs("projects", aiConfig.Projects).Msg("camera_ai_config_updated")
	c.JSON(http.StatusOK, aiConfig)
}

func (h *CameraHandler) GetCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	aiConfig, err := h.cameraManager.GetCameraAI(cameraID)
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

func (h *CameraHandler) ToggleCameraAI(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	if err := h.cameraManager.ToggleCameraAI(cameraID); err != nil {
		if err.Error() == "camera not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("toggle_camera_ai_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	aiConfig, err := h.cameraManager.GetCameraAI(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_ai_config_after_toggle_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI toggled but failed to get details"})
		return
	}
	log.Info().Str("camera_id", cameraID).Bool("ai_enabled", aiConfig.AIEnabled).Msg("camera_ai_toggled")
	c.JSON(http.StatusOK, aiConfig)
}
