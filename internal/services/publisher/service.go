package publisher

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
)

// PublishMethod defines the publishing method to MediaMTX
type PublishMethod string

const (
	PublishMethodSRT    PublishMethod = "srt"
	PublishMethodWebRTC PublishMethod = "webrtc"
)

// Service handles publishing video streams to external media servers
type Service struct {
	cfg *config.Config

	// MediaMTX streams per camera (SRT or WebRTC)
	streamMutex sync.RWMutex
	streams     map[string]*MediaMTXStream

	// Publishing method configuration
	publishMethod PublishMethod
}

// MediaMTXStream represents an ultra-low latency stream to MediaMTX (SRT or WebRTC)
type MediaMTXStream struct {
	cameraID    string
	process     *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	frameBuffer chan *models.ProcessedFrame
	isRunning   bool
	stopChannel chan struct{}

	// Publishing method for this stream
	method PublishMethod

	// WebRTC WHIP specific fields
	whipURL      string
	whipClient   *http.Client
	whipResource string
	pc           *webrtc.PeerConnection
	videoTrack   *webrtc.TrackLocalStaticSample

	// Conservative frame control
	targetFPS     float64
	frameInterval time.Duration
	lastFrameTime time.Time
	frameCount    int64
	droppedFrames int64
}

// NewService creates a new publisher service with default SRT method
func NewService(cfg *config.Config) (*Service, error) {
	return NewServiceWithMethod(cfg, PublishMethodSRT)
}

// NewServiceWithMethod creates a new publisher service with specified method
func NewServiceWithMethod(cfg *config.Config, method PublishMethod) (*Service, error) {
	service := &Service{
		cfg:           cfg,
		streams:       make(map[string]*MediaMTXStream),
		publishMethod: method,
	}

	methodName := "SRT"
	if method == PublishMethodWebRTC {
		methodName = "WebRTC WHIP"
	}

	log.Info().
		Str("mediamtx_url", cfg.MediaMTXURL).
		Str("publish_method", string(method)).
		Msgf("Ultra-low latency %s Publisher service initialized with MediaMTX", methodName)

	return service, nil
}

// PublishFrame publishes a processed frame to MediaMTX
func (s *Service) PublishFrame(frame *models.ProcessedFrame) error {
	// Get or create MediaMTX stream for this camera
	stream, err := s.getOrCreateMediaMTXStream(frame.CameraID, frame.Width, frame.Height)
	if err != nil {
		return fmt.Errorf("failed to get MediaMTX stream for camera %s: %w", frame.CameraID, err)
	}

	// Send frame to stream processing (non-blocking with aggressive dropping)
	select {
	case stream.frameBuffer <- frame:
	default:
		// Drop frame if buffer is full to prevent MediaMTX overflow
		stream.droppedFrames++
		log.Debug().
			Str("camera_id", frame.CameraID).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("Dropped frame for MediaMTX (preventing overflow)")
	}

	return nil
}

func (s *Service) getOrCreateMediaMTXStream(cameraID string, width, height int) (*MediaMTXStream, error) {
	s.streamMutex.Lock()
	defer s.streamMutex.Unlock()

	// Return existing stream if running
	if stream, exists := s.streams[cameraID]; exists && stream.isRunning {
		return stream, nil
	}

	// Create new MediaMTX stream based on configured method
	stream, err := s.createMediaMTXStream(cameraID, width, height)
	if err != nil {
		return nil, err
	}

	s.streams[cameraID] = stream
	return stream, nil
}

func (s *Service) createMediaMTXStream(cameraID string, width, height int) (*MediaMTXStream, error) {
	switch s.publishMethod {
	case PublishMethodSRT:
		return s.createSRTStream(cameraID, width, height)
	case PublishMethodWebRTC:
		return s.createWebRTCStream(cameraID, width, height)
	default:
		return nil, fmt.Errorf("unsupported publish method: %s", s.publishMethod)
	}
}

