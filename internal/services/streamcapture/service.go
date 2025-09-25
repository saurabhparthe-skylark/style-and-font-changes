package streamcapture

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"os"
	"sync"
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

type activeCaptureEntry struct {
	cancel    context.CancelFunc
	done      chan struct{}
	startTime time.Time
	cap       *gocv.VideoCapture
}

var (
	activeCaptureMu sync.Mutex
	activeCaptures  = make(map[string]*activeCaptureEntry)
)

// NewService creates a new stream capture service
func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg: cfg,
	}
}

// StopCameraCapture forcefully stops a camera's capture process
func (s *Service) StopCameraCapture(cameraID string) {
	activeCaptureMu.Lock()
	defer activeCaptureMu.Unlock()

	if existing, ok := activeCaptures[cameraID]; ok {
		log.Info().
			Str("camera_id", cameraID).
			Dur("runtime", time.Since(existing.startTime)).
			Msg("Force stopping camera capture")

		// Cancel the context
		if existing.cancel != nil {
			existing.cancel()
		}

		// Close the VideoCapture immediately
		if existing.cap != nil && existing.cap.IsOpened() {
			existing.cap.Close()
		}

		// Remove from active captures
		delete(activeCaptures, cameraID)
	}
}

// CleanupAllCaptures stops all active captures (useful for shutdown)
func (s *Service) CleanupAllCaptures() {
	activeCaptureMu.Lock()
	defer activeCaptureMu.Unlock()

	log.Info().
		Int("active_captures", len(activeCaptures)).
		Msg("Cleaning up all active captures")

	for cameraID, existing := range activeCaptures {
		if existing.cancel != nil {
			existing.cancel()
		}

		if existing.cap != nil && existing.cap.IsOpened() {
			existing.cap.Close()
		}

		log.Debug().
			Str("camera_id", cameraID).
			Msg("Cleaned up capture")
	}

	// Clear the map
	activeCaptures = make(map[string]*activeCaptureEntry)
}

// GetActiveCaptureCount returns the number of active captures (for monitoring)
func (s *Service) GetActiveCaptureCount() int {
	activeCaptureMu.Lock()
	defer activeCaptureMu.Unlock()
	return len(activeCaptures)
}

// CleanupStaleCaptures removes captures that have been running too long without activity
func (s *Service) CleanupStaleCaptures(maxAge time.Duration) {
	activeCaptureMu.Lock()
	defer activeCaptureMu.Unlock()

	now := time.Now()
	for cameraID, existing := range activeCaptures {
		age := now.Sub(existing.startTime)
		if age > maxAge {
			log.Warn().
				Str("camera_id", cameraID).
				Dur("age", age).
				Dur("max_age", maxAge).
				Msg("Cleaning up stale capture")

			if existing.cancel != nil {
				existing.cancel()
			}

			if existing.cap != nil && existing.cap.IsOpened() {
				existing.cap.Close()
			}

			delete(activeCaptures, cameraID)
		}
	}
}

