package camera

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/frameprocessing"
	"kepler-worker-go/internal/services/postprocessing"
	"kepler-worker-go/internal/services/publisher"
	"kepler-worker-go/internal/services/recorder"

	// "kepler-worker-go/internal/services/recorder"
	"kepler-worker-go/internal/services/streamcapture"
)

// CameraManager manages the entire camera pipeline with production-grade lifecycle management
type CameraManager struct {
	cfg *config.Config

	cameras map[string]*CameraLifecycle
	mutex   sync.RWMutex

	// Pipeline components
	frameProcessor   *frameprocessing.FrameProcessor
	publisherSvc     *publisher.Service // Unified publisher service (MJPEG + WebRTC)
	streamCaptureSvc *streamcapture.Service
	// recorder         *recorder.Service

	stopChannel chan struct{}

	// internal: watchdog
	watchdogOnce sync.Once

	// Post-processing service
	postProcessingService *postprocessing.Service
}

// NewCameraManager creates a new camera manager with full pipeline
func NewCameraManager(cfg *config.Config, postProcessingSvc *postprocessing.Service, recorderSvc *recorder.Service, publisherSvc *publisher.Service) (*CameraManager, error) {
	// Create pipeline components
	frameProcessor, err := frameprocessing.NewFrameProcessor(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create frame processor: %w", err)
	}

	streamCaptureSvc := streamcapture.NewService(cfg)

	cm := &CameraManager{
		cfg:              cfg,
		cameras:          make(map[string]*CameraLifecycle),
		frameProcessor:   frameProcessor,
		publisherSvc:     publisherSvc,
		streamCaptureSvc: streamCaptureSvc,
		// recorder:              recorderSvc,
		stopChannel:           make(chan struct{}),
		postProcessingService: postProcessingSvc,
	}

	log.Info().
		Int("max_cameras", cfg.MaxCameras).
		Int("max_fps_no_ai", cfg.MaxFPSNoAI).
		Int("max_fps_with_ai", cfg.MaxFPSWithAI).
		Bool("ai_enabled", cfg.AIEnabled).
		Msg("Camera manager initialized with enterprise pipeline")

	// Start watchdog to keep streams fresh and recover from staleness
	cm.watchdogOnce.Do(func() {
		go cm.runWatchdog()
		log.Info().Dur("interval", cm.cfg.HealthCheckInterval).Msg("Watchdog started")
	})

	return cm, nil
}

// StartCamera starts a camera with production-grade lifecycle management
func (cm *CameraManager) StartCamera(req *models.CameraRequest) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Check if camera already exists
	if lifecycle, exists := cm.cameras[req.CameraID]; exists {
		if lifecycle.getState() == StateRunning {
			return fmt.Errorf("camera %s is already running", req.CameraID)
		}
		// Stop existing camera before restarting
		if err := lifecycle.Stop(); err != nil {
			log.Warn().Err(err).Str("camera_id", req.CameraID).Msg("Failed to stop existing camera")
		}
		delete(cm.cameras, req.CameraID)
	}

	// Validate maximum cameras
	if len(cm.cameras) >= cm.cfg.MaxCameras {
		return fmt.Errorf("maximum number of cameras (%d) reached", cm.cfg.MaxCameras)
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

	// Create camera with configuration
	camera := &models.Camera{
		ID:        req.CameraID,
		URL:       req.URL,
		Projects:  req.Projects,
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

		// FPS Calculation setup
		RecentFrameTimes: make([]time.Time, 0, 30),
		FPSWindowSize:    30,
		AIFrameCounter:   0,

		// Generate MediaMTX URLs
		RTSPUrl:   cm.publisherSvc.GetRTSPURL(req.CameraID),
		WebRTCUrl: cm.publisherSvc.GetWebRTCURL(req.CameraID),
		HLSUrl:    cm.publisherSvc.GetHLSURL(req.CameraID),
		MJPEGUrl:  fmt.Sprintf("http://localhost:%d/mjpeg/%s", cm.cfg.Port, req.CameraID),
	}

	// Create lifecycle manager
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
		Strs("projects", req.Projects).
		Bool("ai_enabled", camera.AIEnabled).
		Str("ai_endpoint", camera.AIEndpoint).
		Dur("ai_timeout", camera.AITimeout).
		Str("rtsp_url", camera.RTSPUrl).
		Str("webrtc_url", camera.WebRTCUrl).
		Str("hls_url", camera.HLSUrl).
		Str("mjpeg_url", camera.MJPEGUrl).
		Msg("Camera started with production-grade lifecycle management")

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
		Projects:         camera.Projects,
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
			Projects:         camera.Projects,
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
		})
	}

	return cameras
}

