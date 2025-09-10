package camera

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/frameprocessing"
	"kepler-worker-go/internal/services/publisher"
	"kepler-worker-go/internal/services/streamcapture"
)

// CameraState represents the atomic state of a camera
type CameraState int32

const (
	StateStopped CameraState = iota
	StateRunning
	StateStopping
)

func (s CameraState) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	default:
		return "unknown"
	}
}

// CameraLifecycle manages a single camera with isolated resources
type CameraLifecycle struct {
	camera *models.Camera
	cm     *CameraManager

	// State management
	state   int32
	running int32

	// Context for lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// Shutdown synchronization
	shutdownDone chan struct{}
	streamDone   chan struct{}

	// Per-camera isolated services
	frameProcessor   *frameprocessing.FrameProcessor
	publisherSvc     *publisher.Service
	streamCaptureSvc *streamcapture.Service

	// Service access mutexes
	fpMutex     sync.RWMutex
	pubMutex    sync.RWMutex
	streamMutex sync.RWMutex

	// Channel management
	mu sync.RWMutex

	// Cleanup coordination
	cleanupOnce sync.Once
}

// NewCameraLifecycle creates a new camera lifecycle manager
func NewCameraLifecycle(camera *models.Camera, cm *CameraManager) *CameraLifecycle {
	cl := &CameraLifecycle{
		camera: camera,
		cm:     cm,
	}

	cl.setState(StateStopped)
	cl.createChannels()

	return cl
}

// setState atomically sets the camera state
func (cl *CameraLifecycle) setState(state CameraState) {
	atomic.StoreInt32(&cl.state, int32(state))
}

// getState atomically gets the camera state
func (cl *CameraLifecycle) getState() CameraState {
	return CameraState(atomic.LoadInt32(&cl.state))
}

// isRunning checks if camera is running
func (cl *CameraLifecycle) isRunning() bool {
	return atomic.LoadInt32(&cl.running) == 1
}

// createChannels creates channels for the camera
func (cl *CameraLifecycle) createChannels() {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	rawBufferSize := cl.cm.cfg.FrameBufferSize
	processedBufferSize := cl.cm.cfg.PublishingBuffer

	cl.camera.RawFrames = make(chan *models.RawFrame, rawBufferSize)
	cl.camera.ProcessedFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.PostProcessingFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.RecorderFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.StopChannel = make(chan struct{})

}

// createServices creates dedicated services
func (cl *CameraLifecycle) createServices() error {
	cl.cleanupServices()

	cl.fpMutex.Lock()
	cl.pubMutex.Lock()
	cl.streamMutex.Lock()
	defer cl.fpMutex.Unlock()
	defer cl.pubMutex.Unlock()
	defer cl.streamMutex.Unlock()

	var err error

	// Create dedicated frame processor
	cl.frameProcessor, err = frameprocessing.NewFrameProcessorWithCamera(cl.cm.cfg, cl.camera)
	if err != nil {
		cl.frameProcessor, _ = frameprocessing.NewFrameProcessor(cl.cm.cfg)
	}

	// Create dedicated publisher service (per camera)
	cl.publisherSvc, err = publisher.NewService(cl.cm.cfg)
	if err != nil {
		return fmt.Errorf("failed to create publisher for camera %s: %w", cl.camera.ID, err)
	}

	// Create dedicated stream capture service
	cl.streamCaptureSvc = streamcapture.NewService(cl.cm.cfg)

	log.Debug().
		Str("camera_id", cl.camera.ID).
		Bool("ai_enabled", cl.camera.AIEnabled).
		Msg("Created isolated services")

	return nil
}

// Service getters
func (cl *CameraLifecycle) getFrameProcessor() *frameprocessing.FrameProcessor {
	cl.fpMutex.RLock()
	defer cl.fpMutex.RUnlock()
	return cl.frameProcessor
}

func (cl *CameraLifecycle) getPublisherService() *publisher.Service {
	cl.pubMutex.RLock()
	defer cl.pubMutex.RUnlock()
	return cl.publisherSvc
}

func (cl *CameraLifecycle) getStreamCaptureService() *streamcapture.Service {
	cl.streamMutex.RLock()
	defer cl.streamMutex.RUnlock()
	return cl.streamCaptureSvc
}

