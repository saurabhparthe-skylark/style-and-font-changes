package camera

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/models"
)

// CameraState represents the atomic state of a camera
type CameraState int32

const (
	StateStopped CameraState = iota
	StateStarting
	StateRunning
	StateStopping
	StateRestarting
)

func (s CameraState) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateRestarting:
		return "restarting"
	default:
		return "unknown"
	}
}

// CameraLifecycle manages the lifecycle of a single camera with proper resource management
type CameraLifecycle struct {
	camera *models.Camera
	cm     *CameraManager

	// Atomic state management
	state int32 // CameraState

	// Context management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Channel management
	channels *CameraChannels
	mu       sync.RWMutex

	// Restart coordination
	restartWg sync.WaitGroup
}

// CameraChannels holds all camera channels with proper lifecycle management
type CameraChannels struct {
	RawFrames       chan *models.RawFrame
	ProcessedFrames chan *models.ProcessedFrame
	AlertFrames     chan *models.ProcessedFrame
	RecorderFrames  chan *models.ProcessedFrame
}

// NewCameraLifecycle creates a new camera lifecycle manager
func NewCameraLifecycle(camera *models.Camera, cm *CameraManager) *CameraLifecycle {
	ctx, cancel := context.WithCancel(context.Background())

	cl := &CameraLifecycle{
		camera: camera,
		cm:     cm,
		ctx:    ctx,
		cancel: cancel,
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

// createChannels creates new channels for the camera
func (cl *CameraLifecycle) createChannels() {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.channels = &CameraChannels{
		RawFrames:       make(chan *models.RawFrame, cl.cm.cfg.FrameBufferSize),
		ProcessedFrames: make(chan *models.ProcessedFrame, cl.cm.cfg.PublishingBuffer),
		AlertFrames:     make(chan *models.ProcessedFrame, cl.cm.cfg.PublishingBuffer),
		RecorderFrames:  make(chan *models.ProcessedFrame, cl.cm.cfg.PublishingBuffer),
	}

	// Update camera with new channels
	cl.camera.RawFrames = cl.channels.RawFrames
	cl.camera.ProcessedFrames = cl.channels.ProcessedFrames
	cl.camera.AlertFrames = cl.channels.AlertFrames
	cl.camera.RecorderFrames = cl.channels.RecorderFrames
}

// Start starts the camera with proper lifecycle management
func (cl *CameraLifecycle) Start() error {
	if !cl.compareAndSwapState(StateStopped, StateStarting) {
		return fmt.Errorf("camera %s cannot start from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Starting camera with production-grade lifecycle management")

	// Reset camera state
	cl.camera.IsActive = true
	cl.camera.FrameCount = 0
	cl.camera.ErrorCount = 0
	cl.camera.LastFrameTime = time.Time{}
	cl.camera.RecentFrameTimes = make([]time.Time, 0, 30)
	cl.camera.AIFrameCounter = 0
	cl.camera.AIDetectionCount = 0
	cl.camera.LastAIError = ""

	// Start all components
	cl.wg.Add(4)
	go cl.runStreamReader()
	go cl.runFrameProcessor()
	go cl.runPublisher()
	go cl.runPostProcessor()

	cl.setState(StateRunning)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Str("state", cl.getState().String()).
		Msg("Camera started successfully")

	return nil
}

// Stop gracefully stops the camera
func (cl *CameraLifecycle) Stop() error {
	if !cl.compareAndSwapState(StateRunning, StateStopping) &&
		!cl.compareAndSwapState(StateStarting, StateStopping) {
		return fmt.Errorf("camera %s cannot stop from state %s", cl.camera.ID, cl.getState())
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Gracefully stopping camera")

	// Cancel context to signal all goroutines
	cl.cancel()

	// Wait for all goroutines to finish with timeout
	done := make(chan struct{})
	go func() {
		cl.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info().Str("camera_id", cl.camera.ID).Msg("All goroutines stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Warn().Str("camera_id", cl.camera.ID).Msg("Timeout waiting for goroutines to stop")
	}

	// Clean up resources
	cl.cleanup()

	cl.camera.IsActive = false
	cl.setState(StateStopped)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Str("state", cl.getState().String()).
		Msg("Camera stopped successfully")

	return nil
}

// Restart performs a zero-downtime restart
func (cl *CameraLifecycle) Restart() error {
	// Try to atomically change state from RUNNING to RESTARTING
	currentState := cl.getState()

	// Handle different states
	switch currentState {
	case StateRunning:
		// Normal case - change to restarting
		if !cl.compareAndSwapState(StateRunning, StateRestarting) {
			// Race condition - someone else changed the state
			return fmt.Errorf("camera %s state changed during restart attempt", cl.camera.ID)
		}
	case StateRestarting:
		// Already restarting
		return fmt.Errorf("restart already in progress for camera %s", cl.camera.ID)
	default:
		// Cannot restart from other states
		return fmt.Errorf("camera %s cannot restart from state %s", cl.camera.ID, currentState)
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Initiating zero-downtime restart")

	// Signal restart (non-blocking since we already changed state)
	go cl.performRestart()
	return nil
}

// ForceRestart forces a restart regardless of current state (for recovery)
func (cl *CameraLifecycle) ForceRestart() error {
	currentState := cl.getState()

	log.Warn().
		Str("camera_id", cl.camera.ID).
		Str("current_state", currentState.String()).
		Msg("Forcing camera restart for recovery")

	// Force state to restarting
	cl.setState(StateRestarting)

	// Perform restart
	go cl.performRestart()
	return nil
}

// compareAndSwapState atomically compares and swaps the state
func (cl *CameraLifecycle) compareAndSwapState(old, new CameraState) bool {
	return atomic.CompareAndSwapInt32(&cl.state, int32(old), int32(new))
}

// performRestart performs the actual restart with zero downtime
func (cl *CameraLifecycle) performRestart() {
	// State should already be RESTARTING from the Restart() call
	currentState := cl.getState()
	if currentState != StateRestarting {
		log.Error().
			Str("camera_id", cl.camera.ID).
			Str("state", currentState.String()).
			Msg("performRestart called but camera not in restarting state")
		return
	}

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Performing zero-downtime restart")

	// Ensure we recover from any failure and don't get stuck in restarting state
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Panic during restart, recovering to running state")
			cl.setState(StateRunning)
		}
	}()

	// Add timeout protection to prevent getting stuck in restarting state
	restartTimeout := time.After(30 * time.Second)
	done := make(chan struct{})

	go func() {
		defer close(done)
		cl.doRestart()
	}()

	select {
	case <-done:
		// Restart completed successfully
	case <-restartTimeout:
		log.Error().
			Str("camera_id", cl.camera.ID).
			Msg("Restart timeout, forcing back to running state")
		cl.setState(StateRunning)
		return
	}
}

// doRestart performs the actual restart logic
func (cl *CameraLifecycle) doRestart() {

	// Create new context for the restarted components
	newCtx, newCancel := context.WithCancel(context.Background())
	oldCancel := cl.cancel

	// Create new channels
	oldChannels := cl.channels
	cl.createChannels()

	// Update context
	cl.ctx = newCtx
	cl.cancel = newCancel

	// Start new components
	cl.restartWg.Add(4)
	go cl.runStreamReaderWithWg(&cl.restartWg)
	go cl.runFrameProcessorWithWg(&cl.restartWg)
	go cl.runPublisherWithWg(&cl.restartWg)
	go cl.runPostProcessorWithWg(&cl.restartWg)

	// Wait a moment for new components to stabilize
	time.Sleep(100 * time.Millisecond)

	// Stop old components gracefully
	oldCancel()

	// Wait for old components to stop with timeout
	waitDone := make(chan struct{})
	go func() {
		cl.wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// Old components stopped successfully
	case <-time.After(5 * time.Second):
		log.Warn().
			Str("camera_id", cl.camera.ID).
			Msg("Timeout waiting for old components to stop during restart")
	}

	// Transfer waitgroup - create new one instead of copying
	cl.wg = sync.WaitGroup{}
	cl.wg.Add(4) // We have 4 new goroutines running

	// Clean up old channels
	go cl.drainAndCloseChannels(oldChannels)

	cl.setState(StateRunning)

	log.Info().
		Str("camera_id", cl.camera.ID).
		Msg("Zero-downtime restart completed successfully")
}

// drainAndCloseChannels safely drains and closes old channels
func (cl *CameraLifecycle) drainAndCloseChannels(channels *CameraChannels) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Panic during channel cleanup (expected)")
		}
	}()

	// Drain channels with timeout
	timeout := time.After(1 * time.Second)

	// Drain RawFrames
	for {
		select {
		case <-channels.RawFrames:
		case <-timeout:
			goto CloseChannels
		default:
			goto DrainProcessed
		}
	}

DrainProcessed:
	// Drain ProcessedFrames
	for {
		select {
		case <-channels.ProcessedFrames:
		case <-timeout:
			goto CloseChannels
		default:
			goto DrainAlert
		}
	}

DrainAlert:
	// Drain AlertFrames
	for {
		select {
		case <-channels.AlertFrames:
		case <-timeout:
			goto CloseChannels
		default:
			goto DrainRecorder
		}
	}

DrainRecorder:
	// Drain RecorderFrames
	for {
		select {
		case <-channels.RecorderFrames:
		case <-timeout:
			goto CloseChannels
		default:
			goto CloseChannels
		}
	}

CloseChannels:
	// Close channels safely
	close(channels.RawFrames)
	close(channels.ProcessedFrames)
	close(channels.AlertFrames)
	close(channels.RecorderFrames)

	log.Debug().
		Str("camera_id", cl.camera.ID).
		Msg("Old channels drained and closed")
}

// cleanup cleans up resources
func (cl *CameraLifecycle) cleanup() {
	// Stop MediaMTX stream
	if err := cl.cm.publisherSvc.StopStream(cl.camera.ID); err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to stop MediaMTX stream")
	}

	// Clean up channels
	cl.mu.Lock()
	if cl.channels != nil {
		cl.drainAndCloseChannels(cl.channels)
		cl.channels = nil
	}
	cl.mu.Unlock()
}

