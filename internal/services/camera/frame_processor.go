package camera

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/detection"
	pb "kepler-worker-go/proto"
)

// FrameProcessor handles AI processing and metadata overlay
type FrameProcessor struct {
	cfg    *config.Config
	detSvc *detection.Service
	camera *models.Camera // Per-camera configuration
}

// NewFrameProcessor creates a new frame processor
func NewFrameProcessor(cfg *config.Config, detSvc *detection.Service) (*FrameProcessor, error) {
	return &FrameProcessor{
		cfg:    cfg,
		detSvc: detSvc,
	}, nil
}

// NewFrameProcessorWithDetectionService creates a frame processor with per-camera AI configuration
func NewFrameProcessorWithDetectionService(cfg *config.Config, detSvc *detection.Service, camera *models.Camera) (*FrameProcessor, error) {
	return &FrameProcessor{
		cfg:    cfg,
		detSvc: detSvc,
		camera: camera,
	}, nil
}

// StandardizedDetection represents a standardized detection result for visualization
type StandardizedDetection struct {
	TrackID          int32     `json:"track_id"`
	Score            float32   `json:"score"`
	Label            string    `json:"label"`
	ClassName        string    `json:"class_name"`
	BBox             []float32 `json:"bbox"` // [x1, y1, x2, y2]
	Timestamp        time.Time `json:"timestamp"`
	ParentID         int32     `json:"parent_id,omitempty"`
	ProjectName      string    `json:"project_name,omitempty"`
	FalseMatchID     string    `json:"false_match_id,omitempty"`
	TrueMatchID      string    `json:"true_match_id,omitempty"`
	IsSuppressed     bool      `json:"is_suppressed"`
	IsTrueSuppressed bool      `json:"is_true_suppressed"`
	IsRPN            bool      `json:"is_rpn"`
	SendAlert        bool      `json:"send_alert"`
	Violations       []string  `json:"violations,omitempty"`

	// PPE fields
	HasVest    *bool   `json:"has_vest,omitempty"`
	HasHelmet  *bool   `json:"has_helmet,omitempty"`
	HasHead    *bool   `json:"has_head,omitempty"`
	Compliance *string `json:"compliance,omitempty"`
	HasGloves  *bool   `json:"has_gloves,omitempty"`
	HasGlasses *bool   `json:"has_glasses,omitempty"`

	// Additional fields
	IsIntrusion      *bool    `json:"is_intrusion,omitempty"`
	Plate            *string  `json:"plate,omitempty"`
	Color            *string  `json:"color,omitempty"`
	Gender           *string  `json:"gender,omitempty"`
	IsLoitering      *bool    `json:"is_loitering,omitempty"`
	TimeInRegion     *float32 `json:"time_in_region,omitempty"`
	VesselType       *string  `json:"vessel_type,omitempty"`
	RecognitionID    *int32   `json:"recognition_id,omitempty"`
	RecognitionScore *float32 `json:"recognition_score,omitempty"`
}

// SolutionResults represents solution-specific results (e.g., people counter)
type SolutionResults struct {
	CurrentCount      int32 `json:"current_count"`
	TotalCount        int32 `json:"total_count"`
	MaxCount          int32 `json:"max_count"`
	OutRegionCount    int32 `json:"out_region_count"`
	ViolationDetected *bool `json:"violation_detected,omitempty"`
	IntrusionDetected *bool `json:"intrusion_detected,omitempty"`
}

// AIProcessingResult contains the complete AI processing result for a frame
type AIProcessingResult struct {
	Detections     []StandardizedDetection    `json:"detections"`
	Solutions      map[string]SolutionResults `json:"solutions"` // project_name -> solution_name -> results
	ProcessingTime time.Duration              `json:"processing_time"`
	ErrorMessage   string                     `json:"error_message,omitempty"`
	FrameProcessed bool                       `json:"frame_processed"`
	ProjectResults map[string]int             `json:"project_results"` // project_name -> detection_count
}