// Start starts the camera
func (cl *CameraLifecycle) Start() error {
	if !atomic.CompareAndSwapInt32(&cl.state, int32(StateStopped), int32(StateRunning)) {
		return fmt.Errorf("camera %s cannot start from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Starting camera")

	// Create fresh context
	cl.ctx, cl.cancel = context.WithCancel(context.Background())
	atomic.StoreInt32(&cl.running, 1)

	// Create synchronization channels
	cl.shutdownDone = make(chan struct{})
	cl.streamDone = make(chan struct{})
	cl.cleanupOnce = sync.Once{}

	// Reset camera state
	cl.camera.IsActive = true
	cl.camera.FrameCount = 0
	cl.camera.ErrorCount = 0
	cl.camera.LastFrameTime = time.Time{}
	cl.camera.RecentFrameTimes = make([]time.Time, 0, 30)
	cl.camera.AIFrameCounter = 0
	cl.camera.AIDetectionCount = 0
	cl.camera.LastAIError = ""

	// Create fresh channels and services
	cl.createChannels()
	if err := cl.createServices(); err != nil {
		cl.setState(StateStopped)
		atomic.StoreInt32(&cl.running, 0)
		return err
	}

	// Start everything in one place
	go cl.runCamera()

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera started successfully")

	return nil
}

// Stop stops the camera
func (cl *CameraLifecycle) Stop() error {
	if !atomic.CompareAndSwapInt32(&cl.state, int32(StateRunning), int32(StateStopping)) {
		return fmt.Errorf("camera %s cannot stop from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Stopping camera")

	atomic.StoreInt32(&cl.running, 0)

	if cl.cancel != nil {
		cl.cancel()
	}

	func() {
		defer func() { _ = recover() }()
		if cl.camera.StopChannel != nil {
			close(cl.camera.StopChannel)
		}
	}()

	// Wait for shutdown
	select {
	case <-cl.shutdownDone:
		log.Debug().Str("camera_id", cl.camera.ID).Msg("Shutdown confirmed")
	case <-time.After(5 * time.Second):
		log.Warn().Str("camera_id", cl.camera.ID).Msg("Shutdown timeout")
	}

	cl.cleanup()
	cl.camera.IsActive = false
	cl.setState(StateStopped)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera stopped successfully")

	return nil
}

// Restart restarts the camera
func (cl *CameraLifecycle) Restart() error {
	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Restarting camera")

	_ = cl.Stop()
	time.Sleep(300 * time.Millisecond)
	return cl.Start()
}

// ForceRestart forces a restart
func (cl *CameraLifecycle) ForceRestart() error {
	return cl.Restart()
}

// ========================================
// MAIN CAMERA LOOP - Everything happens here
// ========================================

// runCamera - Single method that runs everything
func (cl *CameraLifecycle) runCamera() {
	defer func() {
		// Signal shutdown
		func() {
			defer func() { _ = recover() }()
			close(cl.shutdownDone)
		}()

		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Camera panic recovered")
			_ = cl.Stop()
		}
	}()

	log.Debug().
		Str("camera_id", cl.camera.ID).
		Msg("Camera capture and processing started")

	// Start frame processor, publisher, and alert processor in background
	go cl.runFrameProcessor()
	go cl.runPublisher()
	go cl.runPostProcessor()

	// Main capture loop
	streamSvc := cl.getStreamCaptureService()
	if streamSvc == nil {
		log.Error().
			Str("camera_id", cl.camera.ID).
			Msg("No stream capture service")
		return
	}

	// Register RTSP source for audio with publisher
	if pub := cl.getPublisherService(); pub != nil {
		pub.RegisterCameraSource(cl.camera.ID, cl.camera.URL)
	}

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			log.Info().
				Str("camera_id", cl.camera.ID).
				Msg("Camera context cancelled")
			return
		default:
			err := streamSvc.StartVideoCaptureProcess(cl.ctx, cl.camera)

			if err != nil && cl.isRunning() {
				log.Error().
					Err(err).
					Str("camera_id", cl.camera.ID).
					Msg("Video capture failed, retrying")

				cl.camera.ErrorCount++
				delay := time.Duration(cl.camera.ErrorCount) * time.Second
				if delay > 10*time.Second {
					delay = 10 * time.Second
				}

				select {
				case <-cl.ctx.Done():
					return
				case <-time.After(delay):
					continue
				}
			}
		}
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera capture ended")
}

