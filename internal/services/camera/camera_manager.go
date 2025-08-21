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
	frameprocessing "kepler-worker-go/internal/services/frameprocessing"
	"kepler-worker-go/internal/services/postprocessing"
	"kepler-worker-go/internal/services/publisher"
	"kepler-worker-go/internal/services/recorder"
)

// CameraManager manages the entire camera pipeline
type CameraManager struct {
	cfg *config.Config

	cameras map[string]*models.Camera
	mutex   sync.RWMutex

	// Pipeline components
	frameProcessor *frameprocessing.FrameProcessor
	publisher      *Publisher         // MJPEG publisher
	publisherSvc   *publisher.Service // MediaMTX publisher service
	streamCapture  *StreamCapture
	recorder       *recorder.Service

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

	mjpegPublisher, err := NewPublisher(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create MJPEG publisher: %w", err)
	}

	streamCapture := NewStreamCapture(cfg)

	cm := &CameraManager{
		cfg:                   cfg,
		cameras:               make(map[string]*models.Camera),
		frameProcessor:        frameProcessor,
		publisher:             mjpegPublisher,
		publisherSvc:          publisherSvc,
		streamCapture:         streamCapture,
		recorder:              recorderSvc,
		stopChannel:           make(chan struct{}),
		postProcessingService: postProcessingSvc,
	}

	log.Info().
		Int("max_cameras", cfg.MaxCameras).
		Int("max_fps_no_ai", cfg.MaxFPSNoAI).
		Int("max_fps_with_ai", cfg.MaxFPSWithAI).
		Bool("ai_enabled", cfg.AIEnabled).
		Msg("Camera manager initialized with enterprise pipeline")

	// Start watchdog once
	cm.watchdogOnce.Do(func() { go cm.runWatchdog() })

	return cm, nil
}

// StartCamera starts a camera with full pipeline
func (cm *CameraManager) StartCamera(req *models.CameraRequest) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Check if camera already exists
	if camera, exists := cm.cameras[req.CameraID]; exists {
		if camera.IsActive {
			return fmt.Errorf("camera %s is already active", req.CameraID)
		}
		// Stop existing camera before restarting
		cm.stopCameraInternal(camera)
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

	// Create camera with pipeline channels
	camera := &models.Camera{
		ID:        req.CameraID,
		URL:       req.URL,
		Projects:  req.Projects,
		IsActive:  true,
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

		RawFrames:       make(chan *models.RawFrame, cm.cfg.FrameBufferSize),
		ProcessedFrames: make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer),
		AlertFrames:     make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer),
		RecorderFrames:  make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer),
		StopChannel:     make(chan struct{}),

		// Generate MediaMTX URLs
		RTSPUrl:   cm.publisherSvc.GetRTSPURL(req.CameraID),
		WebRTCUrl: cm.publisherSvc.GetWebRTCURL(req.CameraID),
		HLSUrl:    cm.publisherSvc.GetHLSURL(req.CameraID),
		MJPEGUrl:  fmt.Sprintf("http://localhost:%d/mjpeg/%s", cm.cfg.Port, req.CameraID),
	}

	cm.cameras[req.CameraID] = camera

	// Start recording for this camera only if enabled and not paused
	if cm.recorder != nil && enableRecord && status != models.CameraStatusPaused {
		if err := cm.recorder.StartRecording(req.CameraID); err != nil {
			log.Error().Err(err).Str("camera_id", req.CameraID).Msg("Failed to start recording")
		} else {
			camera.IsRecording = true
		}
	}

	// Start pipeline components for this camera
	go cm.runStreamReader(camera)
	go cm.runFrameProcessor(camera)
	go cm.runPublisher(camera)
	go cm.runPostProcessor(camera)
	// go cm.runRecorderProcessor(camera)

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
		Msg("Camera started with full enterprise pipeline and AI configuration")

	return nil
}

// runStreamReader runs OpenCV VideoCapture to read RTSP stream
func (cm *CameraManager) runStreamReader(camera *models.Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("OpenCV reader panic recovered")
			// slight delay before loop retries
			time.Sleep(cm.cfg.PanicRestartDelay)
		}
	}()

	// backoff state per camera
	var attempt int

	for {
		select {
		case <-camera.StopChannel:
			return
		default:
			err := cm.streamCapture.StartVideoCaptureProcess(camera)
			if err != nil {
				log.Error().Err(err).Str("camera_id", camera.ID).Msg("VideoCapture process failed")
				camera.ErrorCount++
				attempt++

				// jittered exponential backoff within configured min/max
				delay := cm.streamCapture.CalculateBackoffDelay(attempt)
				log.Info().Str("camera_id", camera.ID).Dur("retry_in", delay).Int("attempt", attempt).Msg("Reconnecting to camera")

				select {
				case <-camera.StopChannel:
					return
				case <-time.After(delay):
					continue
				}
			} else {
				// success path resets attempts
				attempt = 0
			}
		}
	}
}