// ProcessFrame processes a raw frame through AI and adds metadata with enterprise-grade features
func (fp *FrameProcessor) ProcessFrame(rawFrame *models.RawFrame, projects []string, currentFPS float64, currentLatency time.Duration) *models.ProcessedFrame {
	// Determine AI settings - use per-camera if available, otherwise fall back to global config
	aiEnabled := fp.cfg.AIEnabled
	aiTimeout := fp.cfg.AITimeout
	aiFrameInterval := fp.cfg.AIFrameInterval

	if fp.camera != nil {
		aiEnabled = fp.camera.AIEnabled
		aiTimeout = fp.camera.AITimeout
		// Increment AI frame counter for this camera
		fp.camera.AIFrameCounter++
	}

	// Create ProcessedFrame with current metadata
	processedFrame := &models.ProcessedFrame{
		CameraID:  rawFrame.CameraID,
		Data:      rawFrame.Data, // Will be modified by addEnhancedStatsFrame
		Timestamp: rawFrame.Timestamp,
		FrameID:   rawFrame.FrameID,
		Width:     rawFrame.Width,
		Height:    rawFrame.Height,
		FPS:       currentFPS,
		Latency:   currentLatency,
		AIEnabled: aiEnabled,
		Quality:   fp.cfg.OutputQuality,
		Bitrate:   fp.cfg.OutputBitrate,
	}

	// Initialize AI result
	aiResult := &AIProcessingResult{
		Detections:     []StandardizedDetection{},
		Solutions:      make(map[string]SolutionResults),
		ProjectResults: make(map[string]int),
		FrameProcessed: false,
	}

	// Check if this frame should be processed by AI (Nth frame logic)
	shouldProcessAI := aiEnabled && len(projects) > 0 && fp.detSvc != nil
	if shouldProcessAI {
		if fp.camera != nil {
			// Only process every Nth frame for AI when per-camera tracking is available
			shouldProcessAI = (fp.camera.AIFrameCounter % int64(aiFrameInterval)) == 0

			log.Debug().
				Str("camera_id", rawFrame.CameraID).
				Int64("ai_frame_counter", fp.camera.AIFrameCounter).
				Int("ai_frame_interval", aiFrameInterval).
				Bool("will_process_ai", shouldProcessAI).
				Msg("AI frame interval check with per-camera tracking")
		} else {
			// Fallback: when no per-camera context, use frame ID for interval
			shouldProcessAI = (rawFrame.FrameID % int64(aiFrameInterval)) == 0

			log.Debug().
				Str("camera_id", rawFrame.CameraID).
				Int64("frame_id", rawFrame.FrameID).
				Int("ai_frame_interval", aiFrameInterval).
				Bool("will_process_ai", shouldProcessAI).
				Msg("AI frame interval check with frame ID fallback")
		}
	}

	// Process with AI if conditions are met
	if shouldProcessAI {
		startTime := time.Now()
		aiResult = fp.processFrameWithAI(rawFrame, projects, aiTimeout)
		aiResult.ProcessingTime = time.Since(startTime)

		log.Debug().
			Str("camera_id", rawFrame.CameraID).
			Int("detection_count", len(aiResult.Detections)).
			Dur("processing_time", aiResult.ProcessingTime).
			Bool("frame_processed", aiResult.FrameProcessed).
			Int64("ai_frame_counter", func() int64 {
				if fp.camera != nil {
					return fp.camera.AIFrameCounter
				}
				return rawFrame.FrameID
			}()).
			Msg("AI processing completed")
	}

	// Set AI detections in processed frame
	processedFrame.AIDetections = aiResult

	// Add enhanced stats overlay to the frame
	fp.addEnhancedStatsFrame(processedFrame, aiResult)

	return processedFrame
}

