package camera

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
)

// Publisher handles MJPEG publishing of frames
type Publisher struct {
	cfg *config.Config

	// MJPEG latest-frame buffer
	jpegMutex  sync.RWMutex
	latestJPEG map[string][]byte
}

// NewPublisher creates a new publisher
func NewPublisher(cfg *config.Config) (*Publisher, error) {
	p := &Publisher{
		cfg:        cfg,
		latestJPEG: make(map[string][]byte),
	}

	log.Info().Msg("Publisher initialized (MJPEG only)")
	return p, nil
}

// PublishFrame ingests a processed frame; updates MJPEG buffer
func (p *Publisher) PublishFrame(frame *ProcessedFrame) error {
	log.Debug().
		Str("camera_id", frame.CameraID).
		Int64("frame_id", frame.FrameID).
		Int("frame_size", len(frame.Data)).
		Float64("fps", frame.FPS).
		Dur("latency", frame.Latency).
		Bool("ai_enabled", frame.AIEnabled).
		Msg("Publishing frame")

	// Always keep a fresh JPEG for MJPEG streaming
	if err := p.updateLatestJPEG(frame); err != nil {
		log.Warn().Err(err).Str("camera_id", frame.CameraID).Msg("Failed to encode JPEG for MJPEG")
	}
	return nil
}

// updateLatestJPEG encodes frame to JPEG and stores it for MJPEG
func (p *Publisher) updateLatestJPEG(frame *ProcessedFrame) error {
	mat, err := gocv.NewMatFromBytes(frame.Height, frame.Width, gocv.MatTypeCV8UC3, frame.Data)
	if err != nil {
		return fmt.Errorf("failed to create Mat from frame data: %w", err)
	}
	defer mat.Close()

	buf, err := gocv.IMEncode(gocv.JPEGFileExt, mat)
	if err != nil {
		return fmt.Errorf("failed to encode JPEG: %w", err)
	}
	// IMPORTANT: deep copy bytes before closing the buffer to avoid reuse/corruption
	b := buf.GetBytes()
	jpegCopy := make([]byte, len(b))
	copy(jpegCopy, b)
	buf.Close()

	p.jpegMutex.Lock()
	p.latestJPEG[frame.CameraID] = jpegCopy
	p.jpegMutex.Unlock()
	return nil
}

// StreamMJPEGHTTP streams MJPEG over HTTP for a camera ID
func (p *Publisher) StreamMJPEGHTTP(w http.ResponseWriter, r *http.Request, cameraID string) {
	boundary := "frame"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	writePart := func(jpeg []byte) bool {
		if _, err := io.WriteString(w, "--"+boundary+"\r\n"); err != nil {
			return false
		}
		if _, err := io.WriteString(w, "Content-Type: image/jpeg\r\n"); err != nil {
			return false
		}
		if _, err := io.WriteString(w, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(jpeg))); err != nil {
			return false
		}
		if _, err := w.Write(jpeg); err != nil {
			return false
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Send first frame immediately (or placeholder) so client renders instantly
	p.jpegMutex.RLock()
	first, ok := p.latestJPEG[cameraID]
	p.jpegMutex.RUnlock()
	if !ok || len(first) == 0 {
		// Create a simple gray placeholder JPEG 640x360
		placeholder := gocv.NewMatWithSize(360, 640, gocv.MatTypeCV8UC3)
		defer placeholder.Close()
		placeholder.SetTo(gocv.Scalar{Val1: 128, Val2: 128, Val3: 128, Val4: 0})
		buf, err := gocv.IMEncode(gocv.JPEGFileExt, placeholder)
		if err == nil {
			first = buf.GetBytes()
			buf.Close()
		}
	}
	if len(first) > 0 {
		if !writePart(first) {
			return
		}
	}

	// Periodic frame push (can be tuned)
	ticker := time.NewTicker(time.Second / 15)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.jpegMutex.RLock()
			buf, ok := p.latestJPEG[cameraID]
			p.jpegMutex.RUnlock()
			if !ok || len(buf) == 0 {
				continue
			}
			if !writePart(buf) {
				return
			}
		}
	}
}

// Shutdown shuts down the publisher
func (p *Publisher) Shutdown() {
	log.Info().Msg("Publisher shutting down (MJPEG only)")
}
