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

// UpsertCamera godoc
// @Summary Upsert camera (Create or Update)
// @Description UPSERT operation: If camera exists, update settings smoothly. If not exists, create new camera. Supports all fields: URL, projects, AI config, recording control, status management.
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Param settings body models.CameraUpsertRequest true "Camera settings (URL required for creation, all other fields optional)"
// @Success 200 {object} models.CameraResponse
// @Success 201 {object} models.CameraResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id} [put]
func (h *CameraHandler) UpsertCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	var req models.CameraUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("invalid_camera_upsert_request")
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate status if provided
	if req.Status != nil {
		if !req.Status.IsValid() {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid status '%s'. Valid values are: start, stop, paused", req.Status.String())})
			return
		}
	}

	// Check if camera exists
	_, err := h.cameraManager.GetCamera(cameraID)
	cameraExists := err == nil

	if cameraExists {
		// UPDATE: Camera exists, update it smoothly
		log.Info().Str("camera_id", cameraID).Msg("Camera exists - updating settings")

		// Use CameraUpsertRequest directly for update logic
		if err := h.cameraManager.UpdateCameraSettings(cameraID, &req); err != nil {
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

		log.Info().Str("camera_id", cameraID).Msg("camera_updated_successfully")
		c.JSON(http.StatusOK, camera)
	} else {
		// CREATE: Camera doesn't exist, create it
		log.Info().Str("camera_id", cameraID).Msg("Camera not found - creating new camera")

		// URL is required for creation
		if req.URL == nil || *req.URL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "URL is required when creating a new camera"})
			return
		}

		// Convert CameraUpsertRequest to CameraRequest for creation
		createReq := models.CameraRequest{
			CameraID:     cameraID,
			URL:          *req.URL,
			Projects:     req.Projects,
			EnableRecord: req.EnableRecord,
			AIEnabled:    req.AIEnabled,
			AIEndpoint:   req.AIEndpoint,
		}

		// Create the camera
		if err := h.cameraManager.StartCamera(&createReq); err != nil {
			log.Error().Err(err).Str("camera_id", cameraID).Msg("create_camera_failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Return created camera details
		camera, err := h.cameraManager.GetCamera(cameraID)
		if err != nil {
			log.Error().Err(err).Str("camera_id", cameraID).Msg("get_camera_after_create_failed")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Camera created but failed to get details"})
			return
		}

		log.Info().Str("camera_id", cameraID).Str("url", *req.URL).Msg("camera_created_successfully")
		c.JSON(http.StatusCreated, camera)
	}
}

// RestartCamera godoc
// @Summary Hard restart a camera
// @Description Perform a complete hard restart/reset of a camera's entire pipeline, resetting all state and reconnecting
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.CameraResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id}/restart [post]
func (h *CameraHandler) RestartCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	if err := h.cameraManager.RestartCamera(cameraID); err != nil {
		if err.Error() == fmt.Sprintf("camera %s not found", cameraID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("restart_camera_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Return updated camera details after restart
	camera, err := h.cameraManager.GetCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("get_camera_after_restart_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Camera restarted but failed to get details"})
		return
	}

	log.Info().Str("camera_id", cameraID).Msg("camera_restarted_successfully")
	c.JSON(http.StatusOK, camera)
}

// DeleteCamera godoc
// @Summary Delete a camera
// @Description Permanently delete/remove a camera and stop its stream
// @Tags cameras
// @Accept json
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /cameras/{camera_id} [delete]
func (h *CameraHandler) DeleteCamera(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}
	if err := h.cameraManager.StopCamera(cameraID); err != nil {
		if err.Error() == fmt.Sprintf("camera %s not found", cameraID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found"})
			return
		}
		log.Error().Err(err).Str("camera_id", cameraID).Msg("delete_camera_failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Info().Str("camera_id", cameraID).Msg("camera_deleted")
	c.JSON(http.StatusOK, gin.H{"message": "Camera deleted successfully"})
}