// processFrameWithAI handles the complete AI processing pipeline
func (fp *FrameProcessor) processFrameWithAI(rawFrame *models.RawFrame, projects []string, aiTimeout time.Duration) *AIProcessingResult {
	result := &AIProcessingResult{
		Detections:     []StandardizedDetection{},
		Solutions:      make(map[string]SolutionResults),
		ProjectResults: make(map[string]int),
		FrameProcessed: false,
	}

	// Create Mat from raw frame data for JPEG encoding
	mat, err := gocv.NewMatFromBytes(rawFrame.Height, rawFrame.Width, gocv.MatTypeCV8UC3, rawFrame.Data)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to create Mat from frame data: %v", err)
		log.Error().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Msg("Failed to create Mat from frame data for AI processing")
		return result
	}
	defer mat.Close()

	// Use high-quality JPEG encoding to minimize quality loss
	// Set JPEG quality to 95% to maintain color fidelity
	buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to encode frame as JPEG: %v", err)
		log.Error().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Msg("Failed to encode frame as JPEG for AI processing")
		return result
	}
	defer buf.Close()

	// Convert JPEG bytes to base64 for gRPC (same as Python version)
	jpegBytes := buf.GetBytes()
	// Create AI request
	req := &pb.FrameRequest{
		Image:        jpegBytes,
		CameraId:     rawFrame.CameraID,
		ProjectNames: projects,
	}

	log.Debug().
		Str("camera_id", rawFrame.CameraID).
		Int("jpeg_size", len(jpegBytes)).
		Msg("Sending high-quality JPEG-encoded frame to AI service")

	// Send to AI service with per-camera timeout
	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	resp, err := fp.detSvc.ProcessFrame(ctx, req)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("AI service call failed: %v", err)
		log.Error().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Str("ai_endpoint", fp.getAIEndpoint()).
			Dur("ai_timeout", aiTimeout).
			Msg("AI processing failed")
		return result
	}

	log.Debug().Msgf("AI service response: %v", resp)

	// Process AI response
	result.FrameProcessed = true
	fp.extractDetectionsFromResponse(resp, result)
	fp.extractSolutionsFromResponse(resp, result)

	return result
}

// extractDetectionsFromResponse converts protobuf detections to standardized format
func (fp *FrameProcessor) extractDetectionsFromResponse(resp *pb.DetectionResponse, result *AIProcessingResult) {
	if resp == nil || resp.Results == nil {
		return
	}

	for projectName, projectDetections := range resp.Results {
		projectCount := 0

		// Process primary detections
		for _, det := range projectDetections.GetPrimaryDetections() {
			stdDet := fp.convertProtoToStandardized(det, projectName)
			if stdDet != nil {
				result.Detections = append(result.Detections, *stdDet)
				projectCount++
			}
		}

		// Process secondary detections
		for _, det := range projectDetections.GetSecondaryDetections() {
			stdDet := fp.convertProtoToStandardized(det, projectName)
			if stdDet != nil {
				result.Detections = append(result.Detections, *stdDet)
				projectCount++
			}
		}

		// Process tertiary detections
		for _, det := range projectDetections.GetTertiaryDetections() {
			stdDet := fp.convertProtoToStandardized(det, projectName)
			if stdDet != nil {
				result.Detections = append(result.Detections, *stdDet)
				projectCount++
			}
		}

		result.ProjectResults[projectName] = projectCount
	}
}

// convertProtoToStandardized converts a protobuf detection to standardized format
func (fp *FrameProcessor) convertProtoToStandardized(det *pb.Detection, projectName string) *StandardizedDetection {
	if det == nil || len(det.Bbox) != 4 {
		return nil
	}

	// Check for suppression
	falseMatchID := det.GetFalseMatchId()
	if falseMatchID == "None" || falseMatchID == "" {
		falseMatchID = ""
	}

	// Create standardized detection
	stdDet := &StandardizedDetection{
		TrackID:      det.GetTrackId(),
		Score:        det.GetConfidence(),
		Label:        det.GetClassName(),
		ClassName:    det.GetClassName(),
		BBox:         det.GetBbox(),
		Timestamp:    time.Now(),
		ParentID:     det.GetParentId(),
		ProjectName:  projectName,
		FalseMatchID: falseMatchID,
		IsSuppressed: falseMatchID != "",
		SendAlert:    det.GetSendAlert(),

		// PPE fields
		HasVest:    det.HasVest,
		HasHelmet:  det.HasHelmet,
		HasHead:    det.HasHead,
		Compliance: det.Compliance,
		HasGloves:  det.HasGloves,
		HasGlasses: det.HasGlasses,
		Violations: det.GetViolations(),

		// Additional fields
		IsIntrusion:      det.IsIntrusion,
		Plate:            det.Plate,
		Color:            det.Color,
		Gender:           det.Gender,
		IsLoitering:      det.IsLoitering,
		TimeInRegion:     det.TimeInRegion,
		VesselType:       det.VesselType,
		RecognitionID:    det.RecognitionId,
		RecognitionScore: det.RecognitionScore,
	}

	return stdDet
}

