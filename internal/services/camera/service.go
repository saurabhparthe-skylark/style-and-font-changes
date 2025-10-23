package camera

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/postprocessing"
	"kepler-worker-go/internal/services/streamcapture"
)

// CameraManager manages multiple cameras with completely isolated resources per camera
type CameraManager struct {
	cfg *config.Config

	// Per-camera lifecycles with completely isolated resources
	cameras map[string]*CameraLifecycle
	mutex   sync.RWMutex

	// Manager-level resources (minimal shared state)
	stopChannel  chan struct{}
	watchdogOnce sync.Once

	// Post-processing service (for alerts)
	postProcessingService *postprocessing.Service
}

// NewCameraManager creates a new camera manager with complete per-camera resource isolation
func NewCameraManager(cfg *config.Config, postProcessingSvc *postprocessing.Service) (*CameraManager, error) {
	cm := &CameraManager{
		cfg:                   cfg,
		cameras:               make(map[string]*CameraLifecycle),
		stopChannel:           make(chan struct{}),
		postProcessingService: postProcessingSvc,
	}

	log.Info().
		Int("max_fps_no_ai", cfg.MaxFPSNoAI).
		Int("max_fps_with_ai", cfg.MaxFPSWithAI).
		Bool("ai_enabled", cfg.AIEnabled).
		Msg("Camera manager initialized with complete per-camera resource isolation")

	// Start watchdog
	cm.watchdogOnce.Do(func() {
		go cm.runWatchdog()
		log.Info().Dur("interval", cm.cfg.HealthCheckInterval).Msg("Watchdog started")
	})

	return cm, nil
}

// StartCamera starts a camera with completely isolated resources
func (cm *CameraManager) StartCamera(req *models.CameraRequest) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Check if camera already exists
	if lifecycle, exists := cm.cameras[req.CameraID]; exists {
		if lifecycle.getState() == StateRunning {
			return fmt.Errorf("camera %s is already running", req.CameraID)
		}
		// Stop existing camera completely before restarting
		log.Info().
			Str("camera_id", req.CameraID).
			Msg("Stopping existing camera for clean restart")
		if err := lifecycle.Stop(); err != nil {
			log.Warn().Err(err).Str("camera_id", req.CameraID).Msg("Failed to stop existing camera")
		}
		// Wait for complete cleanup
		time.Sleep(200 * time.Millisecond)
		delete(cm.cameras, req.CameraID)
	}

	// Configure AI settings with defaults from config
	aiEnabled := cm.cfg.AIEnabled
	if req.AIEnabled != nil {
		aiEnabled = *req.AIEnabled
	}

	aiEndpoint := cm.cfg.AIGRPCURL
	if req.AIEndpoint != nil && *req.AIEndpoint != "" {
		aiEndpoint = *req.AIEndpoint
	}

	aiTimeout := cm.cfg.AITimeout

	// Configure recording settings
	enableRecord := true // Default to enabled
	if req.EnableRecord != nil {
		enableRecord = *req.EnableRecord
	}

	// Configure status - always start when creating a new camera
	status := models.CameraStatusStart

	projects := make([]string, 0, len(req.CameraSolutions))
	for _, solution := range req.CameraSolutions {
		if solution.ProjectName != "" {
			projects = append(projects, solution.ProjectName)
		}
	}

	// Create camera with configuration
	camera := &models.Camera{
		ID:        req.CameraID,
		URL:       req.URL,
		IsActive:  false, // Will be set by lifecycle manager
		CreatedAt: time.Now(),

		// Status and Recording Configuration
		Status:       status,
		EnableRecord: enableRecord,
		IsRecording:  false, // Will be set when recording actually starts
		IsPaused:     status == models.CameraStatusPaused,

		// AI Configuration
		AIEnabled:  aiEnabled,
		AIEndpoint: aiEndpoint,
		AITimeout:  aiTimeout,

		// Camera Configuration and ROI Data
		Config:          req.Config,
		CameraSolutions: req.CameraSolutions,
		ROIData:         req.ROIData,
		Projects:        projects,

		// FPS Calculation setup
		RecentFrameTimes: make([]time.Time, 0, 30),
		FPSWindowSize:    30,
		AIFrameCounter:   0,

		// Generate MediaMTX URLs using shared publisher service
		RTSPUrl:   cm.cfg.GetRTSPURL(req.CameraID),
		WebRTCUrl: cm.cfg.GetWebRTCURL(req.CameraID),
		HLSUrl:    cm.cfg.GetHLSURL(req.CameraID),
		MJPEGUrl:  cm.cfg.GetMJPEGURL(req.CameraID),
	}

	// Create lifecycle manager with completely isolated resources
	lifecycle := NewCameraLifecycle(camera, cm)
	cm.cameras[req.CameraID] = lifecycle

	// Start the camera
	if err := lifecycle.Start(); err != nil {
		delete(cm.cameras, req.CameraID)
		return fmt.Errorf("failed to start camera %s: %w", req.CameraID, err)
	}

	log.Info().
		Str("camera_id", req.CameraID).
		Str("url", req.URL).
		Int("solution_count", len(camera.CameraSolutions)).
		Bool("ai_enabled", camera.AIEnabled).
		Str("ai_endpoint", camera.AIEndpoint).
		Dur("ai_timeout", camera.AITimeout).
		Str("rtsp_url", camera.RTSPUrl).
		Str("webrtc_url", camera.WebRTCUrl).
		Str("hls_url", camera.HLSUrl).
		Str("mjpeg_url", camera.MJPEGUrl).
		Msg("Camera started with completely isolated resources")

	return nil
}