// runFrameProcessor processes frames through AI and adds metadata
func (cm *CameraManager) runFrameProcessor(camera *models.Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("Frame processor panic recovered")
			// ensure we don't hot-loop restarts
			time.Sleep(cm.cfg.PanicRestartDelay)
		}
	}()

	// Create per-camera frame processor
	frameProcessor, err := frameprocessing.NewFrameProcessorWithCamera(cm.cfg, camera)
	if err != nil {
		log.Error().
			Err(err).
			Str("camera_id", camera.ID).
			Msg("Failed to create frame processor for camera")
		return
	}
	defer frameProcessor.Shutdown()
	for {
		select {
		case <-camera.StopChannel:
			return
		case rawFrame := <-camera.RawFrames:
			// FRAME SKIPPING: Always get the latest frame by draining the buffer
			var latestFrame *models.RawFrame = rawFrame
			skippedFrames := 0

			// Drain all pending frames to get the absolute latest
		DrainLoop:
			for {
				select {
				case newerFrame := <-camera.RawFrames:
					latestFrame = newerFrame
					skippedFrames++
				default:
					break DrainLoop
				}
			}

			if skippedFrames > 0 {
				log.Debug().
					Str("camera_id", camera.ID).
					Int("skipped_frames", skippedFrames).
					Int64("latest_frame_id", latestFrame.FrameID).
					Msg("Skipped frames for real-time processing")
			}

			startTime := time.Now()

			// Update camera statistics first
			camera.FPS = cm.streamCapture.CalculateFPS(camera)
			camera.Latency = time.Since(latestFrame.Timestamp)

			// Process the latest frame with current stats and per-camera AI settings
			processedFrame := frameProcessor.ProcessFrame(latestFrame, camera.Projects, camera.FPS, camera.Latency)

			// Update camera AI statistics
			if processedFrame.AIDetections != nil {
				if aiResult, ok := processedFrame.AIDetections.(*frameprocessing.AIProcessingResult); ok {
					camera.AIProcessingTime = aiResult.ProcessingTime
					camera.AIDetectionCount += int64(len(aiResult.Detections))

					if aiResult.ErrorMessage != "" {
						camera.LastAIError = aiResult.ErrorMessage
						camera.ErrorCount++
					} else if aiResult.FrameProcessed {
						camera.LastAIError = "" // Clear error on successful processing
					}
				}
			}

			processingTime := time.Since(startTime)
			log.Debug().
				Str("camera_id", camera.ID).
				Dur("processing_time", processingTime).
				Bool("ai_enabled", camera.AIEnabled).
				Int("skipped_frames", skippedFrames).
				Msg("Frame processed with real-time optimization")

			// Send to publisher
			select {
			case camera.ProcessedFrames <- processedFrame:
			default:
				// Drop frame if buffer is full for real-time streaming
			}

			// Send to alerts
			select {
			case camera.AlertFrames <- processedFrame:
			default:
				// Drop frame if buffer is full
			}

			// Send to recorder
			select {
			case camera.RecorderFrames <- processedFrame:
			default:
				// Drop frame if buffer is full
			}
		}
	}
}

// runPublisher publishes frames to MediaMTX
func (cm *CameraManager) runPublisher(camera *models.Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("Publisher panic recovered")
			// ensure we don't hot-loop restarts
			time.Sleep(cm.cfg.PanicRestartDelay)
		}
	}()

	for {
		select {
		case <-camera.StopChannel:
			return
		case processedFrame := <-camera.ProcessedFrames:
			// Publish to MJPEG
			err := cm.publisher.PublishFrame(processedFrame)
			if err != nil {
				log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to publish MJPEG frame")
				camera.ErrorCount++
			}

			// // Publish to MediaMTX via publisher service
			// err = cm.publisherSvc.PublishFrame(processedFrame)
			// if err != nil {
			// 	log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to publish MediaMTX frame")
			// 	// Don't increment error count for MediaMTX failures as it's secondary
			// }
		}
	}
}

