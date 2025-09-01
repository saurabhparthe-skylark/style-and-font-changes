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

// CameraLifecycle manages a single camera with completely isolated resources
type CameraLifecycle struct {
	camera *models.Camera
	cm     *CameraManager

	// Simple state management
	state   int32
	running int32 // 0 = stopped, 1 = running

	// Single context for everything
	ctx    context.Context
	cancel context.CancelFunc

	// Synchronization for proper shutdown
	shutdownDone chan struct{}
	streamDone   chan struct{}

	// Per-camera isolated services (NO SHARING!)
	frameProcessor   *frameprocessing.FrameProcessor
	publisherSvc     *publisher.Service
	streamCaptureSvc *streamcapture.Service

	// Service mutexes for safe access
	fpMutex     sync.RWMutex
	pubMutex    sync.RWMutex
	streamMutex sync.RWMutex

	// Simple channel management
	mu sync.RWMutex

	// Cleanup synchronization
	cleanupOnce sync.Once
}

// NewCameraLifecycle creates a new camera lifecycle manager with completely isolated resources
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

	// Use config buffer sizes
	rawBufferSize := cl.cm.cfg.FrameBufferSize
	processedBufferSize := cl.cm.cfg.PublishingBuffer

	// Create fresh channels
	cl.camera.RawFrames = make(chan *models.RawFrame, rawBufferSize)
	cl.camera.ProcessedFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.AlertFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.RecorderFrames = make(chan *models.ProcessedFrame, processedBufferSize)
	cl.camera.StopChannel = make(chan struct{})

	log.Debug().
		Str("camera_id", cl.camera.ID).
		Int("raw_buffer_size", rawBufferSize).
		Int("processed_buffer_size", processedBufferSize).
		Msg("Created isolated camera channels")
}

// createIsolatedServices creates dedicated services for this camera
func (cl *CameraLifecycle) createIsolatedServices() error {
	// Clean up existing services first
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
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to create dedicated frame processor, will continue without AI")
		cl.frameProcessor = nil // Explicitly set to nil on failure
	}

	// Create dedicated publisher service
	cl.publisherSvc, err = publisher.NewService(cl.cm.cfg)
	if err != nil {
		return fmt.Errorf("failed to create dedicated publisher for camera %s: %w", cl.camera.ID, err)
	}

	// Create dedicated stream capture service
	cl.streamCaptureSvc = streamcapture.NewService(cl.cm.cfg)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Bool("ai_enabled", cl.camera.AIEnabled).
		Bool("has_frame_processor", cl.frameProcessor != nil).
		Msg("Created dedicated isolated services for camera")

	return nil
}

// getServices safely gets the isolated services
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

// Start starts the camera with completely isolated resources
func (cl *CameraLifecycle) Start() error {
	if !atomic.CompareAndSwapInt32(&cl.state, int32(StateStopped), int32(StateRunning)) {
		return fmt.Errorf("camera %s cannot start from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Starting camera with completely isolated resources")

	// Create fresh context
	cl.ctx, cl.cancel = context.WithCancel(context.Background())
	atomic.StoreInt32(&cl.running, 1)

	// Create synchronization channels
	cl.shutdownDone = make(chan struct{})
	cl.streamDone = make(chan struct{})

	// Reset cleanup once
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

	// Create fresh channels
	cl.createChannels()

	// Create completely isolated services for this camera
	if err := cl.createIsolatedServices(); err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to create isolated services")
		cl.setState(StateStopped)
		atomic.StoreInt32(&cl.running, 0)
		return err
	}

	// Start frame processors
	cl.startFrameProcessors()

	// Start main capture goroutine
	go cl.runCamera()

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera started successfully with completely isolated resources")

	return nil
}

// Stop stops the camera with proper cleanup and synchronization
func (cl *CameraLifecycle) Stop() error {
	if !atomic.CompareAndSwapInt32(&cl.state, int32(StateRunning), int32(StateStopping)) {
		return fmt.Errorf("camera %s cannot stop from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Stopping camera with aggressive cleanup and synchronization")

	// Signal stop immediately
	atomic.StoreInt32(&cl.running, 0)

	// Cancel context to stop all goroutines IMMEDIATELY
	if cl.cancel != nil {
		cl.cancel()
		log.Debug().Str("camera_id", cl.camera.ID).Msg("Context cancelled for immediate stop")
	}

	// Close stop channel aggressively
	func() {
		defer func() { _ = recover() }()
		if cl.camera.StopChannel != nil {
			close(cl.camera.StopChannel)
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Stop channel closed")
		}
	}()

	// Wait for stream capture to properly stop with timeout
	log.Debug().Str("camera_id", cl.camera.ID).Msg("Waiting for stream capture to stop...")
	select {
	case <-cl.streamDone:
		log.Debug().Str("camera_id", cl.camera.ID).Msg("Stream capture stopped properly")
	case <-time.After(5 * time.Second):
		log.Warn().Str("camera_id", cl.camera.ID).Msg("Timeout waiting for stream capture to stop")
	}

	// Wait for all shutdown processes with timeout
	log.Debug().Str("camera_id", cl.camera.ID).Msg("Waiting for complete shutdown...")
	select {
	case <-cl.shutdownDone:
		log.Debug().Str("camera_id", cl.camera.ID).Msg("Complete shutdown confirmed")
	case <-time.After(3 * time.Second):
		log.Warn().Str("camera_id", cl.camera.ID).Msg("Timeout waiting for complete shutdown")
	}

	// Clean up all resources
	cl.cleanup()

	cl.camera.IsActive = false
	cl.setState(StateStopped)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera stopped successfully with synchronized cleanup")

	return nil
}

// Restart restarts the camera with aggressive cleanup and synchronization
func (cl *CameraLifecycle) Restart() error {
	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Restarting camera with aggressive cleanup and synchronization")

	// Ensure complete stop first with full synchronization
	if err := cl.Stop(); err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Error during stop phase of restart")
	}

	// Longer pause to ensure absolutely complete cleanup
	log.Debug().
		Str("camera_id", cl.camera.ID).
		Msg("Waiting for complete resource cleanup before restart...")

	// Start fresh
	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Starting fresh instance after complete cleanup")
	return cl.Start()
}

