package webrtc

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

type Publisher struct {
	cfg *config.Config

	streamMutex sync.RWMutex
	streams     map[string]*Stream

	sourceMutex  sync.RWMutex
	cameraSource map[string]string
}

type Stream struct {
	cameraID     string
	videoProcess *exec.Cmd
	videoIn      io.WriteCloser
	videoOut     io.ReadCloser
	frameBuffer  chan *models.ProcessedFrame
	isRunning    bool
	stopCh       chan struct{}

	whipURL      string
	whipClient   *http.Client
	whipResource string
	pc           *webrtc.PeerConnection
	videoTrack   *webrtc.TrackLocalStaticSample

	// Audio
	audioProcess  *exec.Cmd
	audioOut      io.ReadCloser
	audioTrack    *webrtc.TrackLocalStaticSample
	audioStopChan chan struct{}
	audioRunning  bool

	targetFPS     float64
	frameInterval time.Duration
	lastFrameTime time.Time
	frameCount    int64
	droppedFrames int64
}

func NewPublisher(cfg *config.Config) (*Publisher, error) {
	publisher := &Publisher{
		cfg:          cfg,
		streams:      make(map[string]*Stream),
		cameraSource: make(map[string]string),
	}
	log.Info().
		Str("mediamtx_url", cfg.MediaMTXURL).
		Msg("WebRTC Publisher initialized with MediaMTX")
	return publisher, nil
}

func (p *Publisher) PublishFrame(frame *models.ProcessedFrame) error {
	stream, err := p.getOrCreateStream(frame.CameraID, frame.Width, frame.Height)
	if err != nil {
		return fmt.Errorf("failed to get MediaMTX stream for camera %s: %w", frame.CameraID, err)
	}

	select {
	case stream.frameBuffer <- frame:
	default:
		stream.droppedFrames++
		log.Debug().
			Str("camera_id", frame.CameraID).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("Dropped frame for MediaMTX (preventing overflow)")
	}

	return nil
}

func (p *Publisher) getOrCreateStream(cameraID string, width, height int) (*Stream, error) {
	p.streamMutex.Lock()
	defer p.streamMutex.Unlock()

	if stream, exists := p.streams[cameraID]; exists && stream.isRunning {
		return stream, nil
	}

	stream, err := p.createStream(cameraID, width, height)
	if err != nil {
		return nil, err
	}

	p.streams[cameraID] = stream
	return stream, nil
}

// RegisterCameraSource stores the RTSP URL for audio extraction
func (p *Publisher) RegisterCameraSource(cameraID, rtspURL string) {
	p.sourceMutex.Lock()
	p.cameraSource[cameraID] = rtspURL
	p.sourceMutex.Unlock()
}

