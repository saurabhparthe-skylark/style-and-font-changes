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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	pb "kepler-worker-go/proto"
)

// FrameProcessor handles AI processing and metadata overlay
type FrameProcessor struct {
	cfg    *config.Config
	camera *models.Camera // Per-camera configuration

	// Direct gRPC connection
	grpcConn   *grpc.ClientConn
	grpcClient pb.DetectionServiceClient
	aiEndpoint string
}

// NewFrameProcessor creates a new frame processor
func NewFrameProcessor(cfg *config.Config) (*FrameProcessor, error) {
	return &FrameProcessor{
		cfg: cfg,
	}, nil
}

// NewFrameProcessorWithCamera creates a frame processor with per-camera AI configuration
func NewFrameProcessorWithCamera(cfg *config.Config, camera *models.Camera) (*FrameProcessor, error) {
	fp := &FrameProcessor{
		cfg:    cfg,
		camera: camera,
	}

	// Initialize gRPC connection if AI is enabled
	if camera != nil && camera.AIEnabled && camera.AIEndpoint != "" {
		if err := fp.initGRPCConnection(camera.AIEndpoint); err != nil {
			log.Warn().Err(err).Str("ai_endpoint", camera.AIEndpoint).Msg("Failed to initialize AI gRPC connection")
		}
	}

	return fp, nil
}

// initGRPCConnection establishes connection to AI server
func (fp *FrameProcessor) initGRPCConnection(endpoint string) error {
	if fp.grpcConn != nil && fp.aiEndpoint == endpoint {
		return nil // Already connected to this endpoint
	}

	// Close existing connection if different endpoint
	if fp.grpcConn != nil {
		fp.grpcConn.Close()
	}

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to connect to AI service at %s: %w", endpoint, err)
	}

	// Test connection with quick health check
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := pb.NewDetectionServiceClient(conn)
	if _, err := client.HealthCheck(ctx, &pb.Empty{}); err != nil {
		conn.Close()
		return fmt.Errorf("AI service health check failed at %s: %w", endpoint, err)
	}

	fp.grpcConn = conn
	fp.grpcClient = client
	fp.aiEndpoint = endpoint

	log.Info().Str("ai_endpoint", endpoint).Msg("AI gRPC connection established")
	return nil
}

// Shutdown closes gRPC connections
func (fp *FrameProcessor) Shutdown() {
	if fp.grpcConn != nil {
		fp.grpcConn.Close()
		fp.grpcConn = nil
		fp.grpcClient = nil
	}
}

// OPTIMIZATION: Eliminated StandardizedDetection type and conversions
// Frame processor now directly outputs models.Detection to avoid:
// gRPC proto → StandardizedDetection → models.Detection (multiple conversions)
// Now: gRPC proto → models.Detection (single conversion)

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
	Detections     []models.Detection         `json:"detections"`
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
		Detections:     []models.Detection{},
		Solutions:      make(map[string]SolutionResults),
		ProjectResults: make(map[string]int),
		FrameProcessed: false,
	}

	// Check if this frame should be processed by AI (Nth frame logic)
	shouldProcessAI := aiEnabled && len(projects) > 0 && fp.grpcClient != nil
	if shouldProcessAI {
		if fp.camera != nil {
			// Only process every Nth frame for AI when per-camera tracking is available
			shouldProcessAI = (fp.camera.AIFrameCounter % int64(aiFrameInterval)) == 0

			log.Debug().
				Str("camera_id", rawFrame.CameraID).
				Int64("ai_frame_counter", fp.camera.AIFrameCounter).
				Int("ai_frame_interval", aiFrameInterval).
				Bool("will_process_ai", shouldProcessAI).
				Msg("ai_frame_interval_check")
		} else {
			// Fallback: when no per-camera context, use frame ID for interval
			shouldProcessAI = (rawFrame.FrameID % int64(aiFrameInterval)) == 0

			log.Debug().
				Str("camera_id", rawFrame.CameraID).
				Int64("frame_id", rawFrame.FrameID).
				Int("ai_frame_interval", aiFrameInterval).
				Bool("will_process_ai", shouldProcessAI).
				Msg("ai_frame_interval_check")
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
			Msg("ai_processing_completed")
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
		Detections:     []models.Detection{},
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
			Msg("ai_mat_from_bytes_failed")
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
			Msg("ai_jpeg_encoding_failed")
		return result
	}
	defer buf.Close()

	// Get JPEG bytes
	jpegBytes := buf.GetBytes()

	// Create gRPC request
	req := &pb.FrameRequest{
		Image:        jpegBytes,
		CameraId:     rawFrame.CameraID,
		ProjectNames: projects,
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	// Call AI service directly
	resp, err := fp.grpcClient.InferDetection(ctx, req)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("AI service call failed: %v", err)
		log.Error().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Dur("timeout", aiTimeout).
			Msg("ai_grpc_call_failed")
		return result
	}

	result.FrameProcessed = true
	fp.extractDetectionsFromResponse(resp, result)

	return result
}

