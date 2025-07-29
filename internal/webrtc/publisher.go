package webrtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"sync"
	"time"

	"kepler-worker/internal/config"
	"kepler-worker/internal/stream"
	"kepler-worker/pkg/logger"

	"github.com/gen2brain/x264-go"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type Publisher struct {
	config     *config.Config
	logger     *logger.Logger
	active     bool
	cameras    map[string]*CameraPublisher
	mutex      sync.RWMutex
	httpClient *http.Client
}

type CameraPublisher struct {
	CameraID       string
	StreamPath     string
	Processor      *stream.FrameProcessor
	PeerConnection *webrtc.PeerConnection
	VideoTrack     *webrtc.TrackLocalStaticSample
	WHIPSession    *WHIPSession
	Active         bool
	StartTime      time.Time
	encoder        *x264.Encoder
	encoderBuf     *bytes.Buffer
	ctx            context.Context
	cancel         context.CancelFunc
	mutex          sync.RWMutex
}

type WHIPSession struct {
	SessionURL string
	SessionID  string
	Active     bool
}

type WHIPRequest struct {
	SDP string `json:"sdp"`
}

type WHIPResponse struct {
	SDP       string `json:"sdp"`
	SessionID string `json:"session_id"`
}

func NewPublisher(cfg *config.Config, logger *logger.Logger) (*Publisher, error) {
	return &Publisher{
		config:  cfg,
		logger:  logger,
		active:  false,
		cameras: make(map[string]*CameraPublisher),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (p *Publisher) Start(ctx context.Context) error {
	p.logger.Info("Starting WebRTC publisher")
	p.active = true
	return nil
}

func (p *Publisher) Stop() {
	p.logger.Info("Stopping WebRTC publisher")

	p.mutex.Lock()
	defer p.mutex.Unlock()

	for cameraID := range p.cameras {
		p.stopCameraInternal(cameraID)
	}

	p.active = false
}

func (p *Publisher) StartCamera(cameraID, streamPath string, processor *stream.FrameProcessor, width, height int) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if _, exists := p.cameras[cameraID]; exists {
		return fmt.Errorf("camera %s already publishing", cameraID)
	}

	p.logger.Info("Starting WebRTC for camera", "camera_id", cameraID, "stream_path", streamPath)

	if streamPath == "" {
		streamPath = fmt.Sprintf("camera_%s", cameraID)
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "pion")
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to add track: %w", err)
	}

	rtpSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to add track: %w", err)
	}

	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	opts := &x264.Options{
		Width:     width,
		Height:    height,
		FrameRate: int(p.config.WebRTC.TargetFPS),
		Tune:      "zerolatency",
		Profile:   "baseline",
		LogLevel:  x264.LogInfo,
	}

	buf := new(bytes.Buffer)

	encoder, err := x264.NewEncoder(buf, opts)
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to create x264 encoder: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	camPublisher := &CameraPublisher{
		CameraID:       cameraID,
		StreamPath:     streamPath,
		Processor:      processor,
		PeerConnection: pc,
		VideoTrack:     videoTrack,
		Active:         true,
		StartTime:      time.Now(),
		encoder:        encoder,
		encoderBuf:     buf,
		ctx:            ctx,
		cancel:         cancel,
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to set local description: %w", err)
	}

	whipURL := fmt.Sprintf("%s/%s/whip", p.config.MediaMTX.WHIPEndpoint, streamPath)

	req, err := http.NewRequest("POST", whipURL, bytes.NewReader([]byte(offer.SDP)))
	if err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to create WHIP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("Accept", "application/sdp")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to send WHIP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		pc.Close()
		cancel()
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("WHIP request failed with status %d: %s", resp.StatusCode, string(body))
	}

	answerSDP, err := io.ReadAll(resp.Body)
	if err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to read WHIP response: %w", err)
	}

	answer := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answerSDP)}
	if err := pc.SetRemoteDescription(answer); err != nil {
		pc.Close()
		cancel()
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	p.cameras[cameraID] = camPublisher

	go p.streamFrames(camPublisher)

	p.logger.Info("WebRTC WHIP publishing started",
		"camera_id", cameraID,
		"stream_path", streamPath,
		"whip_url", whipURL,
	)

	return nil
}

func (p *Publisher) streamFrames(camPublisher *CameraPublisher) {
	ticker := time.NewTicker(time.Second / time.Duration(p.config.WebRTC.TargetFPS))
	defer ticker.Stop()

	frameCount := 0

	for {
		select {
		case <-camPublisher.ctx.Done():
			return
		case <-ticker.C:
			frame, _ := camPublisher.Processor.GetStreamingFrame()
			if frame == nil {
				continue
			}

			frameCount++

			h264Frame, err := p.encodeFrame(camPublisher, frame)
			if err != nil {
				p.logger.Error("Failed to encode frame to H264", "camera_id", camPublisher.CameraID, "error", err)
				continue
			}

			if len(h264Frame) == 0 {
				continue // No output from encoder
			}

			timestamp := time.Now()
			sample := media.Sample{
				Data:      h264Frame,
				Timestamp: timestamp,
			}

			if err := camPublisher.VideoTrack.WriteSample(sample); err != nil {
				p.logger.Error("Failed to write sample", "camera_id", camPublisher.CameraID, "error", err)
				continue
			}

			if frameCount%150 == 0 {
				status := "live"
				p.logger.Info("Frame sent",
					"camera_id", camPublisher.CameraID,
					"frame_count", frameCount,
					"status", status,
				)
			}
		}
	}
}