func (s *Service) createSRTStream(cameraID string, width, height int) (*MediaMTXStream, error) {
	// MediaMTX SRT publish URL
	srtURL := fmt.Sprintf("srt://localhost:8890?pkt_size=1316&latency=40&streamid=#!::r=live/%s,m=publish", cameraID)

	// Ultra-low latency FFmpeg command with SRT - optimized for WebRTC
	args := []string{
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-s", "640x480", // Better resolution for WebRTC
		"-r", "15", // Higher frame rate for smoother WebRTC
		"-i", "-", // Read from stdin
		"-c:v", "libx264",
		"-preset", "ultrafast", // Fastest encoding for low latency
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-level", "3.1",
		"-pix_fmt", "yuv420p",
		"-g", "15", // GOP of 15 (1 second at 15 FPS)
		"-keyint_min", "15",
		"-sc_threshold", "0",
		"-b:v", "500k", // Higher bitrate for better quality
		"-maxrate", "750k",
		"-bufsize", "250k", // Smaller buffer for lower latency
		"-rc-lookahead", "0",
		"-threads", "2",
		"-flags", "+global_header",
		"-avoid_negative_ts", "make_zero",
		"-flush_packets", "1",
		"-fflags", "nobuffer+genpts+flush_packets",
		"-muxdelay", "0",
		"-muxpreload", "0",
		"-mpegts_flags", "resend_headers+initial_discontinuity",
		"-max_delay", "0",
		"-f", "mpegts", // Use MPEG-TS for SRT
		"-loglevel", "error",
		srtURL,
	}

	process := exec.Command("ffmpeg", args...)

	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	if err := process.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to start FFmpeg ultra-low latency SRT stream: %w", err)
	}

	// Ultra-low latency timing
	targetFPS := 15.0
	frameInterval := time.Duration(float64(time.Second) / targetFPS)

	stream := &MediaMTXStream{
		cameraID:      cameraID,
		process:       process,
		stdin:         stdin,
		frameBuffer:   make(chan *models.ProcessedFrame, 1), // Single frame buffer
		isRunning:     true,
		stopChannel:   make(chan struct{}),
		method:        PublishMethodSRT,
		targetFPS:     targetFPS,
		frameInterval: frameInterval,
		lastFrameTime: time.Now(),
	}

	// Start frame processing goroutine
	go s.processMediaMTXFrames(stream)

	// Get all stream URLs for this camera
	rtspURL := s.GetRTSPURL(cameraID)
	webrtcURL := s.GetWebRTCURL(cameraID)
	hlsURL := s.GetHLSURL(cameraID)

	log.Info().
		Str("camera_id", cameraID).
		Str("srt_publish_url", srtURL).
		Str("rtsp_url", rtspURL).
		Str("webrtc_view_url", webrtcURL).
		Str("hls_url", hlsURL).
		Float64("target_fps", targetFPS).
		Msg("Ultra-low latency SRT stream started - 640x480@15FPS optimized for WebRTC")

	return stream, nil
}

