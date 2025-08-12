package camera

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"image"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/detection"
)

// CameraManager manages the entire camera pipeline
type CameraManager struct {
	cfg    *config.Config
	detSvc *detection.Service

	cameras map[string]*models.Camera
	mutex   sync.RWMutex

	// Pipeline components
	frameProcessor *FrameProcessor
	publisher      *Publisher

	stopChannel chan struct{}
}

// NewCameraManager creates a new camera manager with full pipeline
func NewCameraManager(cfg *config.Config, detSvc *detection.Service) (*CameraManager, error) {
	// Create pipeline components
	frameProcessor, err := NewFrameProcessor(cfg, detSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create frame processor: %w", err)
	}

	publisher, err := NewPublisher(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	cm := &CameraManager{
		cfg:            cfg,
		detSvc:         detSvc,
		cameras:        make(map[string]*models.Camera),
		frameProcessor: frameProcessor,
		publisher:      publisher,
		stopChannel:    make(chan struct{}),
	}

	log.Info().
		Int("max_cameras", cfg.MaxCameras).
		Int("max_fps_no_ai", cfg.MaxFPSNoAI).
		Int("max_fps_with_ai", cfg.MaxFPSWithAI).
		Bool("ai_enabled", cfg.AIEnabled).
		Msg("Camera manager initialized with enterprise pipeline")

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
	if req.AITimeout != nil && *req.AITimeout != "" {
		if parsed, err := time.ParseDuration(*req.AITimeout); err == nil {
			aiTimeout = parsed
		} else {
			log.Warn().
				Str("camera_id", req.CameraID).
				Str("invalid_timeout", *req.AITimeout).
				Msg("Invalid AI timeout format, using default")
		}
	}

	// Create camera with pipeline channels
	camera := &models.Camera{
		ID:        req.CameraID,
		URL:       req.URL,
		Projects:  req.Projects,
		IsActive:  true,
		CreatedAt: time.Now(),

		// AI Configuration
		AIEnabled:  aiEnabled,
		AIEndpoint: aiEndpoint,
		AITimeout:  aiTimeout,

		RawFrames:       make(chan *models.RawFrame, cm.cfg.FrameBufferSize),
		ProcessedFrames: make(chan *models.ProcessedFrame, cm.cfg.PublishingBuffer),
		StopChannel:     make(chan struct{}),

		// Generate MediaMTX URLs
		RTSPUrl:   fmt.Sprintf("rtsp://localhost:8554/%s", req.CameraID),
		WebRTCUrl: fmt.Sprintf("http://localhost:8889/%s/whep", req.CameraID),
		HLSUrl:    fmt.Sprintf("http://localhost:8888/%s/index.m3u8", req.CameraID),
		MJPEGUrl:  fmt.Sprintf("http://localhost:%d/mjpeg/%s", cm.cfg.Port, req.CameraID),
	}

	cm.cameras[req.CameraID] = camera

	// Start pipeline components for this camera
	go cm.runStreamReader(camera)
	go cm.runFrameProcessor(camera)
	go cm.runPublisher(camera)

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
		}
	}()

	for {
		select {
		case <-camera.StopChannel:
			return
		default:
			err := cm.startVideoCaptureProcess(camera)
			if err != nil {
				log.Error().Err(err).Str("camera_id", camera.ID).Msg("VideoCapture process failed")
				camera.ErrorCount++

				// Wait before retry
				select {
				case <-camera.StopChannel:
					return
				case <-time.After(cm.cfg.ReconnectInterval):
					continue
				}
			}
		}
	}
}

