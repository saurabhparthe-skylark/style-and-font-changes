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

// Service handles video capture operations with per-camera isolation
type Service struct {
	cfg *config.Config
	// Per-service (per-camera) capture state - NOT GLOBAL
	activeCaptureEntry *activeCaptureEntry
	activeCaptureMu    sync.Mutex
}

type activeCaptureEntry struct {
	cancel    context.CancelFunc
	done      chan struct{}
	startTime time.Time
	cap       *gocv.VideoCapture
	cameraID  string
}

var (
	// Only FFmpeg config should be global (configured once)
	ffmpegConfigured sync.Once
)

// NewService creates a new stream capture service
func NewService(cfg *config.Config) *Service {
	return &Service{
		cfg: cfg,
	}
}

// StopCameraCapture forcefully stops this service's capture process
func (s *Service) StopCameraCapture(cameraID string) {
	s.activeCaptureMu.Lock()
	defer s.activeCaptureMu.Unlock()

	if s.activeCaptureEntry != nil && s.activeCaptureEntry.cameraID == cameraID {
		log.Info().
			Str("camera_id", cameraID).
			Dur("runtime", time.Since(s.activeCaptureEntry.startTime)).
			Msg("Force stopping camera capture")

		// Cancel the context
		if s.activeCaptureEntry.cancel != nil {
			s.activeCaptureEntry.cancel()
		}

		// Wait briefly for it to signal done
		select {
		case <-s.activeCaptureEntry.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// CleanupAllCaptures stops this service's active capture (useful for shutdown)
func (s *Service) CleanupAllCaptures() {
	s.activeCaptureMu.Lock()
	defer s.activeCaptureMu.Unlock()

	if s.activeCaptureEntry != nil {
		log.Info().
			Str("camera_id", s.activeCaptureEntry.cameraID).
			Msg("Cleaning up camera capture")

		if s.activeCaptureEntry.cancel != nil {
			s.activeCaptureEntry.cancel()
		}

		s.activeCaptureEntry = nil
	}
}

// GetActiveCaptureCount returns 1 if this service has an active capture, 0 otherwise
func (s *Service) GetActiveCaptureCount() int {
	s.activeCaptureMu.Lock()
	defer s.activeCaptureMu.Unlock()
	if s.activeCaptureEntry != nil {
		return 1
	}
	return 0
}

// CleanupStaleCaptures removes this service's capture if it's stale
func (s *Service) CleanupStaleCaptures(maxAge time.Duration) {
	s.activeCaptureMu.Lock()
	defer s.activeCaptureMu.Unlock()

	if s.activeCaptureEntry != nil {
		now := time.Now()
		age := now.Sub(s.activeCaptureEntry.startTime)
		if age > maxAge {
			log.Warn().
				Str("camera_id", s.activeCaptureEntry.cameraID).
				Dur("age", age).
				Dur("max_age", maxAge).
				Msg("Cleaning up stale capture")

			if s.activeCaptureEntry.cancel != nil {
				s.activeCaptureEntry.cancel()
			}
		}
	}
}

// StartVideoCaptureProcess starts OpenCV VideoCapture for a camera
func (s *Service) StartVideoCaptureProcess(ctx context.Context, camera *models.Camera) error {
	// Per-service guard: ensure only one active capture per service
	runCtx, runCancel := context.WithCancel(ctx)
	var cap *gocv.VideoCapture

	// Always release runCancel and cleanup
	defer func() {
		runCancel()

		// Ensure VideoCapture is properly closed
		if cap != nil && cap.IsOpened() {
			log.Debug().Str("camera_id", camera.ID).Msg("Closing VideoCapture")
			cap.Close()
			log.Debug().Str("camera_id", camera.ID).Msg("VideoCapture closed")
		}

		// Clean up per-service entry
		s.activeCaptureMu.Lock()
		if s.activeCaptureEntry != nil && s.activeCaptureEntry.cameraID == camera.ID {
			close(s.activeCaptureEntry.done)
			s.activeCaptureEntry = nil
		}
		s.activeCaptureMu.Unlock()
	}()

	// Clean up any existing capture in THIS service first
	s.activeCaptureMu.Lock()
	if s.activeCaptureEntry != nil {
		log.Info().
			Str("camera_id", camera.ID).
			Dur("previous_runtime", time.Since(s.activeCaptureEntry.startTime)).
			Msg("Stopping existing capture in this service before starting new one")

		// Signal previously running capture to stop
		if s.activeCaptureEntry.cancel != nil {
			s.activeCaptureEntry.cancel()
		}

		prevDone := s.activeCaptureEntry.done
		s.activeCaptureMu.Unlock()

		// Wait for prior capture to fully release resources
		waitTimeout := 5 * time.Second
		select {
		case <-prevDone:
			log.Info().
				Str("camera_id", camera.ID).
				Msg("Previous capture in this service closed successfully")
		case <-time.After(waitTimeout):
			log.Warn().
				Str("camera_id", camera.ID).
				Dur("timeout", waitTimeout).
				Msg("Previous capture did not close within timeout - proceeding anyway as this is per-service")
		case <-ctx.Done():
			return nil
		}

		// Small delay to ensure cleanup
		time.Sleep(200 * time.Millisecond)

		s.activeCaptureMu.Lock()
	}

	// Create new entry for THIS service
	entry := &activeCaptureEntry{
		cancel:    runCancel,
		done:      make(chan struct{}),
		startTime: time.Now(),
		cap:       nil, // Will be set after successful open
		cameraID:  camera.ID,
	}
	s.activeCaptureEntry = entry
	s.activeCaptureMu.Unlock()

	log.Info().
		Str("camera_id", camera.ID).
		Str("url", camera.URL).
		Msg("Starting OpenCV VideoCapture with optimized FFmpeg settings (per-service isolation)")

	// Configure FFmpeg options ONCE globally (only env var setup)
	ffmpegConfigured.Do(func() {
		s.configureFFmpegOptions()
	})

	// Create VideoCapture - each service has its own isolated instance
	var err error
	cap, err = gocv.OpenVideoCaptureWithAPI(camera.URL, gocv.VideoCaptureFFmpeg)
	if err != nil {
		return fmt.Errorf("failed to open RTSP stream %s: %w", camera.URL, err)
	}

	// Store cap reference in per-service entry
	s.activeCaptureMu.Lock()
	if s.activeCaptureEntry != nil {
		s.activeCaptureEntry.cap = cap
	}
	s.activeCaptureMu.Unlock()

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
	consecutiveFailCount := 0

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

		img := gocv.NewMat()
		ok := cap.Read(&img)

		if !ok || img.Empty() {
			img.Close()
			if !cap.IsOpened() {
				log.Info().Str("camera_id", camera.ID).Msg("VideoCapture reports closed; stopping reader")
				return fmt.Errorf("stream closed for camera %s", camera.ID)
			}

			consecutiveFailCount++
			if consecutiveFailCount >= 100 { // ~10s with 100ms delay
				log.Error().
					Str("camera_id", camera.ID).
					Int("consecutive_failures", consecutiveFailCount).
					Msg("Too many consecutive read failures - forcing capture reopen")
				return fmt.Errorf("too many consecutive read failures for camera %s", camera.ID)
			}

			select {
			case <-runCtx.Done():
				log.Info().Str("camera_id", camera.ID).Msg("Context cancelled during read failure handling")
				return nil
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		consecutiveFailCount = 0

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
		img.Close()

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
// WARNING: This should only be called ONCE via sync.Once to avoid race conditions between cameras
func (s *Service) configureFFmpegOptions() {
	log.Info().Msg("Configuring optimized FFmpeg options GLOBALLY for OpenCV (called once)")

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

	// Configure FFmpeg options ONCE globally
	ffmpegConfigured.Do(func() {
		s.configureFFmpegOptions()
	})

	// Open VideoCapture for validation (temporary instance)
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

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	frame := gocv.NewMat()
	defer frame.Close()

	isFrameValid := func(mat gocv.Mat) bool {
		if mat.Empty() || mat.Cols() <= 0 || mat.Rows() <= 0 {
			return false
		}

		gray := gocv.NewMat()
		defer gray.Close()

		if mat.Channels() == 3 {
			gocv.CvtColor(mat, &gray, gocv.ColorBGRToGray)
		} else {
			gray = mat.Clone()
		}

		meanMat := gocv.NewMat()
		stddevMat := gocv.NewMat()
		defer meanMat.Close()
		defer stddevMat.Close()

		gocv.MeanStdDev(gray, &meanMat, &stddevMat)
		meanValue := meanMat.GetDoubleAt(0, 0)
		stddevValue := stddevMat.GetDoubleAt(0, 0)

		if meanValue < 15 || stddevValue < 8 {
			log.Debug().Float64("mean", meanValue).Float64("stddev", stddevValue).Msg("Frame rejected - too uniform or dark")
			return false
		}

		if meanValue > 110 && meanValue < 150 && stddevValue < 15 {
			log.Debug().Float64("mean", meanValue).Float64("stddev", stddevValue).Msg("Frame rejected - likely grey")
			return false
		}

		return true
	}

	log.Info().Msg("Skipping initial frames to avoid grey frames")

	skipFrames := 60
	for i := 0; i < skipFrames; i++ {
		select {
		case <-ctx.Done():
			response.ErrorDetail = "Timeout during initial frame skipping"
			log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
			return response
		default:
		}

		tempFrame := gocv.NewMat()
		cap.Read(&tempFrame)
		tempFrame.Close()
		time.Sleep(50 * time.Millisecond)
	}

	log.Info().Msg("Looking for valid non-grey frame for thumbnail")

	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		select {
		case <-ctx.Done():
			response.ErrorDetail = "Timeout during frame capture"
			log.Warn().Str("rtsp_url", rtspURL).Str("error", response.ErrorDetail).Msg("RTSP stream validation failed")
			return response
		default:
		}

		if cap.Read(&frame) && isFrameValid(frame) {
			log.Info().Int("attempt", i+1).Msg("Successfully captured valid non-grey frame")
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i == maxAttempts-1 {
			response.ErrorDetail = "Failed to capture valid non-grey frame after extensive attempts"
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
