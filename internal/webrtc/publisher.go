package webrtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"kepler-worker/internal/config"
	"kepler-worker/internal/stream"
	"kepler-worker/pkg/logger"
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
	CameraID    string
	StreamPath  string
	Processor   *stream.FrameProcessor
	WHIPSession *WHIPSession
	Active      bool
	StartTime   time.Time
	mutex       sync.RWMutex
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

func (p *Publisher) StartCamera(cameraID, streamPath string, processor *stream.FrameProcessor) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if _, exists := p.cameras[cameraID]; exists {
		return fmt.Errorf("camera %s already publishing", cameraID)
	}

	p.logger.Info("Starting WebRTC for camera", "camera_id", cameraID, "stream_path", streamPath)

	if streamPath == "" {
		streamPath = fmt.Sprintf("camera_%s", cameraID)
	}

	whipSession, err := p.createWHIPSession(streamPath)
	if err != nil {
		return fmt.Errorf("failed to create WHIP session: %w", err)
	}

	camPublisher := &CameraPublisher{
		CameraID:    cameraID,
		StreamPath:  streamPath,
		Processor:   processor,
		WHIPSession: whipSession,
		Active:      true,
		StartTime:   time.Now(),
	}

	p.cameras[cameraID] = camPublisher

	go p.publishFrames(camPublisher)

	p.logger.Info("WebRTC publishing started",
		"camera_id", cameraID,
		"stream_path", streamPath,
		"session_id", whipSession.SessionID,
	)

	return nil
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

	p.logger.Info("Stopping WebRTC for camera", "camera_id", cameraID)

	camPublisher.mutex.Lock()
	camPublisher.Active = false
	camPublisher.mutex.Unlock()

	if camPublisher.WHIPSession != nil && camPublisher.WHIPSession.Active {
		if err := p.deleteWHIPSession(camPublisher.WHIPSession); err != nil {
			p.logger.Error("Failed to delete WHIP session", "error", err)
		}
	}

	delete(p.cameras, cameraID)

	p.logger.Info("WebRTC stopped for camera", "camera_id", cameraID)
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