// runPostProcessor processes frames for alerts in parallel to publishing
func (cm *CameraManager) runPostProcessor(camera *models.Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("Post processor panic recovered")
			// ensure we don't hot-loop restarts
			time.Sleep(cm.cfg.PanicRestartDelay)
		}
	}()

	for {
		select {
		case <-camera.StopChannel:
			return
		case processedFrame := <-camera.AlertFrames:
			// Only process alerts if AI is enabled for this camera
			if !camera.AIEnabled {
				continue
			}

			// Use new post-processing service with detection-only processing
			if cm.postProcessingService != nil {
				aiResult, ok := processedFrame.AIDetections.(*frameprocessing.AIProcessingResult)
				if !ok || len(aiResult.Detections) == 0 {
					continue
				}

				// No conversion needed! Frame processor already outputs models.Detection
				detections := aiResult.Detections

				// Create frame metadata
				frameMetadata := models.FrameMetadata{
					FrameID:     processedFrame.FrameID,
					Timestamp:   processedFrame.Timestamp,
					Width:       processedFrame.Width,
					Height:      processedFrame.Height,
					AllDetCount: len(aiResult.Detections),
					CameraID:    camera.ID,
				}

				// Use new detection-only processing with both raw and annotated frame data
				result := cm.postProcessingService.ProcessDetections(
					camera.ID,
					detections,
					frameMetadata,
					processedFrame.RawData, // Raw frame data for clean crops
					processedFrame.Data,    // Annotated frame data for context images
				)

				// Log processing results
				if len(result.Errors) > 0 {
					log.Warn().
						Str("camera_id", camera.ID).
						Strs("errors", result.Errors).
						Msg("Alert processing had errors")
				}

				if result.AlertsCreated > 0 {
					log.Debug().
						Str("camera_id", camera.ID).
						Int("alerts_created", result.AlertsCreated).
						Int("valid_detections", result.ValidDetections).
						Int("suppressed_detections", result.SuppressedDetections).
						Msg("Alert processing completed")
				}
			}
		}
	}
}

// runRecorderProcessor processes frames for video recording in parallel
func (cm *CameraManager) runRecorderProcessor(camera *models.Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("Recorder processor panic recovered")
			time.Sleep(cm.cfg.PanicRestartDelay)
		}
	}()

	for {
		select {
		case <-camera.StopChannel:
			// Stop recording when camera stops
			if cm.recorder != nil {
				if err := cm.recorder.StopRecording(camera.ID); err != nil {
					log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to stop recording")
				}
			}
			return
		case processedFrame := <-camera.RecorderFrames:
			// Only process frames if recording is enabled and camera is in recording state
			if !camera.EnableRecord || camera.Status == models.CameraStatusPaused || camera.Status == models.CameraStatusStop || !camera.IsRecording {
				// Skip frame processing when recording is disabled or camera is paused/stopped
				continue
			}

			// Send the actual frame data to recorder for processing
			if cm.recorder != nil {
				if err := cm.recorder.ProcessFrame(camera.ID, processedFrame); err != nil {
					// Only log as debug if it's just that recording is not active (expected behavior)
					if err.Error() == fmt.Sprintf("camera %s is not recording", camera.ID) {
						log.Debug().Str("camera_id", camera.ID).Msg("Skipping frame - recording not active")
					} else {
						log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to process frame for recording")
					}
				}
			}
		}
	}
}

// Shutdown gracefully shuts down the camera manager
func (cm *CameraManager) Shutdown(ctx context.Context) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	log.Info().Msg("Shutting down camera manager")

	// Stop all cameras
	for _, camera := range cm.cameras {
		cm.stopCameraInternal(camera)
	}

	return nil
}

// stopCameraInternal stops camera internal processes
func (cm *CameraManager) stopCameraInternal(camera *models.Camera) {
	if camera == nil {
		return
	}

	camera.IsActive = false

	// Stop MediaMTX stream for this camera
	if err := cm.publisherSvc.StopStream(camera.ID); err != nil {
		log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to stop MediaMTX stream")
	}

	// Safely close all channels
	cm.safeCloseStopChannel(camera.StopChannel, camera.ID)
	cm.safeCloseRawFrames(camera.RawFrames, camera.ID)
	cm.safeCloseProcessedFrames(camera.ProcessedFrames, camera.ID)
	cm.safeCloseAlertFrames(camera.AlertFrames, camera.ID)
	cm.safeCloseRecorderFrames(camera.RecorderFrames, camera.ID)
}