// Shutdown gracefully shuts down the camera manager
func (cm *CameraManager) Shutdown(ctx context.Context) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	log.Info().Msg("Shutting down camera manager")

	// Stop all cameras
	for cameraID, lifecycle := range cm.cameras {
		if err := lifecycle.Stop(); err != nil {
			log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop camera during shutdown")
		}
	}

	// Stop watchdog
	cm.safeCloseStopChannel(cm.stopChannel, "watchdog")

	return nil
}

// GetCamera returns camera information
func (cm *CameraManager) GetCamera(cameraID string) (*models.CameraResponse, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	lifecycle, exists := cm.cameras[cameraID]
	if !exists {
		return nil, fmt.Errorf("camera %s not found", cameraID)
	}

	camera := lifecycle.camera

	return &models.CameraResponse{
		CameraID:         camera.ID,
		URL:              camera.URL,
		IsActive:         camera.IsActive,
		Status:           camera.Status,
		IsRecording:      camera.IsRecording,
		IsPaused:         camera.IsPaused,
		EnableRecord:     camera.EnableRecord,
		CreatedAt:        camera.CreatedAt,
		LastFrameTime:    camera.LastFrameTime,
		FrameCount:       camera.FrameCount,
		ErrorCount:       camera.ErrorCount,
		FPS:              camera.FPS,
		Latency:          camera.Latency.String(),
		AIEnabled:        camera.AIEnabled,
		AIEndpoint:       camera.AIEndpoint,
		AITimeout:        camera.AITimeout.String(),
		AIProcessingTime: camera.AIProcessingTime.String(),
		LastAIError:      camera.LastAIError,
		AIDetectionCount: camera.AIDetectionCount,
		RTSPUrl:          camera.RTSPUrl,
		WebRTCUrl:        camera.WebRTCUrl,
		HLSUrl:           camera.HLSUrl,
		MJPEGUrl:         camera.MJPEGUrl,
		Config:           camera.Config,
		CameraSolutions:  camera.CameraSolutions,
		ROIData:          camera.ROIData,
	}, nil
}

// StopCamera stops a camera and its pipeline
func (cm *CameraManager) StopCamera(cameraID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	lifecycle, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	if err := lifecycle.Stop(); err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop camera")
		return fmt.Errorf("failed to stop camera %s: %w", cameraID, err)
	}

	delete(cm.cameras, cameraID)

	log.Info().Str("camera_id", cameraID).Msg("Camera stopped successfully")
	return nil
}

// GetStats returns camera statistics
func (cm *CameraManager) GetStats() (int, int) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	active := 0
	for _, lifecycle := range cm.cameras {
		if lifecycle.getState() == StateRunning {
			active++
		}
	}

	return active, len(cm.cameras)
}