// runFrameProcessor processes frames - gets latest available frame when ready
func (cl *CameraLifecycle) runFrameProcessor() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Frame processor panic recovered")
		}
	}()

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			return
		case rawFrame, ok := <-cl.camera.RawFrames:
			if !ok {
				return
			}

			// SIMPLE: Just get the absolute latest frame available RIGHT NOW
			latestFrame := rawFrame
			drainedCount := 0

			// Drain everything to get the absolute latest
		GetLatest:
			for {
				select {
				case newerFrame := <-cl.camera.RawFrames:
					latestFrame = newerFrame
					drainedCount++
				default:
					break GetLatest
				}
			}

			if drainedCount > 0 {
				log.Debug().
					Str("camera_id", cl.camera.ID).
					Int("skipped_old_frames", drainedCount).
					Int64("latest_frame_id", latestFrame.FrameID).
					Dur("latest_frame_age", time.Since(latestFrame.Timestamp)).
					Msg("Processing latest available frame")
			}

			// Process the absolute latest frame
			cl.processFrame(latestFrame)

		case <-time.After(1 * time.Second):
			continue
		}
	}
}

// processFrame processes a single frame simply
func (cl *CameraLifecycle) processFrame(rawFrame *models.RawFrame) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Process frame panic recovered")
		}
	}()

	if rawFrame == nil {
		return
	}

	// Update camera statistics
	streamSvc := cl.getStreamCaptureService()
	if streamSvc != nil {
		cl.camera.FPS = streamSvc.CalculateFPS(cl.camera)
	}
	cl.camera.Latency = time.Since(rawFrame.Timestamp)

	// Get frame processor
	processor := cl.getFrameProcessor()

	var processedFrame *models.ProcessedFrame

	if processor != nil {
		// Measure AI processing time for debugging delay
		aiStartTime := time.Now()

		// Try frame processing with error protection
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().
						Str("camera_id", cl.camera.ID).
						Interface("panic", r).
						Msg("Frame processor panic - creating basic frame")
					processedFrame = nil // Force fallback
				}
			}()

			processedFrame = processor.ProcessFrame(rawFrame, cl.camera.Projects, cl.camera.FPS, cl.camera.Latency)
		}()

		aiProcessingDuration := time.Since(aiStartTime)

		if aiProcessingDuration > 100*time.Millisecond {
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Int64("frame_id", rawFrame.FrameID).
				Dur("ai_processing_time", aiProcessingDuration).
				Dur("frame_age_when_processed", time.Since(rawFrame.Timestamp)).
				Bool("ai_enabled", cl.camera.AIEnabled).
				Msg("AI processing took significant time")
		}
	}

	// Fallback if processor failed or doesn't exist
	if processedFrame == nil {
		processedFrame = &models.ProcessedFrame{
			CameraID:     rawFrame.CameraID,
			Data:         rawFrame.Data,
			RawData:      rawFrame.Data,
			Timestamp:    rawFrame.Timestamp,
			FrameID:      rawFrame.FrameID,
			Width:        rawFrame.Width,
			Height:       rawFrame.Height,
			FPS:          cl.camera.FPS,
			Latency:      cl.camera.Latency,
			AIEnabled:    cl.camera.AIEnabled,
			AIDetections: nil,
		}
	}

	// Update AI statistics
	if processedFrame.AIDetections != nil {
		if aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult); ok {
			cl.camera.AIProcessingTime = aiResult.ProcessingTime
			cl.camera.AIDetectionCount += int64(len(aiResult.Detections))

			if aiResult.ErrorMessage != "" {
				cl.camera.LastAIError = aiResult.ErrorMessage
				cl.camera.ErrorCount++
			} else if aiResult.FrameProcessed {
				cl.camera.LastAIError = ""
			}
		}
	}

	// Send frame to publisher
	select {
	case cl.camera.ProcessedFrames <- processedFrame:
	default:
	}

	// Send frame to alert processor if AI is enabled and there are detections
	if cl.camera.AIEnabled && processedFrame.AIDetections != nil {
		if aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult); ok && len(aiResult.Detections) > 0 {
			select {
			case cl.camera.PostProcessingFrames <- processedFrame:
			default:
			}
		}
	}
}