func (p *Publisher) encodeFrame(camPublisher *CameraPublisher, frame *stream.Frame) ([]byte, error) {
	if frame == nil || len(frame.Data) == 0 {
		return nil, fmt.Errorf("empty frame data")
	}

	// Calculate correct plane sizes for YUV420p
	ySize := frame.Width * frame.Height
	uvSize := (frame.Width / 2) * (frame.Height / 2)

	// Ensure we have enough data
	expectedSize := ySize + 2*uvSize
	if len(frame.Data) < expectedSize {
		p.logger.Error("Frame data size mismatch",
			"camera_id", camPublisher.CameraID,
			"got", len(frame.Data),
			"expected", expectedSize,
			"width", frame.Width,
			"height", frame.Height,
			"y_size", ySize,
			"uv_size", uvSize)
		return nil, fmt.Errorf("insufficient frame data: got %d, expected %d", len(frame.Data), expectedSize)
	}

	img := &image.YCbCr{
		Y:              frame.Data[:ySize],
		Cb:             frame.Data[ySize : ySize+uvSize],
		Cr:             frame.Data[ySize+uvSize : ySize+2*uvSize],
		YStride:        frame.Width,
		CStride:        frame.Width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, frame.Width, frame.Height),
	}

	camPublisher.encoderBuf.Reset()
	err := camPublisher.encoder.Encode(img)
	if err != nil {
		return nil, fmt.Errorf("x264 encoding failed: %w", err)
	}

	// Don't flush after every frame - only return the current buffer contents
	return camPublisher.encoderBuf.Bytes(), nil
}