func (fp *FrameProcessor) extractDetectionsFromResponse(resp *pb.DetectionResponse, result *AIProcessingResult) {
	if resp == nil || resp.Results == nil {
		return
	}

	for projectName, projectDetections := range resp.Results {
		projectCount := 0

		for _, det := range projectDetections.GetPrimaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}

		for _, det := range projectDetections.GetSecondaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}

		for _, det := range projectDetections.GetTertiaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}

		result.ProjectResults[projectName] = projectCount
	}
}

func (fp *FrameProcessor) convertProtoToModels(det *pb.Detection, projectName string) *models.Detection {
	if det == nil || len(det.Bbox) != 4 {
		return nil
	}

	falseMatchID := det.GetFalseMatchId()
	if falseMatchID == "None" || falseMatchID == "" {
		falseMatchID = ""
	}

	trueMatchID := ""
	// Handle true_match_id if it exists in the proto (you may need to add this field)

	modelDet := &models.Detection{
		TrackID:          det.GetTrackId(),
		Score:            det.GetConfidence(),
		Label:            det.GetClassName(),
		ClassName:        det.GetClassName(),
		BBox:             det.GetBbox(),
		Timestamp:        time.Now(),
		ProjectName:      projectName,
		ParentID:         det.GetParentId(),
		SendAlert:        det.GetSendAlert(),
		IsRPN:            false, // Set based on detection logic if needed
		FalseMatchID:     falseMatchID,
		TrueMatchID:      trueMatchID,
		IsSuppressed:     falseMatchID != "",
		TrueSuppressed:   trueMatchID != "",
		HasVest:          det.HasVest,
		HasHelmet:        det.HasHelmet,
		HasHead:          det.HasHead,
		HasGloves:        det.HasGloves,
		HasGlasses:       det.HasGlasses,
		Compliance:       det.Compliance,
		Violations:       det.GetViolations(),
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

	return modelDet
}

func (fp *FrameProcessor) addEnhancedStatsFrame(processedFrame *models.ProcessedFrame, aiResult *AIProcessingResult) {
	mat, err := gocv.NewMatFromBytes(processedFrame.Height, processedFrame.Width, gocv.MatTypeCV8UC3, processedFrame.Data)
	if err != nil {
		log.Error().Err(err).Str("camera_id", processedFrame.CameraID).Msg("Failed to create Mat from frame data")
		return
	}
	defer mat.Close()

	if aiResult != nil && len(aiResult.Detections) > 0 {
		fp.drawDetections(&mat, aiResult.Detections)
	}

	if aiResult != nil && len(aiResult.Solutions) > 0 {
		fp.drawSolutionOverlays(&mat, aiResult.Solutions)
	}

	fp.drawStatsOverlay(&mat, processedFrame, aiResult)
	processedFrame.Data = mat.ToBytes()
}

func (fp *FrameProcessor) drawDetections(mat *gocv.Mat, detections []models.Detection) {
	if mat == nil || len(detections) == 0 {
		return
	}

	colors := map[string]color.RGBA{
		"person":        {R: 0, G: 255, B: 0, A: 255},
		"face":          {R: 255, G: 255, B: 0, A: 255},
		"vehicle":       {R: 0, G: 165, B: 255, A: 255},
		"car":           {R: 0, G: 140, B: 255, A: 255},
		"truck":         {R: 0, G: 100, B: 255, A: 255},
		"license_plate": {R: 255, G: 0, B: 255, A: 255},
		"numberplate":   {R: 255, G: 0, B: 255, A: 255},
		"default":       {R: 0, G: 0, B: 255, A: 255},
	}

	for _, det := range detections {
		if det.IsSuppressed || det.TrueSuppressed {
			continue
		}

		if len(det.BBox) != 4 {
			continue
		}

		x1, y1, x2, y2 := int(det.BBox[0]), int(det.BBox[1]), int(det.BBox[2]), int(det.BBox[3])

		width, height := mat.Cols(), mat.Rows()
		x1 = max(0, min(width-2, x1))
		y1 = max(0, min(height-2, y1))
		x2 = max(x1+1, min(width-1, x2))
		y2 = max(y1+1, min(height-1, y2))

		detColor := colors["default"]
		if c, exists := colors[det.Label]; exists {
			detColor = c
		}

		if det.Score < 0.5 {
			detColor.A = 128
		}

		gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, 2)

		cornerLength := 15
		cornerThickness := 3

		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1+cornerLength, y1), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1, y1+cornerLength), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2-cornerLength, y1), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2, y1+cornerLength), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1+cornerLength, y2), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1, y2-cornerLength), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2-cornerLength, y2), detColor, cornerThickness)
		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2, y2-cornerLength), detColor, cornerThickness)

		labelText := fp.formatDetectionLabel(det)
		if labelText != "" {
			fp.drawLabelWithBackground(mat, labelText, x1, y1, detColor)
		}
	}
}