// extractSolutionsFromResponse extracts solution results (people counter, etc.)
func (fp *FrameProcessor) extractSolutionsFromResponse(resp *pb.DetectionResponse, result *AIProcessingResult) {
	if resp == nil || resp.Results == nil {
		return
	}

	for projectName, projectDetections := range resp.Results {
		if projectDetections.Solutions == nil {
			continue
		}

		for solutionName, solutionResult := range projectDetections.Solutions {
			solRes := SolutionResults{}

			// Extract solution data based on available fields
			if solutionResult.CurrentCount != nil {
				solRes.CurrentCount = *solutionResult.CurrentCount
			}
			if solutionResult.TotalCount != nil {
				solRes.TotalCount = *solutionResult.TotalCount
			}
			if solutionResult.MaxCount != nil {
				solRes.MaxCount = *solutionResult.MaxCount
			}
			if solutionResult.OutRegionCount != nil {
				solRes.OutRegionCount = *solutionResult.OutRegionCount
			}
			if solutionResult.ViolationDetected != nil {
				solRes.ViolationDetected = solutionResult.ViolationDetected
			}
			if solutionResult.IntrusionDetected != nil {
				solRes.IntrusionDetected = solutionResult.IntrusionDetected
			}

			// Store solution result with composite key
			key := fmt.Sprintf("%s_%s", projectName, solutionName)
			result.Solutions[key] = solRes
		}
	}
}

// addEnhancedStatsFrame adds comprehensive stats and detection visualization to the frame
func (fp *FrameProcessor) addEnhancedStatsFrame(processedFrame *models.ProcessedFrame, aiResult *AIProcessingResult) {
	// Create Mat from frame data (BGR24 format)
	mat, err := gocv.NewMatFromBytes(processedFrame.Height, processedFrame.Width, gocv.MatTypeCV8UC3, processedFrame.Data)
	if err != nil {
		log.Error().Err(err).Str("camera_id", processedFrame.CameraID).Msg("Failed to create Mat from frame data")
		return
	}
	defer mat.Close()

	// Add detection visualizations first
	if aiResult != nil && len(aiResult.Detections) > 0 {
		fp.drawDetections(&mat, aiResult.Detections)
	}

	// Add solution overlays (people counter, etc.)
	if aiResult != nil && len(aiResult.Solutions) > 0 {
		fp.drawSolutionOverlays(&mat, aiResult.Solutions)
	}

	// Add comprehensive stats overlay
	fp.drawStatsOverlay(&mat, processedFrame, aiResult)

	// Convert Mat back to bytes and update the frame data
	processedFrame.Data = mat.ToBytes()
}

// drawDetections draws detection bounding boxes and labels with enterprise-grade styling
func (fp *FrameProcessor) drawDetections(mat *gocv.Mat, detections []StandardizedDetection) {
	if mat == nil || len(detections) == 0 {
		return
	}

	// Detection colors based on type and confidence
	colors := map[string]color.RGBA{
		"person":        {R: 0, G: 255, B: 0, A: 255},   // Green
		"face":          {R: 255, G: 255, B: 0, A: 255}, // Cyan
		"vehicle":       {R: 0, G: 165, B: 255, A: 255}, // Orange
		"car":           {R: 0, G: 140, B: 255, A: 255}, // Dark orange
		"truck":         {R: 0, G: 100, B: 255, A: 255}, // Darker orange
		"license_plate": {R: 255, G: 0, B: 255, A: 255}, // Magenta
		"numberplate":   {R: 255, G: 0, B: 255, A: 255}, // Magenta
		"default":       {R: 0, G: 0, B: 255, A: 255},   // Red
	}

	for _, det := range detections {
		// Skip suppressed detections unless configured to show them
		if det.IsSuppressed || det.IsTrueSuppressed {
			continue
		}

		// Validate bounding box
		if len(det.BBox) != 4 {
			continue
		}

		x1, y1, x2, y2 := int(det.BBox[0]), int(det.BBox[1]), int(det.BBox[2]), int(det.BBox[3])

		// Ensure coordinates are within frame bounds
		width, height := mat.Cols(), mat.Rows()
		x1 = max(0, min(width-2, x1))
		y1 = max(0, min(height-2, y1))
		x2 = max(x1+1, min(width-1, x2))
		y2 = max(y1+1, min(height-1, y2))

		// Choose color based on detection type
		detColor := colors["default"]
		if c, exists := colors[det.Label]; exists {
			detColor = c
		}

		// Adjust color based on confidence
		if det.Score < 0.5 {
			detColor.A = 128 // Semi-transparent for low confidence
		}

		// Draw main bounding box
		gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, 2)

		// Draw corner markers for better visibility
		cornerLength := 15
		cornerThickness := 3

		// Top-left corner
		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1+cornerLength, y1), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1, y1+cornerLength), detColor, cornerThickness)

		// Top-right corner
		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2-cornerLength, y1), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2, y1+cornerLength), detColor, cornerThickness)

		// Bottom-left corner
		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1+cornerLength, y2), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1, y2-cornerLength), detColor, cornerThickness)

		// Bottom-right corner
		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2-cornerLength, y2), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2, y2-cornerLength), detColor, cornerThickness)

		// Prepare label text with comprehensive information
		labelText := fp.formatDetectionLabel(det)

		if labelText != "" {
			fp.drawLabelWithBackground(mat, labelText, x1, y1, detColor)
		}
	}
}