func (s *Service) createWebRTCStream(cameraID string, width, height int) (*MediaMTXStream, error) {
	// MediaMTX WebRTC WHIP publish URL
	whipURL := fmt.Sprintf("%s/live/%s/whip", s.cfg.MediaMTXURL, cameraID)

	// Create HTTP client for WHIP requests
	whipClient := &http.Client{Timeout: s.cfg.WHIPTimeout}

	// Create PeerConnection and VP8 track
	api := webrtc.NewAPI()
	var iceServers []webrtc.ICEServer
	for _, u := range s.cfg.WebRTCICEServers {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: []string{u}})
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, fmt.Errorf("failed to create PeerConnection: %w", err)
	}
	videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", cameraID)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create video track: %w", err)
	}
	if _, err := pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to add video track: %w", err)
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// WHIP exchange
	req, err := http.NewRequest(http.MethodPost, whipURL, strings.NewReader(pc.LocalDescription().SDP))
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create WHIP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("Accept", "application/sdp")
	resp, err := whipClient.Do(req)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed WHIP POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		pc.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WHIP POST failed: %d - %s", resp.StatusCode, string(body))
	}
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed reading WHIP answer: %w", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answer)}); err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to set remote description: %w", err)
	}
	whipResource := resp.Header.Get("Location")

	// FFmpeg: raw BGR24 -> VP8 (IVF) on stdout
	args := []string{
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-s", "640x480",
		"-r", "15",
		"-i", "-",
		"-c:v", "libvpx",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-error-resilient", "1",
		"-lag-in-frames", "0",
		"-g", "15",
		"-keyint_min", "15",
		"-b:v", "750k",
		"-maxrate", "1000k",
		"-bufsize", "375k",
		"-threads", "2",
		"-f", "ivf",
		"-loglevel", "error",
		"pipe:1",
	}
	process := exec.Command("ffmpeg", args...)
	stdin, err := process.StdinPipe()
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		stdin.Close()
		pc.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	if err := process.Start(); err != nil {
		stdin.Close()
		pc.Close()
		return nil, fmt.Errorf("failed to start FFmpeg WebRTC encoder: %w", err)
	}

	targetFPS := 15.0
	frameInterval := time.Duration(float64(time.Second) / targetFPS)

	stream := &MediaMTXStream{
		cameraID:      cameraID,
		process:       process,
		stdin:         stdin,
		stdout:        stdout,
		frameBuffer:   make(chan *models.ProcessedFrame, 1),
		isRunning:     true,
		stopChannel:   make(chan struct{}),
		method:        PublishMethodWebRTC,
		whipURL:       whipURL,
		whipClient:    whipClient,
		whipResource:  whipResource,
		pc:            pc,
		videoTrack:    videoTrack,
		targetFPS:     targetFPS,
		frameInterval: frameInterval,
		lastFrameTime: time.Now(),
	}

	// Start frame processing and IVF reader
	go s.processMediaMTXFrames(stream)
	go s.readIVFAndPublish(stream)

	// Get all stream URLs for this camera
	rtspURL := s.GetRTSPURL(cameraID)
	webrtcURL := s.GetWebRTCURL(cameraID)
	hlsURL := s.GetHLSURL(cameraID)

	log.Info().
		Str("camera_id", cameraID).
		Str("whip_publish_url", whipURL).
		Str("rtsp_url", rtspURL).
		Str("webrtc_view_url", webrtcURL).
		Str("hls_url", hlsURL).
		Float64("target_fps", targetFPS).
		Msg("Ultra-low latency WebRTC WHIP stream started - 640x480@15FPS via VP8/WHIP")

	return stream, nil
}

func (s *Service) processMediaMTXFrames(stream *MediaMTXStream) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", stream.cameraID).
				Msg("MediaMTX stream processor panic recovered")
		}

		// Clean up
		stream.isRunning = false
		if stream.stdin != nil {
			stream.stdin.Close()
		}
		if stream.process != nil {
			stream.process.Wait()
		}

		// Cleanup WebRTC/WHIP
		if stream.method == PublishMethodWebRTC {
			if stream.pc != nil {
				_ = stream.pc.Close()
			}
			if stream.whipResource != "" && stream.whipClient != nil {
				req, _ := http.NewRequest(http.MethodDelete, stream.whipResource, nil)
				_, _ = stream.whipClient.Do(req)
			}
		}

		// Remove from streams map
		s.streamMutex.Lock()
		delete(s.streams, stream.cameraID)
		s.streamMutex.Unlock()

		log.Info().
			Str("camera_id", stream.cameraID).
			Int64("total_frames", stream.frameCount).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("Ultra-low latency stream stopped")
	}()

	// Ultra-low latency frame timing
	ticker := time.NewTicker(stream.frameInterval)
	defer ticker.Stop()

	for stream.isRunning {
		select {
		case <-stream.stopChannel:
			return
		case <-ticker.C:
			// Get the latest frame only (drain buffer like Python)
			var latestFrame *models.ProcessedFrame
			framesDrained := 0

		drainLoop:
			for {
				select {
				case frame := <-stream.frameBuffer:
					if latestFrame != nil {
						framesDrained++
					}
					latestFrame = frame
				default:
					break drainLoop
				}
			}

			if framesDrained > 0 {
				stream.droppedFrames += int64(framesDrained)
			}

			if latestFrame == nil {
				continue
			}

			// Frame age check - only process recent frames
			frameAge := time.Since(latestFrame.Timestamp)
			if frameAge > 1*time.Second {
				log.Debug().
					Str("camera_id", stream.cameraID).
					Dur("frame_age", frameAge).
					Msg("Skipping old frame")
				continue
			}

			// Resize frame to 640x480 for optimized encoder quality
			resizedData, err := s.resizeFrameToStandard(latestFrame.Data, latestFrame.Width, latestFrame.Height)
			if err != nil {
				log.Error().
					Err(err).
					Str("camera_id", stream.cameraID).
					Msg("Failed to resize frame")
				continue
			}

			// Write frame data to FFmpeg stdin
			if _, err := stream.stdin.Write(resizedData); err != nil {
				log.Error().
					Err(err).
					Str("camera_id", stream.cameraID).
					Msg("Failed to write frame to ultra-low latency stream")
				return
			}

			stream.frameCount++

			// Log stats occasionally
			if stream.frameCount%20 == 0 {
				elapsed := time.Since(stream.lastFrameTime)
				actualFPS := 0.0
				if elapsed.Seconds() > 0 {
					actualFPS = stream.targetFPS / elapsed.Seconds()
				}
				stream.lastFrameTime = time.Now()

				log.Info().
					Str("camera_id", stream.cameraID).
					Int64("frame_count", stream.frameCount).
					Int64("dropped_frames", stream.droppedFrames).
					Float64("actual_fps", actualFPS).
					Float64("target_fps", stream.targetFPS).
					Msg("Ultra-low latency SRT streaming stats")
			}
		}
	}
}