func (p *Publisher) createStream(cameraID string, width, height int) (*Stream, error) {
	whipURL := fmt.Sprintf("%s/live/%s/whip", p.cfg.MediaMTXURL, cameraID)

	// best-effort cleanup of any lingering WebRTC sessions or publishers on this path
	if err := p.cleanupMediaMTXSessions(cameraID); err != nil {
		log.Warn().Err(err).Str("camera_id", cameraID).Msg("Pre-publish cleanup failed; proceeding anyway")
	}

	whipClient := &http.Client{Timeout: p.cfg.WHIPTimeout}

	api := webrtc.NewAPI()
	var iceServers []webrtc.ICEServer
	for _, u := range p.cfg.WebRTCICEServers {
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

	// Add audio track (PCMU G.711 at 8kHz)
	audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", cameraID)
	if err == nil {
		if _, err2 := pc.AddTrack(audioTrack); err2 != nil {
			log.Warn().Err(err2).Str("camera_id", cameraID).Msg("Failed to add audio track; continuing without audio")
			audioTrack = nil
		}
	} else {
		log.Warn().Err(err).Str("camera_id", cameraID).Msg("Failed to create audio track; continuing without audio")
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
		// if conflict, try cleanup once and retry
		if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusLocked || resp.StatusCode == http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			log.Warn().Str("camera_id", cameraID).Int("status", resp.StatusCode).Msg("WHIP conflict; attempting cleanup and retry")
			_ = p.cleanupMediaMTXSessions(cameraID)
			// retry once
			req2, _ := http.NewRequest(http.MethodPost, whipURL, strings.NewReader(pc.LocalDescription().SDP))
			req2.Header.Set("Content-Type", "application/sdp")
			req2.Header.Set("Accept", "application/sdp")
			resp2, err2 := whipClient.Do(req2)
			if err2 != nil {
				pc.Close()
				return nil, fmt.Errorf("failed WHIP POST after cleanup: %w", err2)
			}
			defer resp2.Body.Close()
			if resp2.StatusCode != http.StatusCreated {
				pc.Close()
				body2, _ := io.ReadAll(resp2.Body)
				return nil, fmt.Errorf("WHIP POST failed after cleanup: %d - %s (first: %d - %s)", resp2.StatusCode, string(body2), resp.StatusCode, string(body))
			}
			// replace resp with resp2 for the normal flow below
			resp = resp2
		} else {
			pc.Close()
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("WHIP POST failed: %d - %s", resp.StatusCode, string(body))
		}
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

	// Video encoder
	sizeArg := fmt.Sprintf("%dx%d", p.cfg.OutputWidth, p.cfg.OutputHeight)
	fps := p.cfg.PublishingFPS
	if fps <= 0 {
		fps = 15
	}
	bitrateArg := fmt.Sprintf("%dk", p.cfg.OutputBitrate)
	args := []string{
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-s", sizeArg,
		"-r", fmt.Sprintf("%d", fps),
		"-i", "-",
		"-c:v", "libvpx",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-lag-in-frames", "0",
		"-error-resilient", "1",
		"-g", "15",
		"-row-mt", "1",
		"-threads", "2",
		"-b:v", bitrateArg,
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

	targetFPS := float64(fps)
	frameInterval := time.Duration(float64(time.Second) / targetFPS)

	stream := &Stream{
		cameraID:      cameraID,
		videoProcess:  process,
		videoIn:       stdin,
		videoOut:      stdout,
		frameBuffer:   make(chan *models.ProcessedFrame, 1),
		isRunning:     true,
		stopCh:        make(chan struct{}),
		whipURL:       whipURL,
		whipClient:    whipClient,
		whipResource:  whipResource,
		pc:            pc,
		videoTrack:    videoTrack,
		targetFPS:     targetFPS,
		frameInterval: frameInterval,
		lastFrameTime: time.Now(),
	}

	go p.pumpVideoFramesToEncoder(stream)
	go p.pumpEncodedVideoToTrack(stream)

	// Start audio extraction via FFmpeg if we have a source URL and track
	if audioTrack != nil {
		p.sourceMutex.RLock()
		rtspURL := p.cameraSource[cameraID]
		p.sourceMutex.RUnlock()
		if rtspURL == "" {
			// fallback to configured path if present
			rtspURL = p.cfg.GetRTSPURL(cameraID)
		}
		if rtspURL != "" {
			stream.audioTrack = audioTrack
			stream.audioStopChan = make(chan struct{})
			if err := p.startAudioPipeline(stream, rtspURL); err != nil {
				log.Warn().Err(err).Str("camera_id", cameraID).Msg("Audio pipeline failed to start; continuing without audio")
			}
		}
	}

	rtspURL := p.cfg.GetRTSPURL(cameraID)
	webrtcURL := p.cfg.GetWebRTCURL(cameraID)
	hlsURL := p.cfg.GetHLSURL(cameraID)

	log.Info().
		Str("camera_id", cameraID).
		Str("whip_publish_url", whipURL).
		Str("rtsp_url", rtspURL).
		Str("webrtc_view_url", webrtcURL).
		Str("hls_url", hlsURL).
		Float64("target_fps", targetFPS).
		Msg("WebRTC WHIP stream started")

	return stream, nil
}

// startAudioPipeline runs ffmpeg to extract/encode G.711 PCMU at 8kHz and sends 20ms samples
func (p *Publisher) startAudioPipeline(stream *Stream, rtspURL string) error {
	// Transcode to 8kHz mono mulaw (PCMU) and pipe raw mulaw bytes
	args := []string{
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-vn",
		"-ac", "1",
		"-ar", "8000",
		"-c:a", "pcm_mulaw",
		"-f", "mulaw",
		"-loglevel", "error",
		"pipe:1",
	}
	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stream.audioProcess = cmd
	stream.audioOut = stdout
	stream.audioRunning = true

	go func() {
		defer func() {
			stream.audioRunning = false
			if stream.audioOut != nil {
				stream.audioOut.Close()
			}
			if stream.audioProcess != nil {
				stream.audioProcess.Wait()
			}
		}()

		reader := bufio.NewReader(stdout)
		// 20ms of 8kHz mono mulaw = 8000 * 1 byte * 0.02 = 160 bytes per chunk
		chunkBytes := 160
		buf := make([]byte, chunkBytes)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		for stream.isRunning && stream.audioRunning && stream.audioTrack != nil {
			select {
			case <-stream.audioStopChan:
				return
			case <-ticker.C:
				_, err := io.ReadFull(reader, buf)
				if err != nil {
					// On underrun, fill with mulaw silence (0xFF)
					for i := range buf {
						buf[i] = 0xFF
					}
				}
				_ = stream.audioTrack.WriteSample(media.Sample{Data: append([]byte(nil), buf...), Duration: 20 * time.Millisecond})
			}
		}
	}()
	return nil
}

func (p *Publisher) pumpVideoFramesToEncoder(stream *Stream) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("camera_id", stream.cameraID).
				Msg("MediaMTX stream processor panic recovered")
		}

		stream.isRunning = false
		if stream.videoIn != nil {
			stream.videoIn.Close()
		}
		if stream.videoProcess != nil {
			stream.videoProcess.Wait()
		}

		if stream.pc != nil {
			_ = stream.pc.Close()
		}
		if stream.whipResource != "" && stream.whipClient != nil {
			req, _ := http.NewRequest(http.MethodDelete, stream.whipResource, nil)
			_, _ = stream.whipClient.Do(req)
		}

		p.streamMutex.Lock()
		delete(p.streams, stream.cameraID)
		p.streamMutex.Unlock()

		log.Info().
			Str("camera_id", stream.cameraID).
			Int64("total_frames", stream.frameCount).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("WebRTC stream stopped")
	}()

	ticker := time.NewTicker(stream.frameInterval)
	defer ticker.Stop()

	for stream.isRunning {
		select {
		case <-stream.stopCh:
			return
		case <-ticker.C:
			var latestFrame *models.ProcessedFrame
			framesDrained := 0

			for {
				select {
				case frame := <-stream.frameBuffer:
					if latestFrame != nil {
						framesDrained++
					}
					latestFrame = frame
				default:
					goto drained
				}
			}
		drained:

			if framesDrained > 0 {
				stream.droppedFrames += int64(framesDrained)
			}

			if latestFrame == nil {
				continue
			}

			// Validate frame data thoroughly
			if len(latestFrame.Data) == 0 {
				log.Debug().
					Str("camera_id", stream.cameraID).
					Msg("Skipping frame with no data")
				continue
			}

			// Validate frame dimensions and data size
			expectedSize := latestFrame.Width * latestFrame.Height * 3
			if len(latestFrame.Data) != expectedSize {
				log.Debug().
					Str("camera_id", stream.cameraID).
					Int("expected_size", expectedSize).
					Int("actual_size", len(latestFrame.Data)).
					Int("width", latestFrame.Width).
					Int("height", latestFrame.Height).
					Msg("Skipping frame with invalid data size")
				continue
			}

			resizedData, err := p.resizeFrameToStandard(latestFrame.Data, latestFrame.Width, latestFrame.Height)
			if err != nil {
				log.Error().
					Err(err).
					Str("camera_id", stream.cameraID).
					Msg("Failed to resize frame")
				continue
			}

			if _, err := stream.videoIn.Write(resizedData); err != nil {
				log.Error().
					Err(err).
					Str("camera_id", stream.cameraID).
					Msg("Failed to write frame to WebRTC stream")
				return
			}

			stream.frameCount++
		}
	}
}