// formatDetectionLabel creates a comprehensive label for detection
func (fp *FrameProcessor) formatDetectionLabel(det StandardizedDetection) string {
	label := fmt.Sprintf("%s %.2f", det.Label, det.Score)

	if det.TrackID > 0 {
		label += fmt.Sprintf(" ID:%d", det.TrackID)
	}

	// Add PPE compliance information
	if det.Compliance != nil {
		label += fmt.Sprintf(" %s", *det.Compliance)
	}

	// Add plate information
	if det.Plate != nil && *det.Plate != "" {
		label += fmt.Sprintf(" [%s]", *det.Plate)
	}

	// Add gender information
	if det.Gender != nil && *det.Gender != "" {
		label += fmt.Sprintf(" %s", *det.Gender)
	}

	// Add violations
	if len(det.Violations) > 0 {
		label += fmt.Sprintf(" VIOLATION")
	}

	return label
}

// drawLabelWithBackground draws text with a semi-transparent background
func (fp *FrameProcessor) drawLabelWithBackground(mat *gocv.Mat, text string, x, y int, textColor color.RGBA) {
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.6
	thickness := 2

	// Calculate text size
	textSize := gocv.GetTextSize(text, fontFace, fontScale, thickness)

	// Position label above the box if possible
	labelY := y - 10
	if labelY < textSize.Y+5 {
		labelY = y + textSize.Y + 10
	}

	// Ensure label stays within frame bounds
	labelX := x
	if labelX+textSize.X > mat.Cols() {
		labelX = mat.Cols() - textSize.X - 5
	}
	if labelY+textSize.Y > mat.Rows() {
		labelY = mat.Rows() - 5
	}

	// Draw semi-transparent background rectangle directly on the mat
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180} // Black background for better readability
	gocv.Rectangle(mat,
		image.Rect(labelX-2, labelY-textSize.Y-2, labelX+textSize.X+2, labelY+2),
		bgColor, -1)

	// Draw the text
	whiteColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	gocv.PutText(mat, text, image.Pt(labelX, labelY), fontFace, fontScale, whiteColor, thickness)
}

// drawSolutionOverlays draws solution-specific overlays (people counter, etc.)
func (fp *FrameProcessor) drawSolutionOverlays(mat *gocv.Mat, solutions map[string]SolutionResults) {
	if mat == nil || len(solutions) == 0 {
		return
	}

	y := 30
	for solutionKey, solution := range solutions {
		// Check if this is a people counter solution
		if containsString(solutionKey, "people_counter") {
			fp.drawPeopleCounterOverlay(mat, &y, solution)
		}
		// Add other solution types as needed
	}
}

// drawPeopleCounterOverlay draws people counter information
func (fp *FrameProcessor) drawPeopleCounterOverlay(mat *gocv.Mat, y *int, solution SolutionResults) {
	// Draw background rectangle directly without blending
	bgColor := color.RGBA{R: 0, G: 50, B: 150, A: 200} // Dark blue
	gocv.Rectangle(mat, image.Rect(5, *y-25, 400, *y+60), bgColor, -1)

	// Draw counter text
	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.8

	gocv.PutText(mat, "People Counter", image.Pt(10, *y), fontFace, fontScale, textColor, 2)
	*y += 30

	counterText := fmt.Sprintf("Current: %d | Max: %d", solution.CurrentCount, solution.MaxCount)
	gocv.PutText(mat, counterText, image.Pt(10, *y), fontFace, 0.7, textColor, 2)
	*y += 40
}

