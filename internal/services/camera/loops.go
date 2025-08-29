package camera

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/frameprocessing"
)

// streamReaderLoop runs the stream reader with proper context handling
func (cl *CameraLifecycle) streamReaderLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", cl.camera.ID).
				Msg("Stream reader panic recovered")
		}
	}()

	var attempt int

	for {
		select {
		case <-ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Stream reader stopping")
			return
		default:
			err := cl.cm.streamCaptureSvc.StartVideoCaptureProcess(cl.camera)
			if err != nil {
				log.Error().
					Err(err).
					Str("camera_id", cl.camera.ID).
					Msg("VideoCapture process failed")

				cl.camera.ErrorCount++
				attempt++

				// Calculate backoff delay
				delay := cl.cm.streamCaptureSvc.CalculateBackoffDelay(attempt)
				log.Info().
					Str("camera_id", cl.camera.ID).
					Dur("retry_in", delay).
					Int("attempt", attempt).
					Msg("Reconnecting to camera")

				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
					continue
				}
			} else {
				// Success path resets attempts
				attempt = 0
			}
		}
	}
}

// frameProcessorLoop runs the frame processor with proper context handling
func (cl *CameraLifecycle) frameProcessorLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", cl.camera.ID).
				Msg("Frame processor panic recovered")
		}
	}()

	// Create per-camera frame processor
	frameProcessor, err := frameprocessing.NewFrameProcessorWithCamera(cl.cm.cfg, cl.camera)
	if err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to create frame processor for camera")
		return
	}
	defer frameProcessor.Shutdown()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Frame processor stopping")
			return
		case rawFrame := <-cl.camera.RawFrames:
			cl.processFrame(rawFrame, frameProcessor)
		}
	}
}

// processFrame processes a single frame
func (cl *CameraLifecycle) processFrame(rawFrame *models.RawFrame, frameProcessor *frameprocessing.FrameProcessor) {
	// FRAME SKIPPING: Always get the latest frame by draining the buffer
	var latestFrame *models.RawFrame = rawFrame
	skippedFrames := 0

	// Drain all pending frames to get the absolute latest
DrainLoop:
	for {
		select {
		case newerFrame := <-cl.camera.RawFrames:
			latestFrame = newerFrame
			skippedFrames++
		default:
			break DrainLoop
		}
	}

	if skippedFrames > 0 {
		log.Debug().
			Str("camera_id", cl.camera.ID).
			Int("skipped_frames", skippedFrames).
			Int64("latest_frame_id", latestFrame.FrameID).
			Msg("Skipped frames for real-time processing")
	}

	startTime := time.Now()

	// Update camera statistics first
	cl.camera.FPS = cl.cm.streamCaptureSvc.CalculateFPS(cl.camera)
	cl.camera.Latency = time.Since(latestFrame.Timestamp)

	// Process the latest frame with current stats and per-camera AI settings
	processedFrame := frameProcessor.ProcessFrame(latestFrame, cl.camera.Projects, cl.camera.FPS, cl.camera.Latency)

	// Update camera AI statistics
	if processedFrame.AIDetections != nil {
		if aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult); ok {
			cl.camera.AIProcessingTime = aiResult.ProcessingTime
			cl.camera.AIDetectionCount += int64(len(aiResult.Detections))

			if aiResult.ErrorMessage != "" {
				cl.camera.LastAIError = aiResult.ErrorMessage
				cl.camera.ErrorCount++
			} else if aiResult.FrameProcessed {
				cl.camera.LastAIError = "" // Clear error on successful processing
			}
		}
	}

	processingTime := time.Since(startTime)
	log.Debug().
		Str("camera_id", cl.camera.ID).
		Dur("processing_time", processingTime).
		Bool("ai_enabled", cl.camera.AIEnabled).
		Int("skipped_frames", skippedFrames).
		Msg("Frame processed with real-time optimization")

	// Send to publisher (non-blocking)
	select {
	case cl.camera.ProcessedFrames <- processedFrame:
	default:
		// Drop frame if buffer is full for real-time streaming
	}

	// Send to alerts (non-blocking)
	select {
	case cl.camera.AlertFrames <- processedFrame:
	default:
		// Drop frame if buffer is full
	}

	// Send to recorder (non-blocking)
	select {
	case cl.camera.RecorderFrames <- processedFrame:
	default:
		// Drop frame if buffer is full
	}
}

// publisherLoop runs the publisher with proper context handling
func (cl *CameraLifecycle) publisherLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", cl.camera.ID).
				Msg("Publisher panic recovered")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Publisher stopping")
			return
		case processedFrame := <-cl.camera.ProcessedFrames:
			cl.publishFrame(processedFrame)
		}
	}
}

// publishFrame publishes a single frame
func (cl *CameraLifecycle) publishFrame(processedFrame *models.ProcessedFrame) {
	// FRAME SKIPPING: drain to latest to keep real-time publishing
	latestFrame := processedFrame
	skipped := 0
DrainPub:
	for {
		select {
		case newer := <-cl.camera.ProcessedFrames:
			latestFrame = newer
			skipped++
		default:
			break DrainPub
		}
	}

	if skipped > 0 {
		log.Debug().
			Str("camera_id", cl.camera.ID).
			Int("skipped_frames", skipped).
			Int64("latest_frame_id", latestFrame.FrameID).
			Msg("Publisher skipped frames to maintain real-time stream")
	}

	// Publish only the freshest frame to both MJPEG and WebRTC
	err := cl.cm.publisherSvc.PublishFrame(latestFrame)
	if err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to publish frame")
		cl.camera.ErrorCount++
	}
}

// postProcessorLoop runs the post processor with proper context handling
func (cl *CameraLifecycle) postProcessorLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", cl.camera.ID).
				Msg("Post processor panic recovered")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Post processor stopping")
			return
		case processedFrame := <-cl.camera.AlertFrames:
			cl.processAlert(processedFrame)
		}
	}
}

// processAlert processes a single alert frame
func (cl *CameraLifecycle) processAlert(processedFrame *models.ProcessedFrame) {
	// Only process alerts if AI is enabled for this camera
	if !cl.camera.AIEnabled {
		return
	}

	// Use post-processing service with detection-only processing
	if cl.cm.postProcessingService != nil {
		aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult)
		if !ok || len(aiResult.Detections) == 0 {
			return
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
			CameraID:    cl.camera.ID,
		}

		// Use detection-only processing with both raw and annotated frame data
		result := cl.cm.postProcessingService.ProcessDetections(
			cl.camera.ID,
			detections,
			frameMetadata,
			processedFrame.RawData, // Raw frame data for clean crops
			processedFrame.Data,    // Annotated frame data for context images
		)

		// Log processing results
		if len(result.Errors) > 0 {
			log.Warn().
				Str("camera_id", cl.camera.ID).
				Strs("errors", result.Errors).
				Msg("Alert processing had errors")
		}

		if result.AlertsCreated > 0 {
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Int("alerts_created", result.AlertsCreated).
				Int("valid_detections", result.ValidDetections).
				Int("suppressed_detections", result.SuppressedDetections).
				Msg("Alert processing completed")
		}
	}
}