// GetCameras returns all cameras
func (cm *CameraManager) GetCameras() []*models.CameraResponse {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	cameras := make([]*models.CameraResponse, 0, len(cm.cameras))
	for _, lifecycle := range cm.cameras {
		camera := lifecycle.camera
		cameras = append(cameras, &models.CameraResponse{
			CameraID:         camera.ID,
			URL:              camera.URL,
			IsActive:         camera.IsActive,
			Status:           camera.Status,
			IsRecording:      camera.IsRecording,
			IsPaused:         camera.IsPaused,
			EnableRecord:     camera.EnableRecord,
			CreatedAt:        camera.CreatedAt,
			LastFrameTime:    camera.LastFrameTime,
			FrameCount:       camera.FrameCount,
			ErrorCount:       camera.ErrorCount,
			FPS:              camera.FPS,
			Latency:          camera.Latency.String(),
			AIEnabled:        camera.AIEnabled,
			AIEndpoint:       camera.AIEndpoint,
			AITimeout:        camera.AITimeout.String(),
			AIProcessingTime: camera.AIProcessingTime.String(),
			LastAIError:      camera.LastAIError,
			AIDetectionCount: camera.AIDetectionCount,
			RTSPUrl:          camera.RTSPUrl,
			WebRTCUrl:        camera.WebRTCUrl,
			HLSUrl:           camera.HLSUrl,
			MJPEGUrl:         camera.MJPEGUrl,
			Config:           camera.Config,
			CameraSolutions:  camera.CameraSolutions,
			ROIData:          camera.ROIData,
		})
	}

	return cameras
}

// ServeHTTP serves MJPEG stream for a camera using isolated publisher
func (cm *CameraManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract camera ID from URL path
	path := r.URL.Path
	if len(path) < 8 || path[:7] != "/mjpeg/" {
		http.Error(w, "Invalid MJPEG URL format", http.StatusBadRequest)
		return
	}

	cameraID := path[7:]
	if cameraID == "" {
		http.Error(w, "Camera ID is required", http.StatusBadRequest)
		return
	}

	// Check if camera exists
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		http.Error(w, fmt.Sprintf("Camera %s not found", cameraID), http.StatusNotFound)
		return
	}

	if lifecycle.getState() != StateRunning {
		http.Error(w, fmt.Sprintf("Camera %s is not running", cameraID), http.StatusServiceUnavailable)
		return
	}

	// Get the camera's dedicated publisher service
	publisherSvc := lifecycle.getPublisherService()
	if publisherSvc == nil {
		http.Error(w, "Publisher service not available for this camera", http.StatusServiceUnavailable)
		return
	}

	publisherSvc.StreamMJPEGHTTP(w, r, cameraID)
}

// runWatchdog monitors camera health and handles cleanup
func (cm *CameraManager) runWatchdog() {
	ticker := time.NewTicker(cm.cfg.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cm.stopChannel:
			return
		case <-ticker.C:
			cm.checkCameraHealth()
		}
	}
}

// checkCameraHealth checks health of all cameras
func (cm *CameraManager) checkCameraHealth() {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	now := time.Now()
	for _, lifecycle := range cm.cameras {
		camera := lifecycle.camera
		if lifecycle.getState() == StateRunning && !camera.LastFrameTime.IsZero() {
			timeSinceLastFrame := now.Sub(camera.LastFrameTime)
			if timeSinceLastFrame > cm.cfg.FrameStaleThreshold {
				log.Warn().
					Str("camera_id", camera.ID).
					Dur("time_since_last_frame", timeSinceLastFrame).
					Msg("Camera appears to be stale - attempting restart")
				_ = lifecycle.Restart()
			}
		}
	}
}