// runPublisher publishes frames - gets latest processed frame when ready
func (cl *CameraLifecycle) runPublisher() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Publisher panic recovered")
		}
	}()

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			return
		case processedFrame, ok := <-cl.camera.ProcessedFrames:
			if !ok {
				return
			}

			// SIMPLE: Just get the absolute latest processed frame RIGHT NOW
			latestProcessedFrame := processedFrame
			publisherDrainedCount := 0

			// Drain everything to get the absolute latest processed frame
		GetLatestProcessed:
			for {
				select {
				case newerProcessedFrame := <-cl.camera.ProcessedFrames:
					latestProcessedFrame = newerProcessedFrame
					publisherDrainedCount++
				default:
					break GetLatestProcessed
				}
			}

			if publisherDrainedCount > 0 {
				log.Debug().
					Str("camera_id", cl.camera.ID).
					Int("skipped_old_processed", publisherDrainedCount).
					Int64("latest_processed_id", latestProcessedFrame.FrameID).
					Msg("Publishing latest processed frame")
			}

			// Publish the absolute latest processed frame
			cl.publishFrame(latestProcessedFrame)

		case <-time.After(1 * time.Second):
			continue
		}
	}
}

// publishFrame publishes a single frame
func (cl *CameraLifecycle) publishFrame(processedFrame *models.ProcessedFrame) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Publish frame panic recovered")
		}
	}()

	if processedFrame == nil {
		return
	}

	publisherSvc := cl.getPublisherService()
	if publisherSvc == nil {
		return
	}

	err := publisherSvc.PublishFrame(processedFrame)
	if err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to publish frame")
		cl.camera.ErrorCount++
	}
}

// runPostProcessor processes alerts
func (cl *CameraLifecycle) runPostProcessor() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Post processor panic recovered")
		}
	}()

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			return
		case processedFrame, ok := <-cl.camera.PostProcessingFrames:
			if !ok {
				return
			}
			cl.processPost(processedFrame)
		case <-time.After(1 * time.Second):
			continue
		}
	}
}

// processPost processes a single post frame
func (cl *CameraLifecycle) processPost(processedFrame *models.ProcessedFrame) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Process post panic recovered")
		}
	}()

	// Only process alerts if AI is enabled
	if !cl.camera.AIEnabled || processedFrame == nil {
		return
	}

	// Use post-processing service if available
	if cl.cm.postProcessingService != nil {
		aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult)
		if !ok || len(aiResult.Detections) == 0 {
			return
		}

		detections := aiResult.Detections

		frameMetadata := models.FrameMetadata{
			FrameID:     processedFrame.FrameID,
			Timestamp:   processedFrame.Timestamp,
			Width:       processedFrame.Width,
			Height:      processedFrame.Height,
			AllDetCount: len(aiResult.Detections),
			CameraID:    cl.camera.ID,
		}

		result := cl.cm.postProcessingService.ProcessDetections(
			cl.camera.ID,
			detections,
			frameMetadata,
			processedFrame.RawData,
			processedFrame.Data,
		)

		if len(result.Errors) > 0 {
			log.Warn().
				Str("camera_id", cl.camera.ID).
				Strs("errors", result.Errors).
				Msg("Alert processing had errors")
		}
	}
}

// ========================================
// CLEANUP AND RESOURCE MANAGEMENT
// ========================================

// cleanupServices cleans up services
func (cl *CameraLifecycle) cleanupServices() {
	cl.fpMutex.Lock()
	if cl.frameProcessor != nil {
		cl.frameProcessor.Shutdown()
		cl.frameProcessor = nil
	}
	cl.fpMutex.Unlock()

	cl.pubMutex.Lock()
	if cl.publisherSvc != nil {
		cl.publisherSvc.StopStream(cl.camera.ID)
		cl.publisherSvc = nil
	}
	cl.pubMutex.Unlock()

	cl.streamMutex.Lock()
	if cl.streamCaptureSvc != nil {
		cl.streamCaptureSvc = nil
	}
	cl.streamMutex.Unlock()
}

// cleanup cleans up all resources
func (cl *CameraLifecycle) cleanup() {
	cl.cleanupOnce.Do(func() {
		cl.cleanupServices()

		cl.mu.Lock()
		defer cl.mu.Unlock()

		func() {
			defer func() { _ = recover() }()
			if cl.camera.RawFrames != nil {
				close(cl.camera.RawFrames)
			}
		}()

		func() {
			defer func() { _ = recover() }()
			if cl.camera.ProcessedFrames != nil {
				close(cl.camera.ProcessedFrames)
			}
		}()

		func() {
			defer func() { _ = recover() }()
			if cl.camera.PostProcessingFrames != nil {
				close(cl.camera.PostProcessingFrames)
			}
		}()

		func() {
			defer func() { _ = recover() }()
			if cl.camera.RecorderFrames != nil {
				close(cl.camera.RecorderFrames)
			}
		}()

	})
}
