package stream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"kepler-worker/internal/config"
	"kepler-worker/pkg/logger"
)

type Frame struct {
	Data      []byte
	Width     int
	Height    int
	Timestamp time.Time
}

type Handler struct {
	streamURL string
	config    *config.Config
	logger    *logger.Logger

	connected bool
	mutex     sync.RWMutex
	cmd       *exec.Cmd
	stdout    io.ReadCloser
	cancel    context.CancelFunc
	ctx       context.Context

	fps            float64
	width          int
	height         int
	frameSize      int
	reconnectCount int
	lastFrameTime  time.Time

	frameBuffer chan *Frame
	errorCount  int
	maxErrors   int
}

func NewHandler(streamURL string, cfg *config.Config, logger *logger.Logger) (*Handler, error) {
	ctx, cancel := context.WithCancel(context.Background())

	return &Handler{
		streamURL:   streamURL,
		config:      cfg,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		frameBuffer: make(chan *Frame, cfg.Stream.BufferSize),
		maxErrors:   10,
	}, nil
}

func (h *Handler) Connect(ctx context.Context) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.connected {
		return nil
	}

	h.logger.Info("Connecting to RTSP stream", "url", h.streamURL)

	for attempt := 0; attempt < h.config.Stream.MaxRetries; attempt++ {
		if err := h.startFFmpeg(); err != nil {
			h.logger.Error("Failed to start FFmpeg", "attempt", attempt+1, "error", err)
			if attempt < h.config.Stream.MaxRetries-1 {
				time.Sleep(h.config.Stream.RetryDelay)
				continue
			}
			return fmt.Errorf("failed to connect after %d attempts: %w", h.config.Stream.MaxRetries, err)
		}

		h.connected = true
		h.reconnectCount++
		h.logger.Info("RTSP stream connected",
			"attempt", attempt+1,
			"fps", h.fps,
			"resolution", fmt.Sprintf("%dx%d", h.width, h.height),
		)

		go h.readFrames()
		return nil
	}

	return fmt.Errorf("failed to connect to stream after %d attempts", h.config.Stream.MaxRetries)
}

func (h *Handler) startFFmpeg() error {
	args := []string{
		"-rtsp_transport", "tcp",
		"-i", h.streamURL,
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-an",
		"-sn",
		"-",
	}

	h.cmd = exec.CommandContext(h.ctx, "ffmpeg", args...)

	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	h.stdout = stdout

	stderr, err := h.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := h.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	if err := h.parseFFmpegInfo(stderr); err != nil {
		h.cmd.Process.Kill()
		return fmt.Errorf("failed to parse stream info: %w", err)
	}

	h.frameSize = h.width * h.height * 3
	return nil
}

func (h *Handler) parseFFmpegInfo(stderr io.ReadCloser) error {
	buffer := make([]byte, 4096)
	infoFound := false

	done := make(chan bool, 1)
	var parseErr error

	go func() {
		defer stderr.Close()
		var output bytes.Buffer

		for {
			n, err := stderr.Read(buffer)
			if err != nil {
				if err != io.EOF {
					parseErr = err
				}
				break
			}

			output.Write(buffer[:n])
			text := output.String()

			if strings.Contains(text, "Stream #0:0") && strings.Contains(text, "Video:") {
				if err := h.extractVideoInfo(text); err == nil {
					infoFound = true
					done <- true
					return
				}
			}

			if output.Len() > 8192 {
				output.Reset()
			}
		}
		done <- infoFound
	}()

	select {
	case success := <-done:
		if !success || parseErr != nil {
			return fmt.Errorf("failed to parse stream info: %v", parseErr)
		}
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timeout parsing stream info")
	}
}

func (h *Handler) extractVideoInfo(text string) error {
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		if strings.Contains(line, "Stream #0:0") && strings.Contains(line, "Video:") {
			if strings.Contains(line, "fps") {
				parts := strings.Split(line, ",")
				for _, part := range parts {
					part = strings.TrimSpace(part)

					if strings.Contains(part, " fps") {
						fpsStr := strings.Fields(part)[0]
						if fps, err := strconv.ParseFloat(fpsStr, 64); err == nil {
							h.fps = fps
						}
					}

					if strings.Contains(part, "x") && (strings.Contains(part, "1920x1080") ||
						strings.Contains(part, "1280x720") || strings.Contains(part, "640x480")) {
						dims := strings.Split(strings.TrimSpace(part), "x")
						if len(dims) == 2 {
							if w, err := strconv.Atoi(dims[0]); err == nil {
								h.width = w
							}
							if height, err := strconv.Atoi(dims[1]); err == nil {
								h.height = height
							}
						}
					}
				}
			}

			if h.fps > 0 && h.width > 0 && h.height > 0 {
				return nil
			}
		}
	}

	if h.fps == 0 {
		h.fps = 25.0
	}
	if h.width == 0 || h.height == 0 {
		h.width = 1280
		h.height = 720
	}

	return nil
}

func (h *Handler) readFrames() {
	defer func() {
		if h.stdout != nil {
			h.stdout.Close()
		}
		if h.cmd != nil && h.cmd.Process != nil {
			h.cmd.Process.Kill()
		}
	}()

	frameData := make([]byte, h.frameSize)

	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			if _, err := io.ReadFull(h.stdout, frameData); err != nil {
				h.handleReadError(err)
				continue
			}

			frame := &Frame{
				Data:      make([]byte, len(frameData)),
				Width:     h.width,
				Height:    h.height,
				Timestamp: time.Now(),
			}
			copy(frame.Data, frameData)

			select {
			case h.frameBuffer <- frame:
				h.lastFrameTime = time.Now()
				h.errorCount = 0
			case <-time.After(100 * time.Millisecond):
				h.logger.Debug("Frame buffer full, dropping frame")
			case <-h.ctx.Done():
				return
			}
		}
	}
}

func (h *Handler) handleReadError(err error) {
	h.errorCount++
	h.logger.Error("Frame read error", "error", err, "count", h.errorCount)

	if h.errorCount > h.maxErrors {
		h.logger.Error("Too many read errors, disconnecting")
		h.mutex.Lock()
		h.connected = false
		h.mutex.Unlock()
		return
	}

	time.Sleep(100 * time.Millisecond)
}

func (h *Handler) ReadFrame() (*Frame, error) {
	h.mutex.RLock()
	if !h.connected {
		h.mutex.RUnlock()
		return nil, fmt.Errorf("stream not connected")
	}
	h.mutex.RUnlock()

	select {
	case frame := <-h.frameBuffer:
		return frame, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("frame read timeout")
	case <-h.ctx.Done():
		return nil, fmt.Errorf("stream handler closed")
	}
}

func (h *Handler) Close() error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.connected = false

	if h.cancel != nil {
		h.cancel()
	}

	if h.cmd != nil && h.cmd.Process != nil {
		h.cmd.Process.Kill()
		h.cmd.Wait()
	}

	close(h.frameBuffer)
	for range h.frameBuffer {
	}

	h.logger.Info("Stream handler closed")
	return nil
}

func (h *Handler) GetStreamInfo() map[string]interface{} {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	return map[string]interface{}{
		"url":         h.streamURL,
		"connected":   h.connected,
		"fps":         h.fps,
		"width":       h.width,
		"height":      h.height,
		"reconnects":  h.reconnectCount,
		"last_frame":  h.lastFrameTime,
		"error_count": h.errorCount,
		"buffer_size": len(h.frameBuffer),
	}
}