func (p *Publisher) pumpEncodedVideoToTrack(stream *Stream) {
	if stream.videoOut == nil || stream.videoTrack == nil {
		return
	}

	reader := bufio.NewReader(stream.videoOut)

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

func (p *Publisher) resizeFrameToStandard(data []byte, width, height int) ([]byte, error) {
	if len(data) != width*height*3 {
		return nil, fmt.Errorf("invalid frame data size: expected %d, got %d", width*height*3, len(data))
	}

	targetWidth := p.cfg.OutputWidth
	if targetWidth <= 0 {
		targetWidth = 640
	}
	targetHeight := p.cfg.OutputHeight
	if targetHeight <= 0 {
		targetHeight = 480
	}

	if width == targetWidth && height == targetHeight {
		return data, nil
	}

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
				resizedData[dstIdx] = data[srcIdx]
				resizedData[dstIdx+1] = data[srcIdx+1]
				resizedData[dstIdx+2] = data[srcIdx+2]
			}
		}
	}

	return resizedData, nil
}

func (p *Publisher) StopStream(cameraID string) error {
	p.streamMutex.Lock()
	defer p.streamMutex.Unlock()

	stream, exists := p.streams[cameraID]
	if !exists {
		return nil
	}

	stream.isRunning = false
	close(stream.stopCh)
	close(stream.frameBuffer)

	if stream.videoIn != nil {
		stream.videoIn.Close()
	}

	if stream.videoProcess != nil {
		stream.videoProcess.Wait()
	}

	if stream.videoOut != nil {
		stream.videoOut.Close()
	}

	// Audio cleanup
	if stream.audioStopChan != nil {
		close(stream.audioStopChan)
	}
	if stream.audioOut != nil {
		stream.audioOut.Close()
	}
	if stream.audioProcess != nil {
		stream.audioProcess.Wait()
	}

	if stream.pc != nil {
		_ = stream.pc.Close()
	}
	if stream.whipResource != "" && stream.whipClient != nil {
		req, _ := http.NewRequest(http.MethodDelete, stream.whipResource, nil)
		_, _ = stream.whipClient.Do(req)
	}

	delete(p.streams, cameraID)

	log.Info().
		Str("camera_id", cameraID).
		Int64("total_frames", stream.frameCount).
		Int64("dropped_frames", stream.droppedFrames).
		Msg("WebRTC stream stopped")
	return nil
}