// ForceRestart forces a restart
func (cl *CameraLifecycle) ForceRestart() error {
	return cl.Restart()
}

// cleanupServices cleans up isolated services
func (cl *CameraLifecycle) cleanupServices() {
	// Clean up frame processor
	cl.fpMutex.Lock()
	if cl.frameProcessor != nil {
		cl.frameProcessor.Shutdown()
		cl.frameProcessor = nil
	}
	cl.fpMutex.Unlock()

	// Clean up publisher service
	cl.pubMutex.Lock()
	if cl.publisherSvc != nil {
		// Stop any active streams for this camera
		if err := cl.publisherSvc.StopStream(cl.camera.ID); err != nil {
			log.Error().
				Err(err).
				Str("camera_id", cl.camera.ID).
				Msg("Error stopping publisher stream")
		}
		// Note: We don't shutdown the publisher service as it might be shared
		cl.publisherSvc = nil
	}
	cl.pubMutex.Unlock()

	// Clean up stream capture service
	cl.streamMutex.Lock()
	if cl.streamCaptureSvc != nil {
		// Stream capture service is stateless, just nil the reference
		cl.streamCaptureSvc = nil
	}
	cl.streamMutex.Unlock()
}

// cleanup cleans up all resources
func (cl *CameraLifecycle) cleanup() {
	cl.cleanupOnce.Do(func() {
		log.Debug().
			Str("camera_id", cl.camera.ID).
			Msg("Starting complete resource cleanup")

		// Clean up services first
		cl.cleanupServices()

		// Close channels safely
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
			if cl.camera.AlertFrames != nil {
				close(cl.camera.AlertFrames)
			}
		}()

		func() {
			defer func() { _ = recover() }()
			if cl.camera.RecorderFrames != nil {
				close(cl.camera.RecorderFrames)
			}
		}()

		log.Debug().
			Str("camera_id", cl.camera.ID).
			Msg("Complete resource cleanup finished")
	})
}

// runCamera runs everything in a single goroutine with proper error handling and synchronization
func (cl *CameraLifecycle) runCamera() {
	defer func() {
		// Signal that stream capture is done
		func() {
			defer func() { _ = recover() }()
			close(cl.streamDone)
		}()

		// Signal complete shutdown
		func() {
			defer func() { _ = recover() }()
			close(cl.shutdownDone)
		}()

		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Camera main loop panic recovered, stopping camera")
			_ = cl.Stop()
		}
	}()

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera main capture loop started with isolated services and synchronization")

	// Get dedicated stream capture service
	streamCaptureSvc := cl.getStreamCaptureService()
	if streamCaptureSvc == nil {
		log.Error().
			Str("camera_id", cl.camera.ID).
			Msg("No stream capture service available")
		return
	}

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			log.Info().
				Str("camera_id", cl.camera.ID).
				Msg("Camera context cancelled - stopping stream capture immediately")
			return
		default:
			// Run capture with dedicated service and proper context
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Msg("Starting video capture process with context")

			err := streamCaptureSvc.StartVideoCaptureProcess(cl.ctx, cl.camera)

			// Check if context was cancelled during capture
			select {
			case <-cl.ctx.Done():
				log.Info().
					Str("camera_id", cl.camera.ID).
					Msg("Context cancelled during video capture - exiting immediately")
				return
			default:
			}

			if err != nil && cl.isRunning() {
				log.Error().
					Err(err).
					Str("camera_id", cl.camera.ID).
					Msg("Video capture failed, retrying with backoff")

				cl.camera.ErrorCount++

				// Backoff delay with context cancellation check
				delay := time.Duration(cl.camera.ErrorCount) * time.Second
				if delay > 10*time.Second {
					delay = 10 * time.Second
				}

				log.Debug().
					Str("camera_id", cl.camera.ID).
					Dur("delay", delay).
					Msg("Waiting before retry")

				select {
				case <-cl.ctx.Done():
					log.Info().
						Str("camera_id", cl.camera.ID).
						Msg("Context cancelled during backoff - exiting immediately")
					return
				case <-time.After(delay):
					continue
				}
			}
		}
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Camera main capture loop ended")
}
