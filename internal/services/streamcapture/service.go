package streamcapture

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
)

// Service handles video capture operations
type Service struct {
	cfg *config.Config
}

// NewService creates a new stream capture service
func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg: cfg,
	}
}

// StartVideoCaptureProcess starts OpenCV VideoCapture for a camera
func (s *Service) StartVideoCaptureProcess(ctx context.Context, camera *models.Camera) error {
	log.Info().
		Str("camera_id", camera.ID).
		Str("url", camera.URL).
		Msg("Starting OpenCV VideoCapture with optimized FFmpeg settings")

	// Configure FFmpeg options for OpenCV (similar to Python implementation)
	s.configureFFmpegOptions()

	// Create VideoCapture - handle webcam vs RTSP
	var cap *gocv.VideoCapture
	var err error

	// RTSP case
	log.Info().
		Str("camera_id", camera.ID).
		Str("rtsp_url", camera.URL).
		Msg("Opening RTSP stream with comprehensive FFmpeg options")

	cap, err = gocv.OpenVideoCaptureWithAPI(camera.URL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		return fmt.Errorf("failed to open RTSP stream %s: %w", camera.URL, err)
	}
	defer cap.Close()

	// Set comprehensive RTSP properties for optimal streaming
	s.configureVideoCaptureProperties(cap)

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
		// Check context cancellation more aggressively at the start of each loop iteration
		select {
		case <-ctx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Stopping VideoCapture reader due to context cancel")
			return nil
		case <-camera.StopChannel:
			log.Info().Str("camera_id", camera.ID).Msg("Stopping VideoCapture reader due to stop signal")
			return nil
		default:
		}

		// Also check context before reading frame
		select {
		case <-ctx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before frame read")
			return nil
		default:
		}

		ok := cap.Read(&img)

		if !ok {
			consecutiveErrors++
			log.Warn().
				Str("camera_id", camera.ID).
				Int("consecutive_errors", consecutiveErrors).
				Msg("Failed to read frame from VideoCapture")

			// Enhanced error recovery similar to Python implementation
			if consecutiveErrors >= maxConsecutiveErrors {
				log.Warn().
					Str("camera_id", camera.ID).
					Int("consecutive_errors", consecutiveErrors).
					Msg("Too many consecutive errors, attempting decoder reset")

				// Attempt to reset the capture (similar to Python's _reset_decoder)
				if s.resetVideoCapture(&cap, camera) {
					consecutiveErrors = 0
					log.Info().
						Str("camera_id", camera.ID).
						Msg("Successfully reset VideoCapture after errors")
					continue
				} else {
					return fmt.Errorf("failed to reset VideoCapture after %d consecutive errors", consecutiveErrors)
				}
			}

			// Progressive delay based on error count (similar to Python's backoff)
			delay := time.Duration(consecutiveErrors*50) * time.Millisecond
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}

			// Sleep with context cancellation check
			select {
			case <-ctx.Done():
				log.Info().Str("camera_id", camera.ID).Msg("Context cancelled during error backoff")
				return nil
			case <-time.After(delay):
			}
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
		if img.Cols() != s.cfg.OutputWidth || img.Rows() != s.cfg.OutputHeight {
			gocv.Resize(img, &processedImg, image.Pt(s.cfg.OutputWidth, s.cfg.OutputHeight), 0, 0, gocv.InterpolationLinear)
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
			Width:     s.cfg.OutputWidth,
			Height:    s.cfg.OutputHeight,
			Format:    "BGR24",
		}

		// Check context before sending to pipeline
		select {
		case <-ctx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before sending to pipeline")
			return nil
		default:
		}

		// Send to processing pipeline
		s.sendFrameToPipeline(camera, rawFrame, frameID)

		// Check context before FPS sleep
		select {
		case <-ctx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before FPS sleep")
			return nil
		default:
		}

		targetInterval := time.Second / time.Duration(s.getTargetFPS())

		// Sleep with context cancellation check
		select {
		case <-ctx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled during FPS sleep")
			return nil
		case <-time.After(targetInterval):
		}
	}
}

