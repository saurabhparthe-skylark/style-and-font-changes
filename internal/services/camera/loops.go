package camera

import (
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/models"
)

// startFrameProcessors starts background frame processors with completely isolated resources
func (cl *CameraLifecycle) startFrameProcessors() {
	log.Debug().Str("camera_id", cl.camera.ID).Msg("Starting completely isolated frame processors")

	// Start frame processor in background
	go cl.runFrameProcessor()

	// Start publisher in background
	go cl.runPublisher()

	// Start alert processor in background
	go cl.runAlertProcessor()
}

// runFrameProcessor processes frames with dedicated processor and error recovery
func (cl *CameraLifecycle) runFrameProcessor() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Frame processor panic recovered")
		}
	}()

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Dedicated frame processor started")

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Frame processor stopping")
			return
		case rawFrame, ok := <-cl.camera.RawFrames:
			if !ok {
				log.Debug().Str("camera_id", cl.camera.ID).Msg("Raw frames channel closed")
				return
			}
			cl.processFrame(rawFrame)
		case <-time.After(1 * time.Second):
			// Timeout to check if we should stop
			continue
		}
	}

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Dedicated frame processor stopped")
}

// processFrame processes a single frame safely with completely isolated resources
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

	startTime := time.Now()

	// Update camera statistics using isolated stream capture service
	streamCaptureSvc := cl.getStreamCaptureService()
	if streamCaptureSvc != nil {
		cl.camera.FPS = streamCaptureSvc.CalculateFPS(cl.camera)
	}
	cl.camera.Latency = time.Since(rawFrame.Timestamp)

	// Get dedicated frame processor for this camera (completely isolated!)
	frameProcessor := cl.getFrameProcessor()

	// Process the frame - with AI bypass if needed
	var processedFrame *models.ProcessedFrame
	if frameProcessor != nil {
		// Use dedicated processor (no conflicts with other cameras!)
		processedFrame = frameProcessor.ProcessFrame(rawFrame, cl.camera.Projects, cl.camera.FPS, cl.camera.Latency)
	} else {
		// Fallback: create basic processed frame without AI
		log.Debug().
			Str("camera_id", cl.camera.ID).
			Msg("No frame processor available, creating basic frame")

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

	// Update AI statistics safely
	if processedFrame != nil && processedFrame.AIDetections != nil {
		if aiResult, ok := processedFrame.AIDetections.(*models.AIProcessingResult); ok {
			cl.camera.AIProcessingTime = aiResult.ProcessingTime
			cl.camera.AIDetectionCount += int64(len(aiResult.Detections))

			if aiResult.ErrorMessage != "" {
				cl.camera.LastAIError = aiResult.ErrorMessage
				cl.camera.ErrorCount++
				log.Warn().
					Str("camera_id", cl.camera.ID).
					Str("ai_error", aiResult.ErrorMessage).
					Msg("AI processing error")
			} else if aiResult.FrameProcessed {
				cl.camera.LastAIError = ""
			}
		}
	}

	processingTime := time.Since(startTime)
	log.Debug().
		Str("camera_id", cl.camera.ID).
		Dur("processing_time", processingTime).
		Bool("ai_enabled", cl.camera.AIEnabled).
		Bool("has_dedicated_processor", frameProcessor != nil).
		Msg("Frame processed with completely isolated resources")

	// Send to other processors (non-blocking)
	if processedFrame != nil {
		select {
		case cl.camera.ProcessedFrames <- processedFrame:
		default:
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Msg("Processed frames buffer full, dropping frame")
		}

		select {
		case cl.camera.AlertFrames <- processedFrame:
		default:
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Msg("Alert frames buffer full, dropping frame")
		}

		select {
		case cl.camera.RecorderFrames <- processedFrame:
		default:
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Msg("Recorder frames buffer full, dropping frame")
		}
	}
}

// runPublisher publishes frames with isolated publisher service
func (cl *CameraLifecycle) runPublisher() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Publisher panic recovered")
		}
	}()

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Isolated publisher started")

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Publisher stopping")
			return
		case processedFrame, ok := <-cl.camera.ProcessedFrames:
			if !ok {
				log.Debug().Str("camera_id", cl.camera.ID).Msg("Processed frames channel closed")
				return
			}
			cl.publishFrame(processedFrame)
		case <-time.After(1 * time.Second):
			// Timeout to check if we should stop
			continue
		}
	}

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Isolated publisher stopped")
}

// publishFrame publishes a single frame safely using isolated publisher service
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

	// Use dedicated publisher service (completely isolated!)
	publisherSvc := cl.getPublisherService()
	if publisherSvc == nil {
		log.Error().
			Str("camera_id", cl.camera.ID).
			Msg("No publisher service available")
		return
	}

	err := publisherSvc.PublishFrame(processedFrame)
	if err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cl.camera.ID).
			Msg("Failed to publish frame")
		cl.camera.ErrorCount++
	} else {
		log.Debug().
			Str("camera_id", cl.camera.ID).
			Int64("frame_id", processedFrame.FrameID).
			Msg("Frame published successfully with isolated service")
	}
}

// runAlertProcessor processes alert frames
func (cl *CameraLifecycle) runAlertProcessor() {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Alert processor panic recovered")
		}
	}()

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Alert processor started")

	for cl.isRunning() {
		select {
		case <-cl.ctx.Done():
			log.Debug().Str("camera_id", cl.camera.ID).Msg("Alert processor stopping")
			return
		case processedFrame, ok := <-cl.camera.AlertFrames:
			if !ok {
				log.Debug().Str("camera_id", cl.camera.ID).Msg("Alert frames channel closed")
				return
			}
			cl.processAlert(processedFrame)
		case <-time.After(1 * time.Second):
			// Timeout to check if we should stop
			continue
		}
	}

	log.Debug().Str("camera_id", cl.camera.ID).Msg("Alert processor stopped")
}

// processAlert processes a single alert frame safely
func (cl *CameraLifecycle) processAlert(processedFrame *models.ProcessedFrame) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", cl.camera.ID).
				Interface("panic", r).
				Msg("Process alert panic recovered")
		}
	}()

	// Only process alerts if AI is enabled
	if !cl.camera.AIEnabled || processedFrame == nil {
		return
	}

	// Use post-processing service from camera manager (this can be shared as it's stateless)
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

		if result.AlertsCreated > 0 {
			log.Debug().
				Str("camera_id", cl.camera.ID).
				Int("alerts_created", result.AlertsCreated).
				Msg("Alerts processed successfully")
		}
	}
}
