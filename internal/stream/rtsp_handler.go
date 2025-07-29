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

	"bufio"

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
	cameraID  string
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

func NewHandler(streamURL, cameraID string, cfg *config.Config, logger *logger.Logger) (*Handler, error) {
	ctx, cancel := context.WithCancel(context.Background())

	return &Handler{
		streamURL:   streamURL,
		cameraID:    cameraID,
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

	var destURL string
	if h.config.MediaMTX.RTSPEndpoint != "" {
		destURL = fmt.Sprintf("%s/camera_%s", h.config.MediaMTX.RTSPEndpoint, h.cameraID)
	} else {
		destURL = fmt.Sprintf("rtsp://localhost:8554/camera_%s", h.cameraID)
	}

	h.logger.Info("Starting FFmpeg transcoding", "source", h.streamURL, "dest", destURL)

	if err := h.startFFmpeg(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	h.connected = true
	h.reconnectCount++
	h.logger.Info("FFmpeg transcoding started", "camera_id", h.cameraID)
	return nil
}

func (h *Handler) startFFmpeg() error {
	// Use MediaMTX RTSP endpoint - check if we're connecting to dockerized MediaMTX
	var destURL string
	if h.config.MediaMTX.RTSPEndpoint != "" {
		// Use configured endpoint
		destURL = fmt.Sprintf("%s/camera_%s", h.config.MediaMTX.RTSPEndpoint, h.cameraID)
	} else {
		// Default to localhost
		destURL = fmt.Sprintf("rtsp://localhost:8554/camera_%s", h.cameraID)
	}

	args := []string{
		"-rtsp_transport", "tcp",
		"-i", h.streamURL,
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-b:v", "600k",
		"-maxrate", "600k",
		"-bufsize", "1200k",
		"-g", "50",
		"-c:a", "aac",
		"-b:a", "64k",
		"-f", "rtsp",
		destURL,
	}

	h.logger.Info("Starting FFmpeg with command", "args", strings.Join(args, " "))
	h.cmd = exec.CommandContext(h.ctx, "ffmpeg", args...)

	stderr, err := h.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := h.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	// Monitor FFmpeg output more carefully
	go func() {
		defer stderr.Close()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "error") || strings.Contains(line, "Error") || strings.Contains(line, "failed") {
				h.logger.Error("FFmpeg error", "output", line)
			} else if strings.Contains(line, "Stream #") || strings.Contains(line, "fps") {
				h.logger.Info("FFmpeg info", "output", line)
			} else {
				h.logger.Debug("FFmpeg debug", "output", line)
			}
		}
		if err := scanner.Err(); err != nil {
			h.logger.Error("Error reading FFmpeg stderr", "error", err)
		}
	}()

	// Monitor process
	go func() {
		err := h.cmd.Wait()
		if err != nil {
			h.logger.Error("FFmpeg process exited with error", "error", err)
		} else {
			h.logger.Info("FFmpeg process exited normally")
		}
	}()

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
			h.mutex.RLock()
			if !h.connected {
				h.mutex.RUnlock()
				return
			}
			h.mutex.RUnlock()

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
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		h.logger.Error("FFmpeg stream ended, disconnecting", "error", err)
		h.mutex.Lock()
		h.connected = false
		h.mutex.Unlock()
		return
	}

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
	// Not needed anymore - FFmpeg handles everything
	return nil, fmt.Errorf("frame reading not supported in transcoding mode")
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

	h.logger.Info("FFmpeg transcoding stopped")
	return nil
}

func (h *Handler) GetStreamInfo() map[string]interface{} {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	var destURL string
	if h.config.MediaMTX.RTSPEndpoint != "" {
		destURL = fmt.Sprintf("%s/camera_%s", h.config.MediaMTX.RTSPEndpoint, h.cameraID)
	} else {
		destURL = fmt.Sprintf("rtsp://localhost:8554/camera_%s", h.cameraID)
	}

	return map[string]interface{}{
		"source_url": h.streamURL,
		"dest_url":   destURL,
		"connected":  h.connected,
		"reconnects": h.reconnectCount,
		"mode":       "transcoding",
	}
}