// readIVFAndPublish reads IVF frames from ffmpeg stdout and publishes them to the WebRTC track
func (s *Service) readIVFAndPublish(stream *MediaMTXStream) {
	if stream.method != PublishMethodWebRTC || stream.stdout == nil || stream.videoTrack == nil {
		return
	}

	reader := bufio.NewReader(stream.stdout)

	// IVF header is 32 bytes, first 4 bytes should be 'DKIF'
	header := make([]byte, 32)
	if _, err := io.ReadFull(reader, header); err != nil {
		log.Error().Err(err).Str("camera_id", stream.cameraID).Msg("Failed to read IVF header")
		return
	}
	if string(header[:4]) != "DKIF" {
		log.Error().Str("camera_id", stream.cameraID).Msg("Invalid IVF header")
		return
	}

	for stream.isRunning {
		// IVF frame header: 4 bytes size (LE) + 8 bytes timestamp (LE)
		fh := make([]byte, 12)
		if _, err := io.ReadFull(reader, fh); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			log.Error().Err(err).Str("camera_id", stream.cameraID).Msg("Failed to read IVF frame header")
			break
		}
		frameSize := binary.LittleEndian.Uint32(fh[0:4])
		if frameSize == 0 || frameSize > 10*1024*1024 {
			log.Warn().Uint32("frame_size", frameSize).Str("camera_id", stream.cameraID).Msg("Skipping abnormal IVF frame size")
			continue
		}

		frame := make([]byte, int(frameSize))
		if _, err := io.ReadFull(reader, frame); err != nil {
			log.Error().Err(err).Str("camera_id", stream.cameraID).Msg("Failed to read IVF frame payload")
			break
		}

		if err := stream.videoTrack.WriteSample(media.Sample{Data: frame, Duration: stream.frameInterval}); err != nil {
			log.Error().Err(err).Str("camera_id", stream.cameraID).Msg("Failed to write sample to WebRTC track")
			break
		}
	}
}

// resizeFrameToStandard resizes any frame to 640x480 BGR format
func (s *Service) resizeFrameToStandard(data []byte, width, height int) ([]byte, error) {
	if len(data) != width*height*3 {
		return nil, fmt.Errorf("invalid frame data size: expected %d, got %d", width*height*3, len(data))
	}

	// Target size: 640x480 (optimized for WebRTC)
	targetWidth := 640
	targetHeight := 480

	// If already correct size, return as-is
	if width == targetWidth && height == targetHeight {
		return data, nil
	}

	// Simple byte-level downsampling (basic but fast)
	// This is a quick implementation - could be optimized further
	resizedData := make([]byte, targetWidth*targetHeight*3)

	scaleX := float64(width) / float64(targetWidth)
	scaleY := float64(height) / float64(targetHeight)

	for y := 0; y < targetHeight; y++ {
		for x := 0; x < targetWidth; x++ {
			srcX := int(float64(x) * scaleX)
			srcY := int(float64(y) * scaleY)

			if srcX >= width {
				srcX = width - 1
			}
			if srcY >= height {
				srcY = height - 1
			}

			srcIdx := (srcY*width + srcX) * 3
			dstIdx := (y*targetWidth + x) * 3

			if srcIdx+2 < len(data) && dstIdx+2 < len(resizedData) {
				resizedData[dstIdx] = data[srcIdx]     // B
				resizedData[dstIdx+1] = data[srcIdx+1] // G
				resizedData[dstIdx+2] = data[srcIdx+2] // R
			}
		}
	}

	return resizedData, nil
}

