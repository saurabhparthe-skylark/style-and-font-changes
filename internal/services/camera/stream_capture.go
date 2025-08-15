package camera

import (
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
)

// StreamCapture handles video capture operations
type StreamCapture struct {
	cfg *config.Config
}

// NewStreamCapture creates a new stream capture service
func NewStreamCapture(cfg *config.Config) *StreamCapture {
	return &StreamCapture{
		cfg: cfg,
	}
}

// StartVideoCaptureProcess starts OpenCV VideoCapture for a camera
func (sc *StreamCapture) StartVideoCaptureProcess(camera *models.Camera) error {
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
	cap.Set(gocv.VideoCaptureFrameWidth, float64(sc.cfg.OutputWidth))
	cap.Set(gocv.VideoCaptureFrameHeight, float64(sc.cfg.OutputHeight))

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
			if img.Cols() != sc.cfg.OutputWidth || img.Rows() != sc.cfg.OutputHeight {
				gocv.Resize(img, &processedImg, image.Pt(sc.cfg.OutputWidth, sc.cfg.OutputHeight), 0, 0, gocv.InterpolationLinear)
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
				Width:     sc.cfg.OutputWidth,
				Height:    sc.cfg.OutputHeight,
				Format:    "BGR24",
			}

			// Send to processing pipeline
			sc.sendFrameToPipeline(camera, rawFrame, frameID)

			targetInterval := time.Second / time.Duration(sc.getTargetFPS())
			time.Sleep(targetInterval)
		}
	}
}

// sendFrameToPipeline sends frame to pipeline with buffer management
func (sc *StreamCapture) sendFrameToPipeline(camera *models.Camera, rawFrame *models.RawFrame, frameID int64) {
	select {
	case camera.RawFrames <- rawFrame:
		log.Debug().
			Str("camera_id", camera.ID).
			Int64("frame_id", frameID).
			Msg("Frame sent to processing pipeline")
	default:
		// Buffer is full - implement frame dropping for real-time streaming
		droppedRaw := 0
		// Drain some frames to make space for the latest
	DrainRawLoop:
		for len(camera.RawFrames) > sc.cfg.FrameBufferSize/2 {
			select {
			case <-camera.RawFrames:
				droppedRaw++
			default:
				break DrainRawLoop
			}
		}

		// Now try to send the current frame
		select {
		case camera.RawFrames <- rawFrame:
			if droppedRaw > 0 {
				log.Debug().
					Str("camera_id", camera.ID).
					Int64("frame_id", frameID).
					Int("dropped_raw_frames", droppedRaw).
					Msg("Dropped older raw frames for real-time capture")
			}
		default:
			// Still full, just skip this frame
			log.Debug().
				Str("camera_id", camera.ID).
				Int64("frame_id", frameID).
				Msg("Skipped raw frame - buffer still full after draining")
		}
	}
}

// CalculateFPS calculates current FPS for a camera using rolling window
func (sc *StreamCapture) CalculateFPS(camera *models.Camera) float64 {
	if camera.FrameCount < 2 {
		return 0
	}

	// Add current timestamp to the rolling window
	now := time.Now()
	camera.RecentFrameTimes = append(camera.RecentFrameTimes, now)

	// Keep only the most recent N timestamps
	if len(camera.RecentFrameTimes) > camera.FPSWindowSize {
		camera.RecentFrameTimes = camera.RecentFrameTimes[1:]
	}

	// Need at least 2 timestamps to calculate FPS
	if len(camera.RecentFrameTimes) < 2 {
		return 0
	}

	// Calculate FPS based on time difference between first and last timestamp in window
	timeSpan := camera.RecentFrameTimes[len(camera.RecentFrameTimes)-1].Sub(camera.RecentFrameTimes[0]).Seconds()
	if timeSpan > 0 {
		frameCount := float64(len(camera.RecentFrameTimes) - 1)
		return frameCount / timeSpan
	}

	return 0
}

// getTargetFPS returns target FPS based on AI status
func (sc *StreamCapture) getTargetFPS() int {
	if sc.cfg.AIEnabled {
		return sc.cfg.MaxFPSWithAI
	}
	return sc.cfg.MaxFPSNoAI
}

// CalculateBackoffDelay calculates jittered exponential backoff delay
func (sc *StreamCapture) CalculateBackoffDelay(attempt int) time.Duration {
	// Base delay with exponential backoff
	baseDelay := time.Duration(math.Pow(2, float64(attempt))) * time.Second

	// Clamp to configured min/max
	if baseDelay < sc.cfg.ReconnectBackoffMin {
		baseDelay = sc.cfg.ReconnectBackoffMin
	}
	if baseDelay > sc.cfg.ReconnectBackoffMax {
		baseDelay = sc.cfg.ReconnectBackoffMax
	}

	// Add jitter (random percentage of the delay)
	jitterPct := float64(sc.cfg.ReconnectJitterPct) / 100.0
	jitter := time.Duration(float64(baseDelay) * jitterPct * (rand.Float64()*2 - 1))

	return baseDelay + jitter
}