// sendFrameToPipeline sends frame to pipeline with buffer management and panic recovery
func (s *Service) sendFrameToPipeline(camera *models.Camera, rawFrame *models.RawFrame, frameID int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Debug().
				Str("camera_id", camera.ID).
				Int64("frame_id", frameID).
				Interface("panic", r).
				Msg("Recovered from panic during frame send - channel likely being replaced during restart")
		}
	}()

	// Use a helper function to safely check if channel is available
	rawFramesChan := camera.RawFrames
	if rawFramesChan == nil {
		log.Debug().
			Str("camera_id", camera.ID).
			Int64("frame_id", frameID).
			Msg("Raw frames channel is nil - skipping frame")
		return
	}

	select {
	case rawFramesChan <- rawFrame:
	default:
		// Buffer is full - just drop one frame and send current
		select {
		case <-rawFramesChan:
			// Dropped one frame
		default:
		}

		// Send current frame
		select {
		case rawFramesChan <- rawFrame:
		default:
			// Still full, skip this frame
			log.Debug().
				Str("camera_id", camera.ID).
				Int64("frame_id", frameID).
				Msg("Skipped frame - buffer full")
		}
	}
}

// CalculateFPS calculates current FPS for a camera using rolling window
func (s *Service) CalculateFPS(camera *models.Camera) float64 {
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
func (s *Service) getTargetFPS() int {
	if s.cfg.AIEnabled {
		return s.cfg.MaxFPSWithAI
	}
	return s.cfg.MaxFPSNoAI
}

// CalculateBackoffDelay calculates jittered exponential backoff delay
func (s *Service) CalculateBackoffDelay(attempt int) time.Duration {
	// Base delay with exponential backoff
	baseDelay := time.Duration(math.Pow(2, float64(attempt))) * time.Second

	// Clamp to configured min/max
	if baseDelay < s.cfg.ReconnectBackoffMin {
		baseDelay = s.cfg.ReconnectBackoffMin
	}
	if baseDelay > s.cfg.ReconnectBackoffMax {
		baseDelay = s.cfg.ReconnectBackoffMax
	}

	// Add jitter (random percentage of the delay)
	jitterPct := float64(s.cfg.ReconnectJitterPct) / 100.0
	jitter := time.Duration(float64(baseDelay) * jitterPct * (rand.Float64()*2 - 1))

	return baseDelay + jitter
}

// configureFFmpegOptions sets comprehensive FFmpeg options via environment variables
// This matches the configuration from the Python stream_handler.py implementation
func (s *Service) configureFFmpegOptions() {
	log.Info().Msg("Configuring comprehensive FFmpeg options for OpenCV")

	// FFmpeg options optimized for RTSP streaming (matching Python implementation)
	ffmpegOptions := map[string]string{
		"rtsp_transport":        "tcp",     // Use TCP for more reliable connection
		"buffer_size":           "2097152", // 2MB buffer - smaller for real-time
		"max_delay":             "500000",  // 0.5s max delay
		"stimeout":              "5000000", // 5s timeout
		"rw_timeout":            "5000000", // 5s read/write timeout
		"threads":               "1",       // Single thread
		"thread_type":           "slice",
		"flags":                 "low_delay",
		"fflags":                "nobuffer+flush_packets",
		"sync":                  "ext",
		"drop_pkts_on_overflow": "1",
		"max_error_rate":        "0.1",
		"skip_frame":            "default",
		"skip_loop_filter":      "none",
		"analyzeduration":       "500000",  // 0.5s analyze
		"probesize":             "2000000", // 2MB probe
		"err_detect":            "careful",
		"allowed_media_types":   "video",
		"reconnect":             "1",
		"reconnect_at_eof":      "1",
		"reconnect_streamed":    "1",
		"reconnect_delay_max":   "2",
	}

	// Build FFmpeg options string in the format expected by OpenCV
	var optionsBuilder []string
	for key, value := range ffmpegOptions {
		optionsBuilder = append(optionsBuilder, key+";"+value)
	}

	// Join all options with | delimiter
	ffmpegOptsStr := ""
	for i, option := range optionsBuilder {
		if i > 0 {
			ffmpegOptsStr += "|"
		}
		ffmpegOptsStr += option
	}

	// Set the environment variable that OpenCV FFmpeg backend will use
	os.Setenv("OPENCV_FFMPEG_CAPTURE_OPTIONS", ffmpegOptsStr)

	log.Info().
		Str("ffmpeg_options", ffmpegOptsStr).
		Msg("FFmpeg options configured for OpenCV")
}

// configureVideoCaptureProperties sets OpenCV VideoCapture properties for optimal streaming
func (s *Service) configureVideoCaptureProperties(cap *gocv.VideoCapture) {
	log.Info().Msg("Configuring VideoCapture properties")

	// Basic frame properties
	cap.Set(gocv.VideoCaptureFrameWidth, float64(s.cfg.OutputWidth))
	cap.Set(gocv.VideoCaptureFrameHeight, float64(s.cfg.OutputHeight))

	// Buffer settings for low latency (matching Python implementation)
	cap.Set(gocv.VideoCaptureBufferSize, 1) // Minimal buffer

	// RTSP specific properties - these are handled via FFmpeg environment variables
	// since some gocv versions may not have direct RTSP property constants

	// Additional capture properties for optimal streaming
	// Note: Some advanced properties are configured via FFmpeg environment variables

	log.Info().
		Int("output_width", s.cfg.OutputWidth).
		Int("output_height", s.cfg.OutputHeight).
		Msg("VideoCapture properties configured")
}

// resetVideoCapture resets the VideoCapture when encountering persistent errors
// This is similar to the _reset_decoder method in the Python implementation
func (s *Service) resetVideoCapture(cap **gocv.VideoCapture, camera *models.Camera) bool {
	log.Info().
		Str("camera_id", camera.ID).
		Str("url", camera.URL).
		Msg("Resetting VideoCapture due to consecutive errors")

	// Close the current capture
	if *cap != nil {
		(*cap).Close()
		*cap = nil
	}

	// Wait a moment before reconnecting
	time.Sleep(500 * time.Millisecond)

	// Configure recovery FFmpeg options with more conservative settings
	s.configureRecoveryFFmpegOptions()

	// Attempt to recreate the VideoCapture
	newCap, err := gocv.OpenVideoCaptureWithAPI(camera.URL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		log.Error().
			Str("camera_id", camera.ID).
			Str("url", camera.URL).
			Err(err).
			Msg("Failed to reset VideoCapture")
		return false
	}

	// Configure properties for the new capture
	s.configureVideoCaptureProperties(newCap)

	if !newCap.IsOpened() {
		newCap.Close()
		log.Error().
			Str("camera_id", camera.ID).
			Msg("Reset VideoCapture is not opened")
		return false
	}

	// Test read a few frames to ensure it's working
	testImg := gocv.NewMat()
	defer testImg.Close()

	testSuccess := false
	for i := 0; i < 3; i++ {
		if newCap.Read(&testImg) && !testImg.Empty() {
			testSuccess = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !testSuccess {
		newCap.Close()
		log.Error().
			Str("camera_id", camera.ID).
			Msg("Reset VideoCapture failed test reads")
		return false
	}

	*cap = newCap
	log.Info().
		Str("camera_id", camera.ID).
		Msg("VideoCapture reset successful")

	return true
}

// configureRecoveryFFmpegOptions sets more conservative FFmpeg options for recovery
func (s *Service) configureRecoveryFFmpegOptions() {
	log.Info().Msg("Configuring recovery FFmpeg options")

	// More conservative recovery options (similar to Python's recovery_options)
	recoveryOptions := map[string]string{
		"rtsp_transport":      "tcp",
		"buffer_size":         "5000000", // 1MB buffer for recovery
		"probesize":           "5000000", // 2MB probe for recovery
		"stimeout":            "5000000", // 5s timeout
		"fflags":              "nobuffer",
		"flags":               "low_delay",
		"max_delay":           "3000000", // 1s max delay
		"analyzeduration":     "500000",  // 0.25s analyze
		"err_detect":          "careful",
		"reconnect":           "1",
		"reconnect_streamed":  "1",
		"reconnect_delay_max": "1",
		"threads":             "1",
		"thread_type":         "slice",
	}

	// Build recovery options string
	var optionsBuilder []string
	for key, value := range recoveryOptions {
		optionsBuilder = append(optionsBuilder, key+";"+value)
	}

	ffmpegOptsStr := ""
	for i, option := range optionsBuilder {
		if i > 0 {
			ffmpegOptsStr += "|"
		}
		ffmpegOptsStr += option
	}

	os.Setenv("OPENCV_FFMPEG_CAPTURE_OPTIONS", ffmpegOptsStr)

	log.Info().
		Str("recovery_options", ffmpegOptsStr).
		Msg("Recovery FFmpeg options configured")
}

// ValidateRTSPAndCaptureThumbnail validates RTSP stream and captures HD thumbnail using optimized FFmpeg settings
func (s *Service) ValidateRTSPAndCaptureThumbnail(rtspURL string) *models.RTSPCheckResponse {
	response := &models.RTSPCheckResponse{
		Valid:   false,
		Message: "RTSP stream validation failed",
	}

	log.Info().Str("rtsp_url", rtspURL).Msg("Starting RTSP validation with optimized FFmpeg settings")

	// Configure comprehensive FFmpeg options for validation
	s.configureFFmpegOptions()

	// Try to open the RTSP stream with FFmpeg backend
	cap, err := gocv.OpenVideoCaptureWithAPI(rtspURL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to open RTSP stream: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer cap.Close()

	// Configure VideoCapture properties
	s.configureVideoCaptureProperties(cap)

	// Check if capture is opened
	if !cap.IsOpened() {
		response.ErrorDetail = "RTSP stream could not be opened"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	// Set timeout for frame reading (10 seconds for validation)
	timeout := time.After(10 * time.Second)
	frameReadSuccess := make(chan bool, 1)

	var frame gocv.Mat
	var actualFPS, actualWidth, actualHeight float64

	// Try to read frame in goroutine to handle timeout
	go func() {
		frame = gocv.NewMat()

		// Try to read several frames to ensure stream is stable
		for i := 0; i < 5; i++ {
			if cap.Read(&frame) && !frame.Empty() {
				frameReadSuccess <- true
				return
			}
			time.Sleep(200 * time.Millisecond) // Longer delay for validation
		}
		frameReadSuccess <- false
	}()

	// Wait for frame read or timeout
	select {
	case success := <-frameReadSuccess:
		if !success {
			if !frame.Empty() {
				frame.Close()
			}
			response.ErrorDetail = "Failed to read stable frames from RTSP stream"
			log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
			return response
		}
	case <-timeout:
		if !frame.Empty() {
			frame.Close()
		}
		response.ErrorDetail = "Timeout reading from RTSP stream (10s limit)"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	defer frame.Close()

	// Get stream properties
	actualFPS = cap.Get(gocv.VideoCaptureFPS)
	actualWidth = cap.Get(gocv.VideoCaptureFrameWidth)
	actualHeight = cap.Get(gocv.VideoCaptureFrameHeight)

	// Validate frame dimensions
	if frame.Cols() <= 0 || frame.Rows() <= 0 {
		response.ErrorDetail = "Invalid frame dimensions"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	// Resize to HD if needed (1280x720 for HD thumbnail)
	hdWidth, hdHeight := 1280, 720
	var resizedFrame gocv.Mat

	if frame.Cols() != hdWidth || frame.Rows() != hdHeight {
		resizedFrame = gocv.NewMat()
		gocv.Resize(frame, &resizedFrame, image.Pt(hdWidth, hdHeight), 0, 0, gocv.InterpolationLinear)
	} else {
		resizedFrame = frame.Clone()
	}
	defer resizedFrame.Close()

	// Convert to JPEG with high quality
	jpegData, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, resizedFrame, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to encode thumbnail: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer jpegData.Close()

	// Convert to base64 data URL
	thumbnail := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData.GetBytes())

	// Success response
	response.Valid = true
	response.Message = "RTSP stream is valid and accessible"
	response.Thumbnail = thumbnail
	response.Width = int(actualWidth)
	response.Height = int(actualHeight)
	response.FPS = actualFPS
	response.ErrorDetail = ""

	log.Info().
		Str("rtsp_url", rtspURL).
		Int("width", response.Width).
		Int("height", response.Height).
		Float64("fps", response.FPS).
		Msg("RTSP stream validation successful with optimized settings")

	return response
}