func (p *Publisher) Shutdown(ctx context.Context) error {
	log.Info().Msg("WebRTC Publisher shutting down")

	p.streamMutex.Lock()
	defer p.streamMutex.Unlock()

	for cameraID, stream := range p.streams {
		stream.isRunning = false
		close(stream.stopCh)
		close(stream.frameBuffer)

		if stream.videoIn != nil {
			stream.videoIn.Close()
		}

		if stream.videoProcess != nil {
			stream.videoProcess.Wait()
		}
		if stream.videoOut != nil {
			stream.videoOut.Close()
		}

		// Audio cleanup
		if stream.audioStopChan != nil {
			close(stream.audioStopChan)
		}
		if stream.audioOut != nil {
			stream.audioOut.Close()
		}
		if stream.audioProcess != nil {
			stream.audioProcess.Wait()
		}

		log.Info().
			Str("camera_id", cameraID).
			Int64("total_frames", stream.frameCount).
			Int64("dropped_frames", stream.droppedFrames).
			Msg("WebRTC stream stopped during shutdown")
	}

	p.streams = make(map[string]*Stream)

	return nil
}

// for a given camera path to avoid WHIP/WHEP conflicts. It's best-effort and never fatal.
func (p *Publisher) cleanupMediaMTXSessions(cameraID string) error {
	apiBase := p.cfg.MediaMTXAPIURL
	if apiBase == "" {
		return nil
	}

	// Kick publisher on the path (best-effort)
	path := fmt.Sprintf("live/%s", cameraID)
	reqBody := strings.NewReader(fmt.Sprintf(`{"path":"%s"}`, path))
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v3/paths/kick/publisher", apiBase), reqBody)
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		_, _ = http.DefaultClient.Do(req)
	}

	// List WebRTC sessions and delete ones that reference our path
	listReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v3/webrtcsessions/list", apiBase), nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	s := string(body)
	idx := 0
	for {
		pos := strings.Index(s[idx:], path)
		if pos < 0 {
			break
		}
		pos += idx
		idKey := `"id":"`
		start := strings.LastIndex(s[:pos], idKey)
		if start >= 0 {
			start += len(idKey)
			end := strings.IndexByte(s[start:], '"')
			if end > 0 {
				sessionID := s[start : start+end]
				kickURL := fmt.Sprintf("%s/v3/webrtcsessions/kick/%s", apiBase, sessionID)
				kickReq, err2 := http.NewRequest(http.MethodPost, kickURL, nil)
				if err2 == nil {
					_, _ = http.DefaultClient.Do(kickReq)
				}
			}
		}
		idx = pos + len(path)
	}
	return nil
}
