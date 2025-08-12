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
	"kepler-worker-go/internal/services/detection"
)

// CameraManager manages the entire camera pipeline
type CameraManager struct {
	cfg    *config.Config
	detSvc *detection.Service

	cameras map[string]*Camera
	mutex   sync.RWMutex

	// Pipeline components
	frameProcessor *FrameProcessor
	publisher      *Publisher

	stopChannel chan struct{}
}

// Camera represents a single camera with its pipeline
type Camera struct {
	ID        string
	URL       string
	Projects  []string
	IsActive  bool
	CreatedAt time.Time

	// Statistics
	FrameCount    int64
	ErrorCount    int64
	LastFrameTime time.Time
	FPS           float64
	Latency       time.Duration

	// Pipeline channels
	rawFrames       chan *RawFrame
	processedFrames chan *ProcessedFrame

	// Control
	stopChannel chan struct{}

	// URLs
	RTSPUrl   string
	WebRTCUrl string
	HLSUrl    string
	MJPEGUrl  string
}

// RawFrame represents a frame from OpenCV
type RawFrame struct {
	CameraID  string
	Data      []byte
	Timestamp time.Time
	FrameID   int64
	Width     int
	Height    int
	Format    string
}

// ProcessedFrame represents a frame after AI processing and metadata overlay
type ProcessedFrame struct {
	CameraID  string
	Data      []byte
	Timestamp time.Time
	FrameID   int64
	Width     int
	Height    int

	// Metadata
	FPS          float64
	Latency      time.Duration
	AIEnabled    bool
	AIDetections interface{} // Will contain detection results

	// Quality
	Quality int
	Bitrate int
}

// CameraRequest for API
type CameraRequest struct {
	CameraID string   `json:"camera_id" binding:"required"`
	URL      string   `json:"url" binding:"required"`
	Projects []string `json:"projects"`
}

// CameraResponse for API
type CameraResponse struct {
	CameraID      string    `json:"camera_id"`
	URL           string    `json:"url"`
	Projects      []string  `json:"projects"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	LastFrameTime time.Time `json:"last_frame_time"`
	FrameCount    int64     `json:"frame_count"`
	ErrorCount    int64     `json:"error_count"`
	FPS           float64   `json:"fps"`
	Latency       string    `json:"latency"`

	// Streaming URLs
	RTSPUrl   string `json:"rtsp_url"`
	WebRTCUrl string `json:"webrtc_url"`
	HLSUrl    string `json:"hls_url"`
	MJPEGUrl  string `json:"mjpeg_url"`
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
		cameras:        make(map[string]*Camera),
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
func (cm *CameraManager) StartCamera(req *CameraRequest) error {
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

	// Create camera with pipeline channels
	camera := &Camera{
		ID:        req.CameraID,
		URL:       req.URL,
		Projects:  req.Projects,
		IsActive:  true,
		CreatedAt: time.Now(),

		rawFrames:       make(chan *RawFrame, cm.cfg.FrameBufferSize),
		processedFrames: make(chan *ProcessedFrame, cm.cfg.PublishingBuffer),
		stopChannel:     make(chan struct{}),

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
		Str("rtsp_url", camera.RTSPUrl).
		Str("webrtc_url", camera.WebRTCUrl).
		Str("hls_url", camera.HLSUrl).
		Msg("Camera started with full enterprise pipeline")

	return nil
}

// runStreamReader runs OpenCV VideoCapture to read RTSP stream
func (cm *CameraManager) runStreamReader(camera *Camera) {
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
		case <-camera.stopChannel:
			return
		default:
			err := cm.startVideoCaptureProcess(camera)
			if err != nil {
				log.Error().Err(err).Str("camera_id", camera.ID).Msg("VideoCapture process failed")
				camera.ErrorCount++

				// Wait before retry
				select {
				case <-camera.stopChannel:
					return
				case <-time.After(cm.cfg.ReconnectInterval):
					continue
				}
			}
		}
	}
}

// startVideoCaptureProcess starts OpenCV VideoCapture similar
func (cm *CameraManager) startVideoCaptureProcess(camera *Camera) error {
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
		case <-camera.stopChannel:
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

			rawFrame := &RawFrame{
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
			case camera.rawFrames <- rawFrame:
				log.Debug().
					Str("camera_id", camera.ID).
					Int64("frame_id", frameID).
					Msg("Frame sent to processing pipeline")
			default:

				log.Warn().
					Str("camera_id", camera.ID).
					Int64("frame_id", frameID).
					Msg("Dropped raw frame - processing buffer full")
			}

			targetInterval := time.Second / time.Duration(cm.getTargetFPS())
			time.Sleep(targetInterval)
		}
	}
}

// runFrameProcessor processes frames through AI and adds metadata
func (cm *CameraManager) runFrameProcessor(camera *Camera) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", camera.ID).
				Msg("Frame processor panic recovered")
		}
	}()

	for {
		select {
		case <-camera.stopChannel:
			return
		case rawFrame := <-camera.rawFrames:
			// Update camera statistics first
			camera.FPS = cm.calculateFPS(camera)
			camera.Latency = time.Since(rawFrame.Timestamp)

			// Process frame with current stats
			processedFrame := cm.frameProcessor.ProcessFrame(rawFrame, camera.Projects, camera.FPS, camera.Latency)

			// Send to publisher
			select {
			case camera.processedFrames <- processedFrame:
			default:
				log.Warn().Str("camera_id", camera.ID).Msg("Dropped processed frame - buffer full")
			}
		}
	}
}

// runPublisher publishes frames to MediaMTX
func (cm *CameraManager) runPublisher(camera *Camera) {
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
		case <-camera.stopChannel:
			return
		case processedFrame := <-camera.processedFrames:
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
func (cm *CameraManager) calculateFPS(camera *Camera) float64 {
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
func (cm *CameraManager) stopCameraInternal(camera *Camera) {
	if camera == nil {
		return
	}

	camera.IsActive = false

	// Stop all processes
	close(camera.stopChannel)

	// Close channels
	close(camera.rawFrames)
	close(camera.processedFrames)
}

// GetCamera returns camera information
func (cm *CameraManager) GetCamera(cameraID string) (*CameraResponse, error) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	camera, exists := cm.cameras[cameraID]
	if !exists {
		return nil, fmt.Errorf("camera %s not found", cameraID)
	}

	return &CameraResponse{
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
		RTSPUrl:       camera.RTSPUrl,
		WebRTCUrl:     camera.WebRTCUrl,
		HLSUrl:        camera.HLSUrl,
		MJPEGUrl:      camera.MJPEGUrl,
	}, nil
}

// ListCameras returns all cameras
func (cm *CameraManager) ListCameras() []*CameraResponse {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	cameras := make([]*CameraResponse, 0, len(cm.cameras))
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
	cm.cameras = make(map[string]*Camera)
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