// UpdateCameraSettings updates camera settings dynamically
func (cm *CameraManager) UpdateCameraSettings(cameraID string, req *models.CameraUpsertRequest) error {
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	camera := lifecycle.camera
	needsRestart := false

	// Update URL if provided (requires restart)
	if req.URL != nil && *req.URL != camera.URL {
		oldURL := camera.URL
		camera.URL = *req.URL
		needsRestart = true

		log.Info().
			Str("camera_id", cameraID).
			Str("old_url", oldURL).
			Str("new_url", *req.URL).
			Msg("Camera URL updated, will restart camera")
	}

	// Projects field removed - now using CameraSolutions

	// Update AI configuration (NO RESTART NEEDED - dynamic!)
	if req.AIEnabled != nil && *req.AIEnabled != camera.AIEnabled {
		camera.AIEnabled = *req.AIEnabled
		log.Info().
			Str("camera_id", cameraID).
			Bool("ai_enabled", camera.AIEnabled).
			Msg("AI state changed dynamically")
	}

	if req.AIEndpoint != nil && *req.AIEndpoint != camera.AIEndpoint {
		oldAIEndpoint := camera.AIEndpoint
		camera.AIEndpoint = *req.AIEndpoint
		log.Info().
			Str("camera_id", cameraID).
			Str("old_ai_endpoint", oldAIEndpoint).
			Str("new_ai_endpoint", camera.AIEndpoint).
			Msg("AI endpoint changed dynamically (no restart needed)")
	}

	// Update recording settings (no restart needed)
	if req.EnableRecord != nil {
		camera.EnableRecord = *req.EnableRecord
	}

	// Update camera configuration (no restart needed)
	if req.Config != nil {
		camera.Config = req.Config
		log.Info().
			Str("camera_id", cameraID).
			Msg("Camera config updated dynamically")
	}

	// Update camera solutions (no restart needed - can be changed dynamically)
	if req.CameraSolutions != nil {
		camera.CameraSolutions = req.CameraSolutions
		// Also keep derived projects in sync for AI routing and overlays
		projects := make([]string, 0, len(req.CameraSolutions))
		for _, solution := range req.CameraSolutions {
			if solution.ProjectName != "" {
				projects = append(projects, solution.ProjectName)
			}
		}
		camera.Projects = projects
		log.Info().
			Str("camera_id", cameraID).
			Int("solution_count", len(req.CameraSolutions)).
			Int("project_count", len(projects)).
			Msg("Camera solutions updated dynamically and projects synchronized")
	}

	// Update ROI data (no restart needed)
	if req.ROIData != nil {
		camera.ROIData = req.ROIData
		log.Info().
			Str("camera_id", cameraID).
			Int("roi_count", len(req.ROIData)).
			Msg("Camera ROI data updated dynamically")
	}

	// Update status
	if req.Status != nil {
		oldStatus := camera.Status
		camera.Status = *req.Status
		camera.IsPaused = camera.Status == models.CameraStatusPaused

		// Handle status changes
		if camera.Status == models.CameraStatusStop {
			log.Info().
				Str("camera_id", cameraID).
				Str("old_status", oldStatus.String()).
				Str("new_status", camera.Status.String()).
				Msg("Camera status changed to stop")

			return lifecycle.Stop()
		}

		if oldStatus == models.CameraStatusStop && camera.Status == models.CameraStatusStart {
			log.Info().
				Str("camera_id", cameraID).
				Str("old_status", oldStatus.String()).
				Str("new_status", camera.Status.String()).
				Msg("Camera status changed from stop to start")

			return lifecycle.Start()
		}
	}

	// If any critical settings changed, restart the camera
	if needsRestart && lifecycle.getState() == StateRunning {
		log.Info().
			Str("camera_id", cameraID).
			Msg("Restarting camera due to URL change")

		return lifecycle.Restart()
	}

	log.Info().
		Str("camera_id", cameraID).
		Str("status", camera.Status.String()).
		Bool("enable_record", camera.EnableRecord).
		Bool("is_paused", camera.IsPaused).
		Bool("restarted", needsRestart).
		Msg("Camera settings updated")

	return nil
}

// RestartCamera performs a restart of the camera
func (cm *CameraManager) RestartCamera(cameraID string) error {
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	log.Info().Str("camera_id", cameraID).Msg("Performing camera restart")

	return lifecycle.Restart()
}

// ForceRestartCamera forces a restart of the camera
func (cm *CameraManager) ForceRestartCamera(cameraID string) error {
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	log.Warn().Str("camera_id", cameraID).Msg("Forcing camera restart for recovery")

	return lifecycle.ForceRestart()
}

// Safe channel closing helper
func (cm *CameraManager) safeCloseStopChannel(ch chan struct{}, cameraID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().Str("camera_id", cameraID).Msg("StopChannel already closed")
		}
	}()
	close(ch)
}

// ResyncRealtime performs a lightweight resync by restarting the camera
func (cm *CameraManager) ResyncRealtime(cameraID string) error {
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	if lifecycle.getState() != StateRunning {
		return nil
	}

	log.Info().
		Str("camera_id", cameraID).
		Msg("Performing realtime resync via restart")

	return lifecycle.Restart()
}

// ValidateRTSPAndCaptureThumbnail validates RTSP stream
func (cm *CameraManager) ValidateRTSPAndCaptureThumbnail(rtspURL string) *models.RTSPCheckResponse {
	log.Info().Str("rtsp_url", rtspURL).Msg("Validating RTSP stream")

	// Create temporary streamcapture service for validation
	tempStreamCapture := streamcapture.NewService(cm.cfg)
	return tempStreamCapture.ValidateRTSPAndCaptureThumbnail(rtspURL)
}
