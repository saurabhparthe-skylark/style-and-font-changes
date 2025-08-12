package camera

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services/detection"
	pb "kepler-worker-go/proto"
)

// FrameProcessor handles AI processing and metadata overlay
type FrameProcessor struct {
	cfg    *config.Config
	detSvc *detection.Service
}

// NewFrameProcessor creates a new frame processor
func NewFrameProcessor(cfg *config.Config, detSvc *detection.Service) (*FrameProcessor, error) {
	return &FrameProcessor{
		cfg:    cfg,
		detSvc: detSvc,
	}, nil
}

// ProcessFrame processes a raw frame through AI and adds metadata
func (fp *FrameProcessor) ProcessFrame(rawFrame *RawFrame, projects []string, currentFPS float64, currentLatency time.Duration) *ProcessedFrame {
	var aiDetections interface{}

	// Process with AI if enabled and projects are configured
	if fp.cfg.AIEnabled && len(projects) > 0 {
		detections, err := fp.processWithAI(rawFrame, projects)
		if err != nil {
			log.Error().Err(err).Str("camera_id", rawFrame.CameraID).Msg("AI processing failed")
		} else {
			aiDetections = detections
		}
	}

	// Create ProcessedFrame with current metadata
	processedFrame := &ProcessedFrame{
		CameraID:     rawFrame.CameraID,
		Data:         rawFrame.Data, // Will be modified by addStatsFrame
		Timestamp:    rawFrame.Timestamp,
		FrameID:      rawFrame.FrameID,
		Width:        rawFrame.Width,
		Height:       rawFrame.Height,
		FPS:          currentFPS,     // Use current FPS
		Latency:      currentLatency, // Use current latency
		AIEnabled:    fp.cfg.AIEnabled,
		AIDetections: aiDetections,
		Quality:      fp.cfg.OutputQuality,
		Bitrate:      fp.cfg.OutputBitrate,
	}

	// Add stats overlay to the frame
	fp.addStatsFrame(processedFrame)

	return processedFrame
}

// processWithAI sends frame to AI service for processing
func (fp *FrameProcessor) processWithAI(rawFrame *RawFrame, projects []string) (*pb.DetectionResponse, error) {
	// Convert frame to base64
	base64Frame := base64.StdEncoding.EncodeToString(rawFrame.Data)

	// Create AI request
	req := &pb.FrameRequest{
		Image:        base64Frame,
		CameraId:     rawFrame.CameraID,
		ProjectNames: projects,
	}

	// Send to AI service with timeout
	ctx, cancel := context.WithTimeout(context.Background(), fp.cfg.AITimeout)
	defer cancel()

	resp, err := fp.detSvc.ProcessFrame(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("AI service call failed: %w", err)
	}

	return resp, nil
}

// addStatsFrame overlays statistics information on the top-left corner of the frame
func (fp *FrameProcessor) addStatsFrame(processedFrame *ProcessedFrame) {
	// Create Mat from frame data (BGR24 format)
	mat, err := gocv.NewMatFromBytes(processedFrame.Height, processedFrame.Width, gocv.MatTypeCV8UC3, processedFrame.Data)
	if err != nil {
		log.Error().Err(err).Str("camera_id", processedFrame.CameraID).Msg("Failed to create Mat from frame data")
		return
	}
	defer mat.Close()

	// Prepare stats text lines
	statsLines := fp.formatStatsText(processedFrame)

	// Text styling
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.7
	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255} // White text
	backgroundColor := color.RGBA{R: 0, G: 0, B: 0, A: 128} // Semi-transparent black background
	thickness := 2
	lineHeight := 25
	padding := 10

	// Calculate background rectangle size
	maxTextWidth := 0
	for _, line := range statsLines {
		size := gocv.GetTextSize(line, fontFace, fontScale, thickness)
		if size.X > maxTextWidth {
			maxTextWidth = size.X
		}
	}

	// Draw semi-transparent background rectangle
	backgroundRect := image.Rect(
		5,                        // x start
		5,                        // y start
		maxTextWidth+padding*2+5, // x end
		len(statsLines)*lineHeight+padding*2+5, // y end
	)

	// Create overlay for transparency effect
	overlay := gocv.NewMatWithSize(mat.Rows(), mat.Cols(), gocv.MatTypeCV8UC3)
	defer overlay.Close()

	// Fill overlay with background color
	gocv.Rectangle(&overlay, backgroundRect, backgroundColor, -1)

	// Blend overlay with original frame (30% opacity)
	gocv.AddWeighted(mat, 0.7, overlay, 0.3, 0, &mat)

	// Draw text lines
	for i, line := range statsLines {
		textPoint := image.Pt(
			padding+5,                   // x position
			padding+20+(i*lineHeight)+5, // y position
		)
		gocv.PutText(&mat, line, textPoint, fontFace, fontScale, textColor, thickness)
	}

	// Convert Mat back to bytes and update the frame data
	processedFrame.Data = mat.ToBytes()
}

// formatStatsText creates formatted text lines for the stats overlay
func (fp *FrameProcessor) formatStatsText(frame *ProcessedFrame) []string {
	var lines []string

	// Camera ID
	lines = append(lines, fmt.Sprintf("Camera: %s", frame.CameraID))

	// Frame info
	lines = append(lines, fmt.Sprintf("Frame: #%d", frame.FrameID))
	lines = append(lines, fmt.Sprintf("Resolution: %dx%d", frame.Width, frame.Height))

	// FPS (will be 0 initially, updated by camera manager)
	if frame.FPS > 0 {
		lines = append(lines, fmt.Sprintf("FPS: %.1f", frame.FPS))
	} else {
		lines = append(lines, "FPS: --.-")
	}

	// Latency
	lines = append(lines, fmt.Sprintf("Latency: %dms", frame.Latency.Milliseconds()))

	// Timestamp
	lines = append(lines, fmt.Sprintf("Time: %s", frame.Timestamp.Format("15:04:05")))

	// AI Status
	if frame.AIEnabled {
		if frame.AIDetections != nil {
			lines = append(lines, "AI: Active")
		} else {
			lines = append(lines, "AI: Enabled")
		}
	} else {
		lines = append(lines, "AI: Disabled")
	}

	// Quality settings
	lines = append(lines, fmt.Sprintf("Quality: %d%%", frame.Quality))
	if frame.Bitrate > 0 {
		lines = append(lines, fmt.Sprintf("Bitrate: %dkbps", frame.Bitrate))
	}

	return lines
}

// Shutdown shuts down the frame processor
func (fp *FrameProcessor) Shutdown() {
	log.Info().Msg("Frame processor shutdown")
}