// StopStream stops the MediaMTX stream for a specific camera
func (s *Service) StopStream(cameraID string) error {
	s.streamMutex.Lock()
	defer s.streamMutex.Unlock()

	stream, exists := s.streams[cameraID]
	if !exists {
		return nil // Already stopped
	}

	stream.isRunning = false
	close(stream.stopChannel)
	close(stream.frameBuffer)

	if stream.stdin != nil {
		stream.stdin.Close()
	}

	if stream.process != nil {
		stream.process.Wait()
	}

	if stream.stdout != nil {
		stream.stdout.Close()
	}

	// Cleanup WebRTC/WHIP
	if stream.method == PublishMethodWebRTC {
		if stream.pc != nil {
			_ = stream.pc.Close()
		}
		if stream.whipResource != "" && stream.whipClient != nil {
			req, _ := http.NewRequest(http.MethodDelete, stream.whipResource, nil)
			_, _ = stream.whipClient.Do(req)
		}
	}

	delete(s.streams, cameraID)

	log.Info().
		Str("camera_id", cameraID).
		Int64("total_frames", stream.frameCount).
		Int64("dropped_frames", stream.droppedFrames).
		Msg("Ultra-low latency stream stopped")
	return nil
}

// Shutdown gracefully shuts down all streams
func (s *Service) Shutdown(ctx context.Context) error {
	log.Info().Msg("Publisher service shutting down")

	s.streamMutex.Lock()
	defer s.streamMutex.Unlock()

	// Stop all streams
	for cameraID, stream := range s.streams {
		stream.isRunning = false
		close(stream.stopChannel)
		close(stream.frameBuffer)

		if stream.stdin != nil {
			stream.stdin.Close()
		}

		if stream.process != nil {
			stream.process.Wait()
		}
		if stream.stdout != nil {
			stream.stdout.Close()
		}

		log.Info().
			Str("camera_id", cameraID).
			Int64("total_frames", stream.frameCount).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("Ultra-low latency stream stopped during shutdown")
	}

	// Clear streams map
	s.streams = make(map[string]*MediaMTXStream)

	return nil
}

// GetStreamURL returns the MediaMTX stream URL for a camera
func (s *Service) GetStreamURL(cameraID string) string {
	return fmt.Sprintf("%s/live/%s", s.cfg.MediaMTXURL, cameraID)
}

// GetWebRTCURL returns the MediaMTX WebRTC WHEP URL for a camera (for viewing)
func (s *Service) GetWebRTCURL(cameraID string) string {
	return fmt.Sprintf("%s/live/%s/whep", s.cfg.MediaMTXURL, cameraID)
}

// GetWebRTCPublishURL returns the MediaMTX WebRTC WHIP URL for a camera (for publishing)
func (s *Service) GetWebRTCPublishURL(cameraID string) string {
	return fmt.Sprintf("%s/live/%s/whip", s.cfg.MediaMTXURL, cameraID)
}

// GetHLSURL returns the MediaMTX HLS URL for a camera
func (s *Service) GetHLSURL(cameraID string) string {
	return fmt.Sprintf("%s/live/%s/hls", s.cfg.MediaMTXURL, cameraID)
}

// NewWebRTCService creates a publisher service specifically for WebRTC WHIP
func NewWebRTCService(cfg *config.Config) (*Service, error) {
	return NewServiceWithMethod(cfg, PublishMethodWebRTC)
}

// GetRTSPURL returns the MediaMTX RTSP URL for a camera
func (s *Service) GetRTSPURL(cameraID string) string {
	return fmt.Sprintf("rtsp://localhost:8554/live/%s", cameraID)
}