// Component runners with proper context handling

func (cl *CameraLifecycle) runStreamReader() {
	defer cl.wg.Done()
	cl.streamReaderLoop(cl.ctx)
}

func (cl *CameraLifecycle) runStreamReaderWithWg(wg *sync.WaitGroup) {
	defer wg.Done()
	cl.streamReaderLoop(cl.ctx)
}

func (cl *CameraLifecycle) runFrameProcessor() {
	defer cl.wg.Done()
	cl.frameProcessorLoop(cl.ctx)
}

func (cl *CameraLifecycle) runFrameProcessorWithWg(wg *sync.WaitGroup) {
	defer wg.Done()
	cl.frameProcessorLoop(cl.ctx)
}

func (cl *CameraLifecycle) runPublisher() {
	defer cl.wg.Done()
	cl.publisherLoop(cl.ctx)
}

func (cl *CameraLifecycle) runPublisherWithWg(wg *sync.WaitGroup) {
	defer wg.Done()
	cl.publisherLoop(cl.ctx)
}

func (cl *CameraLifecycle) runPostProcessor() {
	defer cl.wg.Done()
	cl.postProcessorLoop(cl.ctx)
}

func (cl *CameraLifecycle) runPostProcessorWithWg(wg *sync.WaitGroup) {
	defer wg.Done()
	cl.postProcessorLoop(cl.ctx)
}