// ServeHTTP serves MJPEG stream for a camera
func (cm *CameraManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract camera ID from URL path
	// URL format: /mjpeg/:camera_id
	path := r.URL.Path
	if len(path) < 8 || path[:7] != "/mjpeg/" {
		http.Error(w, "Invalid MJPEG URL format", http.StatusBadRequest)
		return
	}

	cameraID := path[7:] // Remove "/mjpeg/" prefix
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

	// Delegate to publisher service for actual MJPEG streaming
	if cm.publisherSvc == nil {
		http.Error(w, "Publisher service not available", http.StatusServiceUnavailable)
		return
	}

	cm.publisherSvc.StreamMJPEGHTTP(w, r, cameraID)
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

	// Update projects
	if req.Projects != nil {
		camera.Projects = req.Projects
	}

	// Update AI configuration
	if req.AIEnabled != nil && *req.AIEnabled != camera.AIEnabled {
		oldAIEnabled := camera.AIEnabled
		camera.AIEnabled = *req.AIEnabled
		needsRestart = true
		log.Info().
			Str("camera_id", cameraID).
			Bool("old_ai_enabled", oldAIEnabled).
			Bool("new_ai_enabled", camera.AIEnabled).
			Msg("AI enabled state changed, will restart camera")
	}

	if req.AIEndpoint != nil && *req.AIEndpoint != camera.AIEndpoint {
		oldAIEndpoint := camera.AIEndpoint
		camera.AIEndpoint = *req.AIEndpoint
		needsRestart = true
		log.Info().
			Str("camera_id", cameraID).
			Str("old_ai_endpoint", oldAIEndpoint).
			Str("new_ai_endpoint", camera.AIEndpoint).
			Msg("AI endpoint changed, will restart camera")
	}

	// Update recording settings (no restart needed)
	if req.EnableRecord != nil {
		camera.EnableRecord = *req.EnableRecord
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
			Msg("Restarting camera due to configuration changes")

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

// RestartCamera performs a zero-downtime restart of the camera
func (cm *CameraManager) RestartCamera(cameraID string) error {
	cm.mutex.RLock()
	lifecycle, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	log.Info().Str("camera_id", cameraID).Msg("Performing zero-downtime restart")

	return lifecycle.Restart()
}

// ForceRestartCamera forces a restart of the camera (for recovery from stuck states)
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

// Safe channel closing helper functions to prevent "close of closed channel" panics

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

// ValidateRTSPAndCaptureThumbnail validates RTSP stream and captures HD thumbnail
func (cm *CameraManager) ValidateRTSPAndCaptureThumbnail(rtspURL string) *models.RTSPCheckResponse {
	response := &models.RTSPCheckResponse{
		Valid:   false,
		Message: "RTSP stream validation failed",
	}

	log.Info().Str("rtsp_url", rtspURL).Msg("Starting RTSP stream validation")

	// Try to open the RTSP stream
	cap, err := gocv.OpenVideoCapture(rtspURL)
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to open RTSP stream: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer cap.Close()

	// Check if capture is opened
	if !cap.IsOpened() {
		response.ErrorDetail = "RTSP stream could not be opened"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	// Set timeout for frame reading (5 seconds)
	timeout := time.After(5 * time.Second)
	frameReadSuccess := make(chan bool, 1)

	var frame gocv.Mat
	var actualFPS, actualWidth, actualHeight float64

	// Try to read frame in goroutine to handle timeout
	go func() {
		frame = gocv.NewMat()

		// Try to read a few frames to ensure stream is working
		for i := 0; i < 3; i++ {
			if cap.Read(&frame) && !frame.Empty() {
				frameReadSuccess <- true
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		frameReadSuccess <- false
	}()

	// Wait for frame read or timeout
	select {
	case success := <-frameReadSuccess:
		if !success {
			frame.Close()
			response.ErrorDetail = "Failed to read frames from RTSP stream"
			log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
			return response
		}
	case <-timeout:
		frame.Close()
		response.ErrorDetail = "Timeout reading from RTSP stream"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	defer frame.Close()

	// Get stream properties
	actualFPS = cap.Get(gocv.VideoCaptureFPS)
	actualWidth = cap.Get(gocv.VideoCaptureFrameWidth)
	actualHeight = cap.Get(gocv.VideoCaptureFrameHeight)

	// Validate frame dimensions
	if frame.Cols() <= 0 || frame.Rows() <= 0 {
		response.ErrorDetail = "Invalid frame dimensions"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	// Resize to HD if needed (1280x720 for HD thumbnail)
	hdWidth, hdHeight := 1280, 720
	var resizedFrame gocv.Mat

	if frame.Cols() != hdWidth || frame.Rows() != hdHeight {
		resizedFrame = gocv.NewMat()
		gocv.Resize(frame, &resizedFrame, image.Pt(hdWidth, hdHeight), 0, 0, gocv.InterpolationLinear)
	} else {
		resizedFrame = frame.Clone()
	}
	defer resizedFrame.Close()

	// Convert to JPEG with high quality
	jpegData, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, resizedFrame, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to encode thumbnail: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer jpegData.Close()

	// Convert to base64 data URL
	thumbnail := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData.GetBytes())

	// Success response
	response.Valid = true
	response.Message = "RTSP stream is valid and accessible"
	response.Thumbnail = thumbnail
	response.Width = int(actualWidth)
	response.Height = int(actualHeight)
	response.FPS = actualFPS
	response.ErrorDetail = ""

	log.Info().
		Str("rtsp_url", rtspURL).
		Int("width", response.Width).
		Int("height", response.Height).
		Float64("fps", response.FPS).
		Msg("RTSP stream validation successful")

	return response
}
