package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"kepler-worker/internal/config"
	"kepler-worker/internal/stream"
	"kepler-worker/internal/webrtc"
	"kepler-worker/pkg/logger"
)

// Worker represents the main worker instance
type Worker struct {
	config  *config.Config
	logger  *logger.Logger
	cameras map[string]*CameraInstance
	mutex   sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
	webrtc  *webrtc.Publisher
}

// CameraInstance represents a single camera processing instance
type CameraInstance struct {
	ID         string
	StreamURL  string
	Handler    *stream.Handler
	Processor  *stream.FrameProcessor
	Active     bool
	WebRTCPath string
	mutex      sync.RWMutex
}

// New creates a new worker instance
func New(cfg *config.Config, log *logger.Logger) (*Worker, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize WebRTC publisher if enabled
	var webrtcPublisher *webrtc.Publisher
	if cfg.WebRTC.Enabled {
		var err error
		webrtcPublisher, err = webrtc.NewPublisher(cfg, log.WithComponent("webrtc"))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create WebRTC publisher: %w", err)
		}
	}

	worker := &Worker{
		config:  cfg,
		logger:  log,
		cameras: make(map[string]*CameraInstance),
		ctx:     ctx,
		cancel:  cancel,
		webrtc:  webrtcPublisher,
	}

	return worker, nil
}

// Start starts the worker
func (w *Worker) Start() error {
	w.logger.Info("Starting worker core", "worker_id", w.config.Worker.ID)

	// Start WebRTC publisher if enabled
	if w.webrtc != nil {
		if err := w.webrtc.Start(w.ctx); err != nil {
			return fmt.Errorf("failed to start WebRTC publisher: %w", err)
		}
	}

	w.logger.Info("Worker core started successfully")
	return nil
}

// Stop stops the worker
func (w *Worker) Stop() error {
	w.logger.Info("Stopping worker core...")

	// Cancel context to stop all operations
	w.cancel()

	// Stop all cameras
	w.mutex.Lock()
	for id := range w.cameras {
		w.stopCameraUnsafe(id)
	}
	w.mutex.Unlock()

	// Stop WebRTC publisher
	if w.webrtc != nil {
		w.webrtc.Stop()
	}

	w.logger.Info("Worker core stopped successfully")
	return nil
}

// StartCamera starts processing for a camera
func (w *Worker) StartCamera(cameraID, streamURL string, solutions []string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Check if camera already exists
	if camera, exists := w.cameras[cameraID]; exists && camera.Active {
		return fmt.Errorf("camera %s is already active", cameraID)
	}

	logger := w.logger.WithCamera(cameraID)
	logger.Info("Starting camera", "stream_url", streamURL, "solutions", solutions)

	// Create stream handler
	handler, err := stream.NewHandler(streamURL, w.config, logger.WithComponent("stream"))
	if err != nil {
		return fmt.Errorf("failed to create stream handler: %w", err)
	}

	// Create frame processor
	processor, err := stream.NewFrameProcessor(cameraID, w.config, logger.WithComponent("processor"))
	if err != nil {
		handler.Close()
		return fmt.Errorf("failed to create frame processor: %w", err)
	}

	// Create camera instance
	camera := &CameraInstance{
		ID:         cameraID,
		StreamURL:  streamURL,
		Handler:    handler,
		Processor:  processor,
		Active:     true,
		WebRTCPath: fmt.Sprintf("camera_%s", cameraID),
	}

	w.cameras[cameraID] = camera

	// Start stream processing in background
	go w.processCameraStream(camera)

	// Start WebRTC publishing if enabled
	if w.webrtc != nil {
		if err := w.webrtc.StartCamera(cameraID, camera.WebRTCPath, processor); err != nil {
			logger.Error("Failed to start WebRTC publishing", "error", err)
			// Don't fail the entire camera start for WebRTC issues
		}
	}

	logger.Info("Camera started successfully")
	return nil
}

// StopCamera stops processing for a camera
func (w *Worker) StopCamera(cameraID string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.stopCameraUnsafe(cameraID)
}

func (w *Worker) stopCameraUnsafe(cameraID string) error {
	camera, exists := w.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	logger := w.logger.WithCamera(cameraID)
	logger.Info("Stopping camera")

	// Mark as inactive
	camera.mutex.Lock()
	camera.Active = false
	camera.mutex.Unlock()

	// Stop WebRTC publishing
	if w.webrtc != nil {
		w.webrtc.StopCamera(cameraID)
	}

	// Close stream handler and processor
	camera.Handler.Close()
	camera.Processor.Close()

	// Remove from cameras map
	delete(w.cameras, cameraID)

	logger.Info("Camera stopped successfully")
	return nil
}

// GetCameraStatus returns status of all cameras
func (w *Worker) GetCameraStatus() map[string]interface{} {
	w.mutex.RLock()
	defer w.mutex.RUnlock()

	status := make(map[string]interface{})
	for id, camera := range w.cameras {
		camera.mutex.RLock()
		status[id] = map[string]interface{}{
			"active":      camera.Active,
			"stream_url":  camera.StreamURL,
			"webrtc_path": camera.WebRTCPath,
		}
		camera.mutex.RUnlock()
	}

	return status
}

// processCameraStream handles the main processing loop for a camera
func (w *Worker) processCameraStream(camera *CameraInstance) {
	logger := w.logger.WithCamera(camera.ID).WithComponent("processor")

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Camera processing panic", "panic", r)
		}
	}()

	// Connect to stream
	if err := camera.Handler.Connect(w.ctx); err != nil {
		logger.Error("Failed to connect to stream", "error", err)
		return
	}

	frameCount := 0

	for {
		camera.mutex.RLock()
		active := camera.Active
		camera.mutex.RUnlock()

		if !active {
			logger.Info("Camera processing stopped")
			break
		}

		// Check if context is cancelled
		select {
		case <-w.ctx.Done():
			logger.Info("Worker context cancelled")
			return
		default:
		}

		// Read frame from stream
		frame, err := camera.Handler.ReadFrame()
		if err != nil {
			logger.Error("Failed to read frame", "error", err)
			time.Sleep(100 * time.Millisecond) // Brief pause before retry
			continue
		}

		// Process frame
		if frame != nil {
			frameCount++
			if err := camera.Processor.ProcessFrame(frame, frameCount); err != nil {
				logger.Error("Failed to process frame", "error", err)
			}
		}
	}
}

func (w *Worker) GetWebRTCPublisher() *webrtc.Publisher {
	return w.webrtc
}