// StartVideoCaptureProcess starts OpenCV VideoCapture for a camera
func (s *Service) StartVideoCaptureProcess(ctx context.Context, camera *models.Camera) error {
	// Per-camera guard: ensure only one active capture exists
	runCtx, runCancel := context.WithCancel(ctx)
	var entry *activeCaptureEntry
	var cap *gocv.VideoCapture

	// Always release runCancel; entry cleanup happens only if created
	defer func() {
		runCancel()

		// Ensure VideoCapture is properly closed
		if cap != nil && cap.IsOpened() {
			cap.Close()
		}

		activeCaptureMu.Lock()
		if entry != nil {
			if cur, ok := activeCaptures[camera.ID]; ok && cur == entry {
				delete(activeCaptures, camera.ID)
			}
			close(entry.done)
		}
		activeCaptureMu.Unlock()
	}()

	// Clean up any existing capture first
	activeCaptureMu.Lock()
	if existing, ok := activeCaptures[camera.ID]; ok {
		log.Info().
			Str("camera_id", camera.ID).
			Dur("previous_runtime", time.Since(existing.startTime)).
			Msg("Stopping existing capture before starting new one")

		// Signal previously running capture to stop
		if existing.cancel != nil {
			existing.cancel()
		}

		// Close the existing VideoCapture immediately if we have reference
		if existing.cap != nil && existing.cap.IsOpened() {
			existing.cap.Close()
		}

		prevDone := existing.done
		activeCaptureMu.Unlock()

		// Wait for prior capture to fully release resources
		waitTimeout := 2 * time.Second
		select {
		case <-prevDone:
			log.Info().
				Str("camera_id", camera.ID).
				Msg("Previous capture closed successfully")
		case <-time.After(waitTimeout):
			log.Error().
				Str("camera_id", camera.ID).
				Dur("timeout", waitTimeout).
				Msg("Previous capture failed to close within timeout - forcing cleanup")

			// Force remove the entry
			activeCaptureMu.Lock()
			if cur, ok := activeCaptures[camera.ID]; ok && cur == existing {
				delete(activeCaptures, camera.ID)
			}
			activeCaptureMu.Unlock()
		case <-ctx.Done():
			return nil
		}

		// Add a small delay to ensure FFmpeg process is fully terminated
		time.Sleep(500 * time.Millisecond)

		activeCaptureMu.Lock()
	}

	// Create new entry
	entry = &activeCaptureEntry{
		cancel:    runCancel,
		done:      make(chan struct{}),
		startTime: time.Now(),
		cap:       nil, // Will be set after successful open
	}
	activeCaptures[camera.ID] = entry
	activeCaptureMu.Unlock()

	log.Info().
		Str("camera_id", camera.ID).
		Str("url", camera.URL).
		Msg("Starting OpenCV VideoCapture with optimized FFmpeg settings")

	// Configure FFmpeg options for OpenCV (similar to Python implementation)
	s.configureFFmpegOptions()

	// Create VideoCapture - handle webcam vs RTSP
	var err error

	cap, err = gocv.OpenVideoCaptureWithAPI(camera.URL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		return fmt.Errorf("failed to open RTSP stream %s: %w", camera.URL, err)
	}

	activeCaptureMu.Lock()
	if entry != nil {
		entry.cap = cap
	}
	activeCaptureMu.Unlock()

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

	for {
		select {
		case <-runCtx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Stopping VideoCapture reader due to context cancel")
			return nil
		case <-camera.StopChannel:
			log.Info().Str("camera_id", camera.ID).Msg("Stopping VideoCapture reader due to stop signal")
			return nil
		default:
		}

		select {
		case <-runCtx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before frame read")
			return nil
		default:
		}

		ok := cap.Read(&img)

		if !ok || img.Empty() {
			if !cap.IsOpened() {
				log.Info().Str("camera_id", camera.ID).Msg("VideoCapture reports closed; stopping reader")
				return fmt.Errorf("stream closed for camera %s", camera.ID)
			}

			select {
			case <-runCtx.Done():
				log.Info().Str("camera_id", camera.ID).Msg("Context cancelled during read failure handling")
				return nil
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		frameID++
		camera.FrameCount++
		camera.LastFrameTime = time.Now()

		processedImg := gocv.NewMat()
		if img.Cols() != s.cfg.OutputWidth || img.Rows() != s.cfg.OutputHeight {
			gocv.Resize(img, &processedImg, image.Pt(s.cfg.OutputWidth, s.cfg.OutputHeight), 0, 0, gocv.InterpolationLinear)
		} else {
			processedImg = img.Clone()
		}

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

		select {
		case <-runCtx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before sending to pipeline")
			return nil
		default:
		}

		s.sendFrameToPipeline(camera, rawFrame, frameID)

		select {
		case <-runCtx.Done():
			log.Info().Str("camera_id", camera.ID).Msg("Context cancelled before FPS sleep")
			return nil
		default:
		}

		targetInterval := time.Second / time.Duration(s.getTargetFPS(camera))

		select {
		case <-runCtx.Done():
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
		select {
		case <-rawFramesChan:
		default:
		}

		select {
		case rawFramesChan <- rawFrame:
		default:
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

	now := time.Now()
	camera.RecentFrameTimes = append(camera.RecentFrameTimes, now)

	if len(camera.RecentFrameTimes) > camera.FPSWindowSize {
		camera.RecentFrameTimes = camera.RecentFrameTimes[1:]
	}

	if len(camera.RecentFrameTimes) < 2 {
		return 0
	}

	timeSpan := camera.RecentFrameTimes[len(camera.RecentFrameTimes)-1].Sub(camera.RecentFrameTimes[0]).Seconds()
	if timeSpan > 0 {
		frameCount := float64(len(camera.RecentFrameTimes) - 1)
		return frameCount / timeSpan
	}

	return 0
}

// getTargetFPS returns target FPS based on AI status
func (s *Service) getTargetFPS(camera *models.Camera) int {
	if camera != nil && camera.AIEnabled {
		return s.cfg.MaxFPSWithAI
	}
	return s.cfg.MaxFPSNoAI
}

// CalculateBackoffDelay calculates jittered exponential backoff delay
func (s *Service) CalculateBackoffDelay(attempt int) time.Duration {
	baseDelay := time.Duration(math.Pow(2, float64(attempt))) * time.Second

	if baseDelay < s.cfg.ReconnectBackoffMin {
		baseDelay = s.cfg.ReconnectBackoffMin
	}
	if baseDelay > s.cfg.ReconnectBackoffMax {
		baseDelay = s.cfg.ReconnectBackoffMax
	}

	jitterPct := float64(s.cfg.ReconnectJitterPct) / 100.0
	jitter := time.Duration(float64(baseDelay) * jitterPct * (rand.Float64()*2 - 1))

	return baseDelay + jitter
}

// configureFFmpegOptions sets comprehensive FFmpeg options via environment variables
func (s *Service) configureFFmpegOptions() {
	log.Info().Msg("Configuring optimized FFmpeg options for OpenCV to avoid grey frames")

	ffmpegOptions := map[string]string{
		"rtsp_transport":              "tcp",
		"rtsp_flags":                  "prefer_tcp",
		"stimeout":                    "10000000",
		"rw_timeout":                  "10000000",
		"buffer_size":                 "1048576",
		"max_delay":                   "100000",
		"reorder_queue_size":          "0",
		"analyzeduration":             "2000000",
		"probesize":                   "4000000",
		"max_probe_packets":           "100",
		"threads":                     "1",
		"thread_type":                 "slice",
		"flags":                       "low_delay+global_header",
		"fflags":                      "nobuffer+flush_packets+discardcorrupt+genpts",
		"sync":                        "ext",
		"drop_pkts_on_overflow":       "1",
		"flush_packets":               "1",
		"skip_frame":                  "nokey",
		"skip_loop_filter":            "none",
		"skip_idct":                   "none",
		"max_error_rate":              "0.05",
		"err_detect":                  "careful",
		"allowed_media_types":         "video",
		"reconnect":                   "1",
		"reconnect_at_eof":            "1",
		"reconnect_streamed":          "1",
		"reconnect_delay_max":         "3",
		"avoid_negative_ts":           "make_zero",
		"use_wallclock_as_timestamps": "1",
		"fpsprobesize":                "3",
	}

	var optionsBuilder []string
	for key, value := range ffmpegOptions {
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
		Str("ffmpeg_options", ffmpegOptsStr).
		Msg("FFmpeg options configured for OpenCV")
}

// configureVideoCaptureProperties sets OpenCV VideoCapture properties for optimal streaming
func (s *Service) configureVideoCaptureProperties(cap *gocv.VideoCapture) {
	log.Info().Msg("Configuring VideoCapture properties for grey frame prevention")

	cap.Set(gocv.VideoCaptureBufferSize, 1)
	cap.Set(gocv.VideoCaptureFPS, 0)

	log.Info().Msg("VideoCapture properties configured for optimal grey frame prevention")
}

// ValidateRTSPAndCaptureThumbnail validates RTSP stream and captures HD thumbnail
func (s *Service) ValidateRTSPAndCaptureThumbnail(rtspURL string) *models.RTSPCheckResponse {
	response := &models.RTSPCheckResponse{
		Valid:   false,
		Message: "RTSP stream validation failed",
	}

	log.Info().Str("rtsp_url", rtspURL).Msg("Starting RTSP validation")

	s.configureFFmpegOptions()

	cap, err := gocv.OpenVideoCaptureWithAPI(rtspURL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to open RTSP stream: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer cap.Close()

	s.configureVideoCaptureProperties(cap)

	if !cap.IsOpened() {
		response.ErrorDetail = "RTSP stream could not be opened"
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}

	frame := gocv.NewMat()
	defer frame.Close()

	maxAttempts := 10
	for i := 0; i < maxAttempts; i++ {
		if cap.Read(&frame) && !frame.Empty() && frame.Cols() > 0 && frame.Rows() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i == maxAttempts-1 {
			response.ErrorDetail = "Failed to capture valid frame"
			log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
			return response
		}
	}

	actualFPS := cap.Get(gocv.VideoCaptureFPS)
	actualWidth := cap.Get(gocv.VideoCaptureFrameWidth)
	actualHeight := cap.Get(gocv.VideoCaptureFrameHeight)

	hdWidth, hdHeight := 1280, 720
	resizedFrame := gocv.NewMat()
	defer resizedFrame.Close()

	if frame.Cols() != hdWidth || frame.Rows() != hdHeight {
		gocv.Resize(frame, &resizedFrame, image.Pt(hdWidth, hdHeight), 0, 0, gocv.InterpolationLinear)
	} else {
		resizedFrame = frame.Clone()
	}

	jpegData, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, resizedFrame, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		response.ErrorDetail = fmt.Sprintf("Failed to encode thumbnail: %v", err)
		log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
		return response
	}
	defer jpegData.Close()

	thumbnail := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData.GetBytes())

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
		Msg("RTSP stream validation successful")

	return response
}