// GetCamera returns camera information
func (cm *CameraManager) GetCamera(cameraID string) (*models.CameraResponse, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return nil, fmt.Errorf("camera %s not found", cameraID)
	}

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

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	cm.stopCameraInternal(camera)
	delete(cm.cameras, cameraID)

	log.Info().Str("camera_id", cameraID).Msg("Camera stopped successfully")
	return nil
}

// GetStats returns camera statistics
func (cm *CameraManager) GetStats() (int, int) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	active := 0
	for _, camera := range cm.cameras {
		if camera.IsActive {
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
	for _, camera := range cm.cameras {
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
	camera, exists := cm.cameras[cameraID]
	cm.mutex.RUnlock()

	if !exists {
		http.Error(w, fmt.Sprintf("Camera %s not found", cameraID), http.StatusNotFound)
		return
	}

	if !camera.IsActive {
		http.Error(w, fmt.Sprintf("Camera %s is not active", cameraID), http.StatusServiceUnavailable)
		return
	}

	// Delegate to publisher for actual MJPEG streaming
	if cm.publisher == nil {
		http.Error(w, "Publisher service not available", http.StatusServiceUnavailable)
		return
	}

	cm.publisher.StreamMJPEGHTTP(w, r, cameraID)
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
	for _, camera := range cm.cameras {
		if camera.IsActive && !camera.LastFrameTime.IsZero() {
			timeSinceLastFrame := now.Sub(camera.LastFrameTime)
			if timeSinceLastFrame > cm.cfg.FrameStaleThreshold {
				log.Warn().
					Str("camera_id", camera.ID).
					Dur("time_since_last_frame", timeSinceLastFrame).
					Msg("Camera appears to be stale - no recent frames")
			}
		}
	}
}

// UpdateCameraSettings updates camera settings dynamically and restarts components as needed
func (cm *CameraManager) UpdateCameraSettings(cameraID string, req *models.CameraUpsertRequest) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	var needsStreamRestart bool
	var needsFrameProcessorRestart bool
	var needsCameraStop bool = false
	var needsCameraStart bool = false

	// Update URL if provided (requires restart of stream reader)
	if req.URL != nil && *req.URL != camera.URL {
		oldURL := camera.URL
		camera.URL = *req.URL
		needsStreamRestart = true

		log.Info().
			Str("camera_id", cameraID).
			Str("old_url", oldURL).
			Str("new_url", *req.URL).
			Msg("Camera URL updated, will restart stream reader")
	}

	// Update projects
	if req.Projects != nil {
		camera.Projects = req.Projects
	}

	// Update AI configuration
	aiChanged := false
	oldAIEnabled := camera.AIEnabled
	oldAIEndpoint := camera.AIEndpoint
	oldAITimeout := camera.AITimeout

	if req.AIEnabled != nil && *req.AIEnabled != camera.AIEnabled {
		camera.AIEnabled = *req.AIEnabled
		aiChanged = true
		log.Info().
			Str("camera_id", cameraID).
			Bool("old_ai_enabled", oldAIEnabled).
			Bool("new_ai_enabled", camera.AIEnabled).
			Msg("AI enabled state changed")
	}

	if req.AIEndpoint != nil && *req.AIEndpoint != camera.AIEndpoint {
		camera.AIEndpoint = *req.AIEndpoint
		aiChanged = true
		log.Info().
			Str("camera_id", cameraID).
			Str("old_ai_endpoint", oldAIEndpoint).
			Str("new_ai_endpoint", camera.AIEndpoint).
			Msg("AI endpoint changed")
	}

	if aiChanged {
		// Do not restart components on AI changes; frame processor lazily refreshes gRPC
		needsFrameProcessorRestart = false
		log.Info().
			Str("camera_id", cameraID).
			Bool("old_ai_enabled", oldAIEnabled).
			Bool("new_ai_enabled", camera.AIEnabled).
			Str("old_ai_endpoint", oldAIEndpoint).
			Str("new_ai_endpoint", camera.AIEndpoint).
			Dur("old_ai_timeout", oldAITimeout).
			Dur("new_ai_timeout", camera.AITimeout).
			Msg("AI configuration updated without restart (lazy refresh)")
	}

	// Update recording settings
	if req.EnableRecord != nil {
		oldEnableRecord := camera.EnableRecord
		camera.EnableRecord = *req.EnableRecord

		// Handle recording state change
		if *req.EnableRecord && !oldEnableRecord && camera.Status != "paused" && camera.IsActive {
			// Enable recording
			if cm.recorder != nil && !camera.IsRecording {
				if err := cm.recorder.StartRecording(cameraID); err != nil {
					log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to start recording after enable")
				} else {
					camera.IsRecording = true
				}
			}
		} else if !*req.EnableRecord && oldEnableRecord {
			// Disable recording
			if cm.recorder != nil && camera.IsRecording {
				if err := cm.recorder.StopRecording(cameraID); err != nil {
					log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop recording after disable")
				} else {
					camera.IsRecording = false
				}
			}
		}
	}

	// Update status
	if req.Status != nil {
		oldStatus := camera.Status
		camera.Status = *req.Status
		camera.IsPaused = camera.Status == models.CameraStatusPaused

		// Check if camera needs to be completely stopped
		if camera.Status == models.CameraStatusStop {
			needsCameraStop = true
			log.Info().
				Str("camera_id", cameraID).
				Str("old_status", oldStatus.String()).
				Str("new_status", camera.Status.String()).
				Msg("Camera status changed to stop - will completely stop camera")
		}

		// Check if camera needs to be started (was stopped and now starting)
		if oldStatus == models.CameraStatusStop && camera.Status == models.CameraStatusStart && !camera.IsActive {
			needsCameraStart = true
			log.Info().
				Str("camera_id", cameraID).
				Str("old_status", oldStatus.String()).
				Str("new_status", camera.Status.String()).
				Msg("Camera status changed from stop to start - will restart camera")
		}

		// Handle status change effects on recording
		if camera.EnableRecord && cm.recorder != nil {
			switch camera.Status {
			case models.CameraStatusStart:
				if oldStatus == models.CameraStatusPaused && !camera.IsRecording {
					// Resume from pause
					if err := cm.recorder.StartRecording(cameraID); err != nil {
						log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to resume recording")
					} else {
						camera.IsRecording = true
					}
				}
			case models.CameraStatusPaused:
				if camera.IsRecording {
					// Pause recording
					if err := cm.recorder.StopRecording(cameraID); err != nil {
						log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to pause recording")
					} else {
						camera.IsRecording = false
					}
				}
			case models.CameraStatusStop:
				if camera.IsRecording {
					// Stop recording
					if err := cm.recorder.StopRecording(cameraID); err != nil {
						log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop recording")
					} else {
						camera.IsRecording = false
					}
				}
			}
		}
	}

	// Handle camera stop signal - completely stop the camera
	if needsCameraStop {
		log.Info().
			Str("camera_id", cameraID).
			Msg("Stopping camera completely due to status change")

		// Stop camera internal processes
		cm.stopCameraInternal(camera)

		// Don't delete from cameras map as we want to keep the configuration
		// Just mark as inactive
		camera.IsActive = false

		log.Info().
			Str("camera_id", cameraID).
			Str("status", camera.Status.String()).
			Bool("is_active", camera.IsActive).
			Msg("Camera stopped successfully due to status change")

		return nil
	}

	// Handle camera start signal - restart stopped camera
	if needsCameraStart {
		log.Info().
			Str("camera_id", cameraID).
			Msg("Starting camera due to status change from stop to start")

		// Reset camera state
		camera.IsActive = true
		camera.FrameCount = 0
		camera.ErrorCount = 0
		camera.LastFrameTime = time.Time{}
		camera.RecentFrameTimes = make([]time.Time, 0, 30)
		camera.AIFrameCounter = 0
		camera.AIDetectionCount = 0
		camera.LastAIError = ""

		// Create new channels
		camera.RawFrames = make(chan *models.RawFrame, cm.cfg.FrameBufferSize)
		camera.ProcessedFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
		camera.AlertFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
		camera.RecorderFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
		camera.StopChannel = make(chan struct{})

		// Start recording if enabled
		if cm.recorder != nil && camera.EnableRecord {
			if err := cm.recorder.StartRecording(cameraID); err != nil {
				log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to start recording after camera start")
			} else {
				camera.IsRecording = true
			}
		}

		// Start all pipeline components
		go cm.runStreamReader(camera)
		go cm.runFrameProcessor(camera)
		go cm.runPublisher(camera)
		go cm.runPostProcessor(camera)
		// go cm.runRecorderProcessor(camera)

		log.Info().
			Str("camera_id", cameraID).
			Str("status", camera.Status.String()).
			Bool("is_active", camera.IsActive).
			Bool("is_recording", camera.IsRecording).
			Msg("Camera started successfully due to status change")

		return nil
	}

	// Apply component restarts if needed
	if needsStreamRestart || needsFrameProcessorRestart {
		log.Info().
			Str("camera_id", cameraID).
			Bool("restart_stream", needsStreamRestart).
			Bool("restart_frame_processor", needsFrameProcessorRestart).
			Msg("Restarting camera components due to configuration changes")

		// Use internal restart without resetting statistics
		if err := cm.restartCameraInternal(camera, false); err != nil {
			log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to restart camera components")
			return fmt.Errorf("failed to restart camera components: %w", err)
		}
	}

	log.Info().
		Str("camera_id", cameraID).
		Str("status", camera.Status.String()).
		Bool("enable_record", camera.EnableRecord).
		Bool("is_recording", camera.IsRecording).
		Bool("is_paused", camera.IsPaused).
		Bool("components_restarted", needsStreamRestart || needsFrameProcessorRestart).
		Msg("Camera settings updated dynamically")

	return nil
}

// restartCameraInternal performs camera restart with optional statistics reset
func (cm *CameraManager) restartCameraInternal(camera *models.Camera, resetStats bool) error {
	log.Info().
		Str("camera_id", camera.ID).
		Bool("reset_stats", resetStats).
		Bool("ai_enabled", camera.AIEnabled).
		Str("ai_endpoint", camera.AIEndpoint).
		Msg("Starting camera restart")

	// Stop the camera components
	cm.stopCameraInternal(camera)

	// Wait a moment for graceful shutdown
	time.Sleep(200 * time.Millisecond)

	// Create new channels
	camera.RawFrames = make(chan *models.RawFrame, cm.cfg.FrameBufferSize)
	camera.ProcessedFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
	camera.AlertFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
	camera.RecorderFrames = make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer)
	camera.StopChannel = make(chan struct{})

	// Reset camera state if requested (for hard restart)
	if resetStats {
		camera.FrameCount = 0
		camera.ErrorCount = 0
		camera.LastFrameTime = time.Time{}
		camera.RecentFrameTimes = make([]time.Time, 0, 30)
		camera.AIFrameCounter = 0
		camera.AIDetectionCount = 0
		camera.LastAIError = ""
		log.Debug().Str("camera_id", camera.ID).Msg("Camera statistics reset")
	}

	// Set camera as active
	camera.IsActive = true

	// Restart recording if enabled and not paused
	if cm.recorder != nil && camera.EnableRecord && camera.Status != models.CameraStatusPaused {
		if err := cm.recorder.StartRecording(camera.ID); err != nil {
			log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to start recording after restart")
		} else {
			camera.IsRecording = true
		}
	} else {
		camera.IsRecording = false
	}

	// Restart all pipeline components
	go cm.runStreamReader(camera)
	go cm.runFrameProcessor(camera)
	go cm.runPublisher(camera)
	go cm.runPostProcessor(camera)
	go cm.runRecorderProcessor(camera)

	log.Info().
		Str("camera_id", camera.ID).
		Bool("reset_stats", resetStats).
		Bool("ai_enabled", camera.AIEnabled).
		Bool("recording_enabled", camera.IsRecording).
		Msg("Camera restart completed successfully")

	return nil
}

// RestartCamera performs a hard restart of the entire camera pipeline
func (cm *CameraManager) RestartCamera(cameraID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	log.Info().Str("camera_id", cameraID).Msg("Performing hard restart of camera")

	// Use internal restart with statistics reset
	return cm.restartCameraInternal(camera, true)
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

func (cm *CameraManager) safeCloseRawFrames(ch chan *models.RawFrame, cameraID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().Str("camera_id", cameraID).Msg("RawFrames channel already closed")
		}
	}()
	close(ch)
}

func (cm *CameraManager) safeCloseProcessedFrames(ch chan *models.ProcessedFrame, cameraID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().Str("camera_id", cameraID).Msg("ProcessedFrames channel already closed")
		}
	}()
	close(ch)
}

func (cm *CameraManager) safeCloseAlertFrames(ch chan *models.ProcessedFrame, cameraID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().Str("camera_id", cameraID).Msg("AlertFrames channel already closed")
		}
	}()
	close(ch)
}

func (cm *CameraManager) safeCloseRecorderFrames(ch chan *models.ProcessedFrame, cameraID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().Str("camera_id", cameraID).Msg("RecorderFrames channel already closed")
		}
	}()
	close(ch)
}