func (fp *FrameProcessor) formatDetectionLabel(det models.Detection) string {
	label := fmt.Sprintf("%s %.2f", det.Label, det.Score)

	if det.TrackID > 0 {
		label += fmt.Sprintf(" ID:%d", det.TrackID)
	}

	if det.Compliance != nil {
		label += fmt.Sprintf(" %s", *det.Compliance)
	}

	if det.Plate != nil && *det.Plate != "" {
		label += fmt.Sprintf(" [%s]", *det.Plate)
	}

	if det.Gender != nil && *det.Gender != "" {
		label += fmt.Sprintf(" %s", *det.Gender)
	}

	if len(det.Violations) > 0 {
		label += fmt.Sprintf(" VIOLATION")
	}

	return label
}

func (fp *FrameProcessor) drawLabelWithBackground(mat *gocv.Mat, text string, x, y int, textColor color.RGBA) {
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.6
	thickness := 2

	textSize := gocv.GetTextSize(text, fontFace, fontScale, thickness)

	labelY := y - 10
	if labelY < textSize.Y+5 {
		labelY = y + textSize.Y + 10
	}

	labelX := x
	if labelX+textSize.X > mat.Cols() {
		labelX = mat.Cols() - textSize.X - 5
	}
	if labelY+textSize.Y > mat.Rows() {
		labelY = mat.Rows() - 5
	}

	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180}
	gocv.Rectangle(mat,
		image.Rect(labelX-2, labelY-textSize.Y-2, labelX+textSize.X+2, labelY+2),
		bgColor, -1)

	whiteColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	gocv.PutText(mat, text, image.Pt(labelX, labelY), fontFace, fontScale, whiteColor, thickness)
}

func (fp *FrameProcessor) drawSolutionOverlays(mat *gocv.Mat, solutions map[string]SolutionResults) {
	if mat == nil || len(solutions) == 0 {
		return
	}

	y := 30
	for solutionKey, solution := range solutions {
		if containsString(solutionKey, "people_counter") {
			fp.drawPeopleCounterOverlay(mat, &y, solution)
		}
	}
}

func (fp *FrameProcessor) drawPeopleCounterOverlay(mat *gocv.Mat, y *int, solution SolutionResults) {
	bgColor := color.RGBA{R: 0, G: 50, B: 150, A: 200}
	gocv.Rectangle(mat, image.Rect(5, *y-25, 400, *y+60), bgColor, -1)

	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.8

	gocv.PutText(mat, "People Counter", image.Pt(10, *y), fontFace, fontScale, textColor, 2)
	*y += 30

	counterText := fmt.Sprintf("Current: %d | Max: %d", solution.CurrentCount, solution.MaxCount)
	gocv.PutText(mat, counterText, image.Pt(10, *y), fontFace, 0.7, textColor, 2)
	*y += 40
}

func (fp *FrameProcessor) drawStatsOverlay(mat *gocv.Mat, frame *models.ProcessedFrame, aiResult *AIProcessingResult) {
	if mat == nil {
		return
	}

	statsLines := fp.formatStatsLines(frame, aiResult)
	if len(statsLines) == 0 {
		return
	}

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

	startY := mat.Rows() - (len(statsLines)*lineHeight + padding*2)
	if startY < 100 {
		startY = 100
	}

	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180}
	gocv.Rectangle(mat,
		image.Rect(5, startY-padding, maxTextWidth+padding*2+5, startY+len(statsLines)*lineHeight+padding),
		bgColor, -1)

	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for i, line := range statsLines {
		gocv.PutText(mat, line,
			image.Pt(padding+5, startY+(i*lineHeight)+20),
			fontFace, fontScale, textColor, thickness)
	}
}

func (fp *FrameProcessor) formatStatsLines(frame *models.ProcessedFrame, aiResult *AIProcessingResult) []string {
	var lines []string

	lines = append(lines, fmt.Sprintf("Camera: %s", frame.CameraID))
	lines = append(lines, fmt.Sprintf("Frame: #%d", frame.FrameID))
	lines = append(lines, fmt.Sprintf("Resolution: %dx%d", frame.Width, frame.Height))

	if frame.FPS > 0 {
		lines = append(lines, fmt.Sprintf("FPS: %.1f", frame.FPS))
	} else {
		lines = append(lines, "FPS: --.-")
	}
	lines = append(lines, fmt.Sprintf("Latency: %dms", frame.Latency.Milliseconds()))

	aiStatus := "AI: Disabled"
	if frame.AIEnabled {
		if aiResult != nil && aiResult.FrameProcessed {
			detectionCount := len(aiResult.Detections)
			aiStatus = fmt.Sprintf("AI: Active (%d detections)", detectionCount)

			if aiResult.ProcessingTime > 0 {
				lines = append(lines, fmt.Sprintf("AI Time: %dms", aiResult.ProcessingTime.Milliseconds()))
			}

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

	lines = append(lines, fmt.Sprintf("Time: %s", frame.Timestamp.Format("15:04:05")))
	lines = append(lines, fmt.Sprintf("Quality: %d%%", frame.Quality))
	if frame.Bitrate > 0 {
		lines = append(lines, fmt.Sprintf("Bitrate: %dkbps", frame.Bitrate))
	}

	return lines
}

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

func (fp *FrameProcessor) getAIEndpoint() string {
	if fp.camera != nil {
		return fp.camera.AIEndpoint
	}
	return fp.cfg.AIGRPCURL
}