func (p *Publisher) createWHIPSession(streamPath string) (*WHIPSession, error) {
	whipURL := fmt.Sprintf("%s/%s/whip", p.config.MediaMTX.WHIPEndpoint, streamPath)

	sdpOffer := p.generateSDPOffer()

	whipRequest := WHIPRequest{
		SDP: sdpOffer,
	}

	reqBody, err := json.Marshal(whipRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal WHIP request: %w", err)
	}

	req, err := http.NewRequest("POST", whipURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create WHIP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WHIP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WHIP request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var whipResponse WHIPResponse
	if err := json.NewDecoder(resp.Body).Decode(&whipResponse); err != nil {
		return nil, fmt.Errorf("failed to decode WHIP response: %w", err)
	}

	sessionID := whipResponse.SessionID
	if sessionID == "" {
		sessionID = extractSessionIDFromLocation(resp.Header.Get("Location"))
	}

	return &WHIPSession{
		SessionURL: whipURL,
		SessionID:  sessionID,
		Active:     true,
	}, nil
}

func (p *Publisher) generateSDPOffer() string {
	return fmt.Sprintf(`v=0
o=- %d 2 IN IP4 127.0.0.1
s=-
t=0 0
a=group:BUNDLE 0
a=msid-semantic:WMS
m=video 9 UDP/TLS/RTP/SAVPF 96
c=IN IP4 0.0.0.0
a=rtcp:9 IN IP4 0.0.0.0
a=ice-ufrag:random
a=ice-pwd:randompassword
a=ice-options:trickle
a=fingerprint:sha-256 00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00
a=setup:actpass
a=mid:0
a=sendonly
a=rtcp-mux
a=rtcp-rsize
a=rtpmap:96 H264/90000
a=ssrc:123456789 msid:user123456789@host-12345678 webrtc123456789
a=ssrc:123456789 mslabel:user123456789@host-12345678
a=ssrc:123456789 label:webrtc123456789
`, time.Now().Unix())
}

func extractSessionIDFromLocation(location string) string {
	if location == "" {
		return ""
	}

	parts := bytes.Split([]byte(location), []byte("/"))
	if len(parts) > 0 {
		return string(parts[len(parts)-1])
	}

	return ""
}

func (p *Publisher) publishFrames(camPublisher *CameraPublisher) {
	defer func() {
		p.logger.Info("Frame publishing stopped", "camera_id", camPublisher.CameraID)
	}()

	ticker := time.NewTicker(time.Duration(1000/p.config.WebRTC.TargetFPS) * time.Millisecond)
	defer ticker.Stop()

	frameCount := 0

	for camPublisher.Active {
		select {
		case <-ticker.C:
			frame, detections, timestamp := camPublisher.Processor.GetLatestFrame()
			if frame == nil {
				continue
			}

			if err := p.sendFrameToMediaMTX(camPublisher, frame, detections, timestamp); err != nil {
				p.logger.Error("Failed to send frame to MediaMTX",
					"camera_id", camPublisher.CameraID,
					"error", err,
				)
			}

			frameCount++

			if frameCount%300 == 0 {
				p.logger.Debug("Publishing frames",
					"camera_id", camPublisher.CameraID,
					"frame_count", frameCount,
				)
			}
		}
	}
}

func (p *Publisher) sendFrameToMediaMTX(camPublisher *CameraPublisher, frame *stream.Frame, detections []*stream.Detection, timestamp time.Time) error {
	if frame == nil || len(frame.Data) == 0 {
		return fmt.Errorf("invalid frame data")
	}

	frameData := map[string]interface{}{
		"camera_id":  camPublisher.CameraID,
		"timestamp":  timestamp.Unix(),
		"width":      frame.Width,
		"height":     frame.Height,
		"data_size":  len(frame.Data),
		"detections": len(detections),
		"session_id": camPublisher.WHIPSession.SessionID,
	}

	p.logger.Debug("Frame sent to MediaMTX", frameData)

	return nil
}

func (p *Publisher) StopCamera(cameraID string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.stopCameraInternal(cameraID)
}

func (p *Publisher) stopCameraInternal(cameraID string) error {
	camPublisher, exists := p.cameras[cameraID]
	if !exists {
		return fmt.Errorf("camera %s not found", cameraID)
	}

	p.logger.Info("Stopping WebRTC WHIP publishing for camera", "camera_id", cameraID)

	camPublisher.mutex.Lock()
	camPublisher.Active = false
	camPublisher.mutex.Unlock()

	camPublisher.cancel()

	if camPublisher.encoder != nil {
		// Flush any remaining frames before closing
		camPublisher.encoder.Flush()
		camPublisher.encoder.Close()
	}

	if camPublisher.PeerConnection != nil {
		if err := camPublisher.PeerConnection.Close(); err != nil {
			p.logger.Error("Failed to close peer connection", "error", err)
		}
	}

	if camPublisher.WHIPSession != nil {
		p.deleteWHIPSession(camPublisher.WHIPSession)
	}

	delete(p.cameras, cameraID)

	p.logger.Info("WebRTC WHIP publishing stopped for camera", "camera_id", cameraID)
	return nil
}

func (p *Publisher) deleteWHIPSession(session *WHIPSession) error {
	if session.SessionURL == "" {
		return nil
	}

	deleteURL := fmt.Sprintf("%s/%s", session.SessionURL, session.SessionID)

	req, err := http.NewRequest("DELETE", deleteURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create DELETE request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DELETE request failed with status %d", resp.StatusCode)
	}

	session.Active = false
	return nil
}

func (p *Publisher) GetStreamURL(cameraID, streamType string) string {
	p.mutex.RLock()
	camPublisher, exists := p.cameras[cameraID]
	p.mutex.RUnlock()

	if !exists {
		return ""
	}

	switch streamType {
	case "hls":
		return fmt.Sprintf("%s/%s/index.m3u8", p.config.MediaMTX.HLSEndpoint, camPublisher.StreamPath)
	case "webrtc":
		return fmt.Sprintf("%s/%s/whep", p.config.MediaMTX.WHIPEndpoint, camPublisher.StreamPath)
	case "rtsp":
		return fmt.Sprintf("%s/%s", p.config.MediaMTX.RTSPEndpoint, camPublisher.StreamPath)
	default:
		return ""
	}
}

func (p *Publisher) GetStats(cameraID string) map[string]interface{} {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if cameraID == "" {
		allStats := make(map[string]interface{})
		for id := range p.cameras {
			allStats[id] = p.getCameraStats(id)
		}
		return allStats
	}

	return p.getCameraStats(cameraID)
}

func (p *Publisher) getCameraStats(cameraID string) map[string]interface{} {
	camPublisher, exists := p.cameras[cameraID]
	if !exists {
		return map[string]interface{}{
			"error": "camera not found",
		}
	}

	camPublisher.mutex.RLock()
	defer camPublisher.mutex.RUnlock()

	uptime := time.Since(camPublisher.StartTime)

	stats := map[string]interface{}{
		"camera_id":   cameraID,
		"stream_path": camPublisher.StreamPath,
		"active":      camPublisher.Active,
		"uptime":      uptime.String(),
		"start_time":  camPublisher.StartTime,
	}

	if camPublisher.WHIPSession != nil {
		stats["whip_session"] = map[string]interface{}{
			"session_id": camPublisher.WHIPSession.SessionID,
			"active":     camPublisher.WHIPSession.Active,
		}
	}

	if camPublisher.Processor != nil {
		procStats := camPublisher.Processor.GetStats()
		stats["processing"] = map[string]interface{}{
			"frames_processed": procStats.FramesProcessed,
			"frames_sent_ai":   procStats.FramesSentToAI,
			"avg_process_time": procStats.AverageProcessTime.String(),
			"detection_count":  procStats.DetectionCount,
		}
	}

	return stats
}