// drawStatsOverlay draws comprehensive statistics overlay
func (fp *FrameProcessor) drawStatsOverlay(mat *gocv.Mat, frame *models.ProcessedFrame, aiResult *AIProcessingResult) {
	if mat == nil {
		return
	}

	// Prepare stats information
	statsLines := fp.formatStatsLines(frame, aiResult)
	if len(statsLines) == 0 {
		return
	}

	// Calculate overlay dimensions
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.6
	thickness := 2
	lineHeight := 25
	padding := 10

	maxTextWidth := 0
	for _, line := range statsLines {
		size := gocv.GetTextSize(line, fontFace, fontScale, thickness)
		if size.X > maxTextWidth {
			maxTextWidth = size.X
		}
	}

	// Determine overlay position (bottom-left to avoid conflict with solutions)
	startY := mat.Rows() - (len(statsLines)*lineHeight + padding*2)
	if startY < 100 { // Don't overlap with solutions
		startY = 100
	}

	// Draw background rectangle directly without blending to preserve original frame brightness
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180} // Semi-transparent black
	gocv.Rectangle(mat,
		image.Rect(5, startY-padding, maxTextWidth+padding*2+5, startY+len(statsLines)*lineHeight+padding),
		bgColor, -1)

	// Draw stats text
	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for i, line := range statsLines {
		gocv.PutText(mat, line,
			image.Pt(padding+5, startY+(i*lineHeight)+20),
			fontFace, fontScale, textColor, thickness)
	}
}

// formatStatsLines creates formatted text lines for the stats overlay
func (fp *FrameProcessor) formatStatsLines(frame *models.ProcessedFrame, aiResult *AIProcessingResult) []string {
	var lines []string

	// Camera information
	lines = append(lines, fmt.Sprintf("Camera: %s", frame.CameraID))
	lines = append(lines, fmt.Sprintf("Frame: #%d", frame.FrameID))
	lines = append(lines, fmt.Sprintf("Resolution: %dx%d", frame.Width, frame.Height))

	// Performance metrics
	if frame.FPS > 0 {
		lines = append(lines, fmt.Sprintf("FPS: %.1f", frame.FPS))
	} else {
		lines = append(lines, "FPS: --.-")
	}
	lines = append(lines, fmt.Sprintf("Latency: %dms", frame.Latency.Milliseconds()))

	// AI status and performance
	aiStatus := "AI: Disabled"
	if frame.AIEnabled {
		if aiResult != nil && aiResult.FrameProcessed {
			detectionCount := len(aiResult.Detections)
			aiStatus = fmt.Sprintf("AI: Active (%d detections)", detectionCount)

			if aiResult.ProcessingTime > 0 {
				lines = append(lines, fmt.Sprintf("AI Time: %dms", aiResult.ProcessingTime.Milliseconds()))
			}

			// Add project-specific detection counts
			for project, count := range aiResult.ProjectResults {
				if count > 0 {
					lines = append(lines, fmt.Sprintf("%s: %d", project, count))
				}
			}
		} else {
			aiStatus = "AI: Enabled"
			if aiResult != nil && aiResult.ErrorMessage != "" {
				aiStatus = "AI: Error"
			}
		}
	}
	lines = append(lines, aiStatus)

	// Timestamp
	lines = append(lines, fmt.Sprintf("Time: %s", frame.Timestamp.Format("15:04:05")))

	// Quality information
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

// Helper functions
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func containsString(str, substr string) bool {
	return len(str) >= len(substr) && str[:len(substr)] == substr ||
		len(str) > len(substr) && str[len(str)-len(substr):] == substr ||
		(len(str) > len(substr) && strings.Contains(str, substr))
}

// getAIEndpoint returns the AI endpoint for logging purposes
func (fp *FrameProcessor) getAIEndpoint() string {
	if fp.camera != nil {
		return fp.camera.AIEndpoint
	}
	return fp.cfg.AIGRPCURL
}
