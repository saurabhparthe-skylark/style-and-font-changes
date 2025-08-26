package mjpeg

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"image"
	"image/color"
	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
)

type Publisher struct {
	cfg         *config.Config
	jpegMutex   sync.RWMutex
	latestJPEG  map[string][]byte
	frameNotify map[string]chan struct{}
	notifyMutex sync.RWMutex
}

func NewPublisher(cfg *config.Config) (*Publisher, error) {
	p := &Publisher{
		cfg:         cfg,
		latestJPEG:  make(map[string][]byte),
		frameNotify: make(map[string]chan struct{}),
	}

	return p, nil
}

func (p *Publisher) PublishFrame(frame *models.ProcessedFrame) error {
	if err := p.updateLatestJPEG(frame); err != nil {
		return nil
	}

	p.notifyStreamers(frame.CameraID)
	return nil
}

func (p *Publisher) updateLatestJPEG(frame *models.ProcessedFrame) error {
	mat, err := gocv.NewMatFromBytes(frame.Height, frame.Width, gocv.MatTypeCV8UC3, frame.Data)
	if err != nil {
		return fmt.Errorf("failed to create Mat from frame data: %w", err)
	}
	defer mat.Close()

	buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, 90})
	if err != nil {
		return fmt.Errorf("failed to encode JPEG: %w", err)
	}

	b := buf.GetBytes()
	jpegCopy := make([]byte, len(b))
	copy(jpegCopy, b)
	buf.Close()

	p.jpegMutex.Lock()
	p.latestJPEG[frame.CameraID] = jpegCopy
	p.jpegMutex.Unlock()
	return nil
}

func (p *Publisher) notifyStreamers(cameraID string) {
	p.notifyMutex.RLock()
	notify, exists := p.frameNotify[cameraID]
	p.notifyMutex.RUnlock()

	if exists {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
}

func (p *Publisher) getOrCreateNotifyChannel(cameraID string) chan struct{} {
	p.notifyMutex.Lock()
	defer p.notifyMutex.Unlock()

	notify, exists := p.frameNotify[cameraID]
	if !exists {
		notify = make(chan struct{}, 5)
		p.frameNotify[cameraID] = notify
	}
	return notify
}

func (p *Publisher) cleanupNotifyChannel(cameraID string) {
	p.notifyMutex.Lock()
	defer p.notifyMutex.Unlock()

	if notify, exists := p.frameNotify[cameraID]; exists {
		close(notify)
		delete(p.frameNotify, cameraID)
	}
}

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

	notify := p.getOrCreateNotifyChannel(cameraID)
	defer p.cleanupNotifyChannel(cameraID)

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

	p.jpegMutex.RLock()
	first, ok := p.latestJPEG[cameraID]
	p.jpegMutex.RUnlock()
	if !ok || len(first) == 0 {
		placeholder := gocv.NewMatWithSize(360, 640, gocv.MatTypeCV8UC3)
		defer placeholder.Close()

		placeholder.SetTo(gocv.Scalar{Val1: 64, Val2: 64, Val3: 64, Val4: 0})

		textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
		gocv.PutText(&placeholder, fmt.Sprintf("Camera: %s", cameraID),
			image.Pt(20, 180), gocv.FontHersheySimplex, 1.0, textColor, 2)
		gocv.PutText(&placeholder, "Initializing...",
			image.Pt(20, 220), gocv.FontHersheySimplex, 0.8, textColor, 2)

		buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, placeholder, []int{gocv.IMWriteJpegQuality, 90})
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

	keepaliveTicker := time.NewTicker(2 * time.Second)
	defer keepaliveTicker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-notify:
			p.jpegMutex.RLock()
			buf, ok := p.latestJPEG[cameraID]
			p.jpegMutex.RUnlock()
			if ok && len(buf) > 0 {
				if !writePart(buf) {
					return
				}
			}
		case <-keepaliveTicker.C:
			p.jpegMutex.RLock()
			buf, ok := p.latestJPEG[cameraID]
			p.jpegMutex.RUnlock()
			if ok && len(buf) > 0 {
				if !writePart(buf) {
					return
				}
			}
		}
	}
}

func (p *Publisher) Shutdown() {
	log.Info().Msg("MJPEG Publisher shutting down")
}