// startVideoCaptureProcess starts OpenCV VideoCapture similar
func (cm *CameraManager) startVideoCaptureProcess(camera *models.Camera) error {
	log.Info().
		Str("camera_id", camera.ID).
		Str("url", camera.URL).
		Msg("Starting OpenCV VideoCapture with optimized settings")

	// Create VideoCapture - handle webcam vs RTSP
	var cap *gocv.VideoCapture
	var err error

	// RTSP case
	log.Info().
		Str("camera_id", camera.ID).
		Str("rtsp_url", camera.URL).
		Msg("Opening RTSP stream")

	cap, err = gocv.OpenVideoCapture(camera.URL)
	if err != nil {
		return fmt.Errorf("failed to open RTSP stream %s: %w", camera.URL, err)
	}
	defer cap.Close()

	// Set RTSP properties for low latency
	cap.Set(gocv.VideoCaptureBufferSize, 1) // Minimal buffer
	cap.Set(gocv.VideoCaptureFrameWidth, float64(cm.cfg.OutputWidth))
	cap.Set(gocv.VideoCaptureFrameHeight, float64(cm.cfg.OutputHeight))

	if !cap.IsOpened() {
		return fmt.Errorf("video capture is not opened for camera %s", camera.ID)
	}

	// Get actual properties
	actualFPS := cap.Get(gocv.VideoCaptureFPS)
	actualWidth := cap.Get(gocv.VideoCaptureFrameWidth)
	actualHeight := cap.Get(gocv.VideoCaptureFrameHeight)

	log.Info().
		Str("camera_id", camera.ID).
		Float64("actual_fps", actualFPS).
		Float64("actual_width", actualWidth).
		Float64("actual_height", actualHeight).
		Msg("VideoCapture opened successfully with actual properties")

	// Frame reading loop
	frameID := int64(0)
	img := gocv.NewMat()
	defer img.Close()

	consecutiveErrors := 0
	maxConsecutiveErrors := 10

	for {
		select {
		case <-camera.StopChannel:
			log.Info().Str("camera_id", camera.ID).Msg("Stopping VideoCapture reader due to stop signal")
			return nil
		default:
			ok := cap.Read(&img)

			if !ok {
				consecutiveErrors++
				log.Warn().
					Str("camera_id", camera.ID).
					Int("consecutive_errors", consecutiveErrors).
					Msg("Failed to read frame from VideoCapture")

				if consecutiveErrors >= maxConsecutiveErrors {
					return fmt.Errorf("too many consecutive frame read errors (%d)", consecutiveErrors)
				}

				// Small delay before retry
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if img.Empty() {
				consecutiveErrors++
				log.Warn().
					Str("camera_id", camera.ID).
					Int("consecutive_errors", consecutiveErrors).
					Msg("Received empty frame from VideoCapture")

				if consecutiveErrors >= maxConsecutiveErrors {
					return fmt.Errorf("too many consecutive empty frames (%d)", consecutiveErrors)
				}
				continue
			}

			// Successfully read a frame
			consecutiveErrors = 0
			frameID++
			camera.FrameCount++
			camera.LastFrameTime = time.Now()

			processedImg := gocv.NewMat()
			if img.Cols() != cm.cfg.OutputWidth || img.Rows() != cm.cfg.OutputHeight {
				gocv.Resize(img, &processedImg, image.Pt(cm.cfg.OutputWidth, cm.cfg.OutputHeight), 0, 0, gocv.InterpolationLinear)
			} else {
				processedImg = img.Clone()
			}

			// Convert to bytes (BGR)
			frameData := processedImg.ToBytes()
			processedImg.Close()

			rawFrame := &models.RawFrame{
				CameraID:  camera.ID,
				Data:      frameData,
				Timestamp: time.Now(),
				FrameID:   frameID,
				Width:     cm.cfg.OutputWidth,
				Height:    cm.cfg.OutputHeight,
				Format:    "BGR24",
			}

			// Send to processing pipeline
			select {
			case camera.RawFrames <- rawFrame:
				log.Debug().
					Str("camera_id", camera.ID).
					Int64("frame_id", frameID).
					Msg("Frame sent to processing pipeline")
			default:

				// log.Warn().
				// 	Str("camera_id", camera.ID).
				// 	Int64("frame_id", frameID).
				// 	Msg("Dropped raw frame - processing buffer full")
			}

			targetInterval := time.Second / time.Duration(cm.getTargetFPS())
			time.Sleep(targetInterval)
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
		}
	}()

	// Create per-camera detection service if AI is enabled for this camera
	var detSvc *detection.Service
	if camera.AIEnabled && camera.AIEndpoint != "" {
		var err error
		detSvc, err = detection.NewService(camera.AIEndpoint)
		if err != nil {
			log.Error().
				Err(err).
				Str("camera_id", camera.ID).
				Str("ai_endpoint", camera.AIEndpoint).
				Msg("Failed to create detection service for camera, AI will be disabled")
			camera.AIEnabled = false
		} else {
			log.Info().
				Str("camera_id", camera.ID).
				Str("ai_endpoint", camera.AIEndpoint).
				Msg("AI detection service initialized for camera")
		}
	}

	// Create per-camera frame processor with AI service
	frameProcessor, err := NewFrameProcessorWithDetectionService(cm.cfg, detSvc, camera)
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
			if detSvc != nil {
				detSvc.Shutdown(context.Background())
			}
			return
		case rawFrame := <-camera.RawFrames:
			startTime := time.Now()

			// Update camera statistics first
			camera.FPS = cm.calculateFPS(camera)
			camera.Latency = time.Since(rawFrame.Timestamp)

			// Process frame with current stats and per-camera AI settings
			processedFrame := frameProcessor.ProcessFrame(rawFrame, camera.Projects, camera.FPS, camera.Latency)

			// Update camera AI statistics
			if processedFrame.AIDetections != nil {
				if aiResult, ok := processedFrame.AIDetections.(*AIProcessingResult); ok {
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
				Msg("Frame processed")

			// Send to publisher
			select {
			case camera.ProcessedFrames <- processedFrame:
			default:
				log.Warn().Str("camera_id", camera.ID).Msg("Dropped processed frame - buffer full")
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
		}
	}()

	for {
		select {
		case <-camera.StopChannel:
			return
		case processedFrame := <-camera.ProcessedFrames:
			err := cm.publisher.PublishFrame(processedFrame)
			if err != nil {
				log.Error().Err(err).Str("camera_id", camera.ID).Msg("Failed to publish frame")
				camera.ErrorCount++
			}
		}
	}
}

// getTargetFPS returns target FPS based on AI status
func (cm *CameraManager) getTargetFPS() int {
	if cm.cfg.AIEnabled && len(cm.cameras) > 0 {
		// Check if any camera has AI projects
		for _, camera := range cm.cameras {
			if len(camera.Projects) > 0 {
				return cm.cfg.MaxFPSWithAI
			}
		}
	}
	return cm.cfg.MaxFPSNoAI
}

// calculateFPS calculates current FPS for a camera
func (cm *CameraManager) calculateFPS(camera *models.Camera) float64 {
	// Simple FPS calculation - can be improved with moving average
	if camera.FrameCount < 2 {
		return 0
	}

	elapsed := time.Since(camera.CreatedAt).Seconds()
	if elapsed > 0 {
		return float64(camera.FrameCount) / elapsed
	}
	return 0
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

// stopCameraInternal stops camera internal processes
func (cm *CameraManager) stopCameraInternal(camera *models.Camera) {
	if camera == nil {
		return
	}

	camera.IsActive = false

	// Stop all processes
	close(camera.StopChannel)

	// Close channels
	close(camera.RawFrames)
	close(camera.ProcessedFrames)
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
		CameraID:      camera.ID,
		URL:           camera.URL,
		Projects:      camera.Projects,
		IsActive:      camera.IsActive,
		CreatedAt:     camera.CreatedAt,
		LastFrameTime: camera.LastFrameTime,
		FrameCount:    camera.FrameCount,
		ErrorCount:    camera.ErrorCount,
		FPS:           camera.FPS,
		Latency:       camera.Latency.String(),

		// AI Configuration
		AIEnabled:        camera.AIEnabled,
		AIEndpoint:       camera.AIEndpoint,
		AITimeout:        camera.AITimeout.String(),
		AIProcessingTime: camera.AIProcessingTime.String(),
		LastAIError:      camera.LastAIError,
		AIDetectionCount: camera.AIDetectionCount,

		RTSPUrl:   camera.RTSPUrl,
		WebRTCUrl: camera.WebRTCUrl,
		HLSUrl:    camera.HLSUrl,
		MJPEGUrl:  camera.MJPEGUrl,
	}, nil
}

// ListCameras returns all cameras
func (cm *CameraManager) ListCameras() []*models.CameraResponse {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	cameras := make([]*models.CameraResponse, 0, len(cm.cameras))
	for _, camera := range cm.cameras {
		resp, _ := cm.GetCamera(camera.ID)
		if resp != nil {
			cameras = append(cameras, resp)
		}
	}

	return cameras
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

// Shutdown shuts down the camera manager
func (cm *CameraManager) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down camera manager")

	// Stop all cameras
	cm.mutex.Lock()
	for _, camera := range cm.cameras {
		cm.stopCameraInternal(camera)
	}
	cm.cameras = make(map[string]*models.Camera)
	cm.mutex.Unlock()

	// Shutdown pipeline components
	if cm.frameProcessor != nil {
		cm.frameProcessor.Shutdown()
	}

	if cm.publisher != nil {
		cm.publisher.Shutdown()
	}

	close(cm.stopChannel)

	return nil
}

func (cm *CameraManager) PublisherStreamMJPEG(w http.ResponseWriter, r *http.Request, cameraID string) {
	if cm.publisher == nil {
		http.Error(w, "publisher not available", http.StatusServiceUnavailable)
		return
	}
	cm.publisher.StreamMJPEGHTTP(w, r, cameraID)
}

// UpdateCameraAI updates AI configuration for a specific camera
func (cm *CameraManager) UpdateCameraAI(cameraID string, req *models.AIConfigRequest) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera not found")
	}

	// Update AI enabled flag
	if req.AIEnabled != nil {
		camera.AIEnabled = *req.AIEnabled
	}

	// Update AI endpoint
	if req.AIEndpoint != nil && *req.AIEndpoint != "" {
		camera.AIEndpoint = *req.AIEndpoint
	}

	// Update AI timeout
	if req.AITimeout != nil && *req.AITimeout != "" {
		if parsed, err := time.ParseDuration(*req.AITimeout); err == nil {
			camera.AITimeout = parsed
		} else {
			log.Warn().
				Str("camera_id", cameraID).
				Str("invalid_timeout", *req.AITimeout).
				Msg("Invalid AI timeout format, keeping current value")
		}
	}

	// Update projects
	if req.Projects != nil {
		camera.Projects = req.Projects
	}

	log.Info().
		Str("camera_id", cameraID).
		Bool("ai_enabled", camera.AIEnabled).
		Str("ai_endpoint", camera.AIEndpoint).
		Dur("ai_timeout", camera.AITimeout).
		Strs("projects", camera.Projects).
		Msg("Camera AI configuration updated")

	return nil
}

// GetCameraAI gets AI configuration for a specific camera
func (cm *CameraManager) GetCameraAI(cameraID string) (*models.AIConfigResponse, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return nil, fmt.Errorf("camera not found")
	}

	return &models.AIConfigResponse{
		CameraID:         camera.ID,
		AIEnabled:        camera.AIEnabled,
		AIEndpoint:       camera.AIEndpoint,
		AITimeout:        camera.AITimeout.String(),
		Projects:         camera.Projects,
		AIProcessingTime: camera.AIProcessingTime.String(),
		LastAIError:      camera.LastAIError,
		AIDetectionCount: camera.AIDetectionCount,
	}, nil
}

// ToggleCameraAI toggles AI processing for a specific camera
func (cm *CameraManager) ToggleCameraAI(cameraID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera not found")
	}

	camera.AIEnabled = !camera.AIEnabled

	log.Info().
		Str("camera_id", cameraID).
		Bool("ai_enabled", camera.AIEnabled).
		Msg("Camera AI toggled")

	return nil
}
