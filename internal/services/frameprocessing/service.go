package frameprocessing

import (
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"image/color"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/frameprocessing/solutions"
	pb "kepler-worker-go/proto"
)

// FrameProcessor handles AI processing and metadata overlay
type FrameProcessor struct {
	cfg    *config.Config
	camera *models.Camera

	grpcConn   *grpc.ClientConn
	grpcClient pb.DetectionServiceClient
	aiEndpoint string
}

func NewFrameProcessor(cfg *config.Config) (*FrameProcessor, error) {
	return &FrameProcessor{cfg: cfg}, nil
}

func NewFrameProcessorWithCamera(cfg *config.Config, camera *models.Camera) (*FrameProcessor, error) {
	fp := &FrameProcessor{cfg: cfg, camera: camera}

	if camera != nil && camera.AIEnabled && camera.AIEndpoint != "" {
		log.Info().Str("camera_id", camera.ID).Str("ai_endpoint", camera.AIEndpoint).Bool("ai_enabled", camera.AIEnabled).Msg("Initializing frame processor with AI enabled")
		if err := fp.initGRPCConnection(camera.AIEndpoint); err != nil {
			log.Error().Err(err).Str("camera_id", camera.ID).Str("ai_endpoint", camera.AIEndpoint).Msg("Failed to initialize AI gRPC connection - AI will be disabled for this camera")
		} else {
			log.Info().Str("camera_id", camera.ID).Str("ai_endpoint", camera.AIEndpoint).Msg("AI gRPC connection successfully established")
		}
	} else {
		log.Info().Str("camera_id", func() string {
			if camera != nil {
				return camera.ID
			}
			return "unknown"
		}()).Bool("ai_enabled", camera != nil && camera.AIEnabled).Str("ai_endpoint", func() string {
			if camera != nil {
				return camera.AIEndpoint
			}
			return ""
		}()).Msg("Frame processor created without AI (AI disabled or no endpoint)")
	}

	return fp, nil
}

func (fp *FrameProcessor) initGRPCConnection(endpoint string) error {
	if fp.grpcConn != nil && fp.aiEndpoint == endpoint {
		return nil
	}
	if fp.grpcConn != nil {
		fp.grpcConn.Close()
	}

	normalizedEndpoint, creds, err := fp.parseGRPCEndpoint(endpoint)
	if err != nil {
		return fmt.Errorf("failed to parse AI endpoint %s: %w", endpoint, err)
	}

	log.Info().Str("original_endpoint", endpoint).Str("normalized_endpoint", normalizedEndpoint).Bool("use_tls", creds.Info().SecurityProtocol == "tls").Msg("Connecting to AI gRPC service")

	conn, err := grpc.NewClient(normalizedEndpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to AI service at %s: %w", normalizedEndpoint, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := pb.NewDetectionServiceClient(conn)
	if _, err := client.HealthCheck(ctx, &pb.Empty{}); err != nil {
		conn.Close()
		return fmt.Errorf("AI service health check failed at %s: %w", normalizedEndpoint, err)
	}

	fp.grpcConn = conn
	fp.grpcClient = client
	fp.aiEndpoint = endpoint
	log.Info().Str("ai_endpoint", normalizedEndpoint).Msg("AI gRPC connection successfully established")
	return nil
}

func (fp *FrameProcessor) parseGRPCEndpoint(endpoint string) (string, credentials.TransportCredentials, error) {
	if !strings.Contains(endpoint, "://") {
		if strings.Contains(endpoint, ".") && !strings.Contains(endpoint, ":") {
			endpoint = "https://" + endpoint + ":443"
		} else if strings.Contains(endpoint, ":") {
			parts := strings.Split(endpoint, ":")
			if len(parts) == 2 {
				if port, err := strconv.Atoi(parts[1]); err == nil {
					if port == 443 || port == 8443 || port == 9443 {
						endpoint = "https://" + endpoint
					} else {
						endpoint = "http://" + endpoint
					}
				} else {
					endpoint = "http://" + endpoint
				}
			}
		} else {
			endpoint = "https://" + endpoint + ":443"
		}
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			host = u.Hostname() + ":443"
		case "http":
			host = u.Hostname() + ":80"
		default:
			return "", nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
		}
	}

	var creds credentials.TransportCredentials
	switch u.Scheme {
	case "https":
		tlsConfig := &tls.Config{ServerName: u.Hostname()}
		creds = credentials.NewTLS(tlsConfig)
	case "http":
		creds = insecure.NewCredentials()
	default:
		return "", nil, fmt.Errorf("unsupported scheme: %s (supported: http, https)", u.Scheme)
	}

	return host, creds, nil
}

func (fp *FrameProcessor) Shutdown() {
	if fp.grpcConn != nil {
		fp.grpcConn.Close()
		fp.grpcConn = nil
		fp.grpcClient = nil
	}
}

// refreshAIConnection ensures the gRPC client state matches current camera AI settings.
// It lazily connects or disconnects without requiring a pipeline restart so AI can be
// toggled at runtime smoothly.
func (fp *FrameProcessor) refreshAIConnection() {
	if fp.camera == nil {
		return
	}

	// If AI is disabled or endpoint missing, ensure connection is torn down
	if !fp.camera.AIEnabled || fp.camera.AIEndpoint == "" {
		if fp.grpcConn != nil || fp.grpcClient != nil {
			log.Info().Str("camera_id", fp.camera.ID).Msg("AI disabled - closing gRPC connection")
			fp.Shutdown()
			fp.aiEndpoint = ""
		}
		return
	}

	// If AI is enabled, (re)establish connection when needed
	if fp.grpcClient == nil || fp.aiEndpoint != fp.camera.AIEndpoint {
		if err := fp.initGRPCConnection(fp.camera.AIEndpoint); err != nil {
			log.Error().Err(err).Str("camera_id", fp.camera.ID).Str("ai_endpoint", fp.camera.AIEndpoint).Msg("Failed to (re)initialize AI gRPC connection")
		}
	}
}

// Overlay registry for per-project drawings
type OverlayDrawer interface {
	DrawDetections(mat *gocv.Mat, detections []models.Detection)
	DrawSolutions(mat *gocv.Mat, solutionResults map[string]models.SolutionResults)
}

var overlayRegistry = map[string]OverlayDrawer{}

func RegisterOverlay(project string, drawer OverlayDrawer) {
	overlayRegistry[project] = drawer
}

func getOverlay(project string) OverlayDrawer {
	if d, ok := overlayRegistry[project]; ok {
		return d
	}
	return defaultOverlay{}
}

// Default overlay implementation
type defaultOverlay struct{}

func (d defaultOverlay) DrawDetections(mat *gocv.Mat, detections []models.Detection) {
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
	}
}

func (d defaultOverlay) DrawSolutions(mat *gocv.Mat, solutionResults map[string]models.SolutionResults) {
	if mat == nil || len(solutionResults) == 0 {
		return
	}

	// This is the old interface - we need projects and detections!
	// For now, just log that we need to update the calling code
	log.Warn().Msg("DrawSolutions called with old interface - need projects and detections")
}

// drawSolutionOverlays draws overlays for each project using simple for and switch
func drawSolutionOverlays(mat *gocv.Mat, projects []string, solutionsMap map[string]models.SolutionResults, detections []models.Detection) {
	if mat == nil || len(projects) == 0 {
		return
	}

	y := 30
	for _, project := range projects {
		switch project {
		case "people_counter", "person_detection", "person_detection_lr":
			// Get solution data or create default
			if solution, exists := solutionsMap[project]; exists {
				solutions.DrawPeopleCounter(mat, solution, &y)
			} else {
				// Default solution
				defaultSolution := models.SolutionResults{CurrentCount: 0}
				solutions.DrawPeopleCounter(mat, defaultSolution, &y)
			}
		case "intrusion_detection":
			if solution, exists := solutionsMap[project]; exists {
				solutions.DrawIntrusion(mat, solution, &y)
			} else {
				hasIntrusion := false
				defaultSolution := models.SolutionResults{IntrusionDetected: &hasIntrusion}
				solutions.DrawIntrusion(mat, defaultSolution, &y)
			}
		case "ppe_detection", "violation_detection":
			if solution, exists := solutionsMap[project]; exists {
				solutions.DrawPPE(mat, solution, &y)
			} else {
				hasViolation := false
				defaultSolution := models.SolutionResults{ViolationDetected: &hasViolation}
				solutions.DrawPPE(mat, defaultSolution, &y)
			}
		case "vehicle_detection":
			if solution, exists := solutionsMap[project]; exists {
				solutions.DrawVehicle(mat, solution, &y)
			} else {
				defaultSolution := models.SolutionResults{CurrentCount: 0}
				solutions.DrawVehicle(mat, defaultSolution, &y)
			}
		}
	}
}

func (fp *FrameProcessor) ProcessFrame(rawFrame *models.RawFrame, projects []string, currentFPS float64, currentLatency time.Duration) *models.ProcessedFrame {
	// Align connection state with current camera settings on each frame in a lightweight way
	fp.refreshAIConnection()
	aiEnabled := fp.cfg.AIEnabled
	aiTimeout := fp.cfg.AITimeout
	aiFrameInterval := fp.cfg.AIFrameInterval
	if fp.camera != nil {
		aiEnabled = fp.camera.AIEnabled
		aiTimeout = fp.camera.AITimeout
		fp.camera.AIFrameCounter++
	}

	// Deep-copy raw bytes so RawData stays clean and Data is a separate buffer for overlays
	rawCopy := make([]byte, len(rawFrame.Data))
	copy(rawCopy, rawFrame.Data)
	annotatedCopy := make([]byte, len(rawFrame.Data))
	copy(annotatedCopy, rawFrame.Data)

	processedFrame := &models.ProcessedFrame{
		CameraID:  rawFrame.CameraID,
		Data:      annotatedCopy,
		RawData:   rawCopy,
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

	aiResult := &models.AIProcessingResult{Detections: []models.Detection{}, Solutions: make(map[string]models.SolutionResults), ProjectResults: make(map[string]int), FrameProcessed: false}

	shouldProcessAI := aiEnabled && len(projects) > 0 && fp.grpcClient != nil
	log.Debug().Str("camera_id", rawFrame.CameraID).Bool("ai_enabled", aiEnabled).Int("projects_count", len(projects)).Bool("grpc_client_available", fp.grpcClient != nil).Bool("initial_should_process", shouldProcessAI).Msg("AI processing decision check")

	if shouldProcessAI {
		if fp.camera != nil {
			shouldProcessAI = (fp.camera.AIFrameCounter % int64(aiFrameInterval)) == 0
			log.Debug().Str("camera_id", rawFrame.CameraID).Int64("ai_frame_counter", fp.camera.AIFrameCounter).Int("ai_frame_interval", aiFrameInterval).Bool("will_process_ai", shouldProcessAI).Msg("ai_frame_interval_check")
		} else {
			shouldProcessAI = (rawFrame.FrameID % int64(aiFrameInterval)) == 0
			log.Debug().Str("camera_id", rawFrame.CameraID).Int64("frame_id", rawFrame.FrameID).Int("ai_frame_interval", aiFrameInterval).Bool("will_process_ai", shouldProcessAI).Msg("ai_frame_interval_check")
		}
	} else {
		log.Debug().Str("camera_id", rawFrame.CameraID).Bool("ai_enabled", aiEnabled).Int("projects_count", len(projects)).Bool("grpc_client_available", fp.grpcClient != nil).Msg("AI processing skipped - conditions not met")
	}

	if shouldProcessAI {
		startTime := time.Now()
		aiResult = fp.processFrameWithAI(rawFrame, projects, aiTimeout)
		aiResult.ProcessingTime = time.Since(startTime)
		log.Debug().Str("camera_id", rawFrame.CameraID).Int("detection_count", len(aiResult.Detections)).Dur("processing_time", aiResult.ProcessingTime).Bool("frame_processed", aiResult.FrameProcessed).Int64("ai_frame_counter", func() int64 {
			if fp.camera != nil {
				return fp.camera.AIFrameCounter
			}
			return rawFrame.FrameID
		}()).Msg("ai_processing_completed")
	}

	processedFrame.AIDetections = aiResult
	fp.addOverlay(processedFrame, aiResult, projects)
	return processedFrame
}

func (fp *FrameProcessor) processFrameWithAI(rawFrame *models.RawFrame, projects []string, aiTimeout time.Duration) *models.AIProcessingResult {
	result := &models.AIProcessingResult{Detections: []models.Detection{}, Solutions: make(map[string]models.SolutionResults), ProjectResults: make(map[string]int), FrameProcessed: false}

	mat, err := gocv.NewMatFromBytes(rawFrame.Height, rawFrame.Width, gocv.MatTypeCV8UC3, rawFrame.Data)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to create Mat from frame data: %v", err)
		log.Error().Err(err).Str("camera_id", rawFrame.CameraID).Msg("ai_mat_from_bytes_failed")
		return result
	}
	defer mat.Close()

	buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to encode frame as JPEG: %v", err)
		log.Error().Err(err).Str("camera_id", rawFrame.CameraID).Msg("ai_jpeg_encoding_failed")
		return result
	}
	defer buf.Close()

	jpegBytes := buf.GetBytes()

	req := &pb.FrameRequest{Image: jpegBytes, CameraId: rawFrame.CameraID, ProjectNames: projects}
	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	resp, err := fp.grpcClient.InferDetection(ctx, req)

	log.Debug().Str("camera_id", rawFrame.CameraID).Str("json_data", func() string {
		if resp != nil {
			return resp.String()
		}
		return ""
	}()).Msg("AI response")

	//print resp
	// log.Info().Msgf("AI response: %v", resp)

	if err != nil {
		result.ErrorMessage = fmt.Sprintf("AI service call failed: %v", err)
		log.Error().Err(err).Str("camera_id", rawFrame.CameraID).Dur("timeout", aiTimeout).Msg("ai_grpc_call_failed")
		return result
	}

	result.FrameProcessed = true
	fp.extractDetectionsFromResponse(resp, result)
	return result
}

func (fp *FrameProcessor) extractDetectionsFromResponse(resp *pb.DetectionResponse, result *models.AIProcessingResult) {
	if resp == nil || resp.Results == nil {
		return
	}
	for projectName, projectDetections := range resp.Results {
		projectCount := 0
		for _, det := range projectDetections.GetPrimaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelPrimary)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		for _, det := range projectDetections.GetSecondaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelSecondary)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		for _, det := range projectDetections.GetTertiaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelTertiary)
			if modelDet != nil {
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		result.ProjectResults[projectName] = projectCount

		// Extract solutions from the response
		if projectDetections.Solutions != nil {
			for solutionKey, solutionData := range projectDetections.Solutions {
				result.Solutions[solutionKey] = models.SolutionResults{
					CurrentCount:      solutionData.GetCurrentCount(),
					TotalCount:        solutionData.GetTotalCount(),
					MaxCount:          solutionData.GetMaxCount(),
					OutRegionCount:    solutionData.GetOutRegionCount(),
					ViolationDetected: solutionData.ViolationDetected,
					IntrusionDetected: solutionData.IntrusionDetected,
				}
			}
		}
	}
}

func (fp *FrameProcessor) convertProtoToModels(det *pb.Detection, projectName string, detectionLevel models.DetectionLevel) *models.Detection {
	if det == nil || len(det.Bbox) != 4 {
		return nil
	}
	falseMatchID := det.GetFalseMatchId()
	if falseMatchID == "None" || falseMatchID == "" {
		falseMatchID = ""
	}
	trueMatchID := det.GetTrueMatchId()
	if trueMatchID == "None" || trueMatchID == "" {
		trueMatchID = ""
	}
	modelDet := &models.Detection{
		TrackID:          det.GetTrackId(),
		Score:            det.GetConfidence(),
		Label:            det.GetClassName(),
		ClassName:        det.GetClassName(),
		BBox:             det.GetBbox(),
		Timestamp:        time.Now(),
		DetectionLevel:   detectionLevel,
		ProjectName:      projectName,
		ParentID:         det.GetParentId(),
		SendAlert:        det.GetSendAlert(),
		IsRPN:            false,
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

func (fp *FrameProcessor) addOverlay(processedFrame *models.ProcessedFrame, aiResult *models.AIProcessingResult, projects []string) {
	mat, err := gocv.NewMatFromBytes(processedFrame.Height, processedFrame.Width, gocv.MatTypeCV8UC3, processedFrame.Data)
	if err != nil {
		log.Error().Err(err).Str("camera_id", processedFrame.CameraID).Msg("Failed to create Mat from frame data")
		return
	}
	defer mat.Close()

	if aiResult != nil && len(aiResult.Detections) > 0 {
		// Group detections by project and apply per-project overlays
		byProject := map[string][]models.Detection{}
		for _, d := range aiResult.Detections {
			byProject[d.ProjectName] = append(byProject[d.ProjectName], d)
		}
		for project, dets := range byProject {
			drawer := getOverlay(project)
			drawer.DrawDetections(&mat, dets)
		}
	}

	// Draw solution overlays based on camera projects
	var projectsToUse []string
	if fp.camera != nil && len(fp.camera.Projects) > 0 {
		projectsToUse = fp.camera.Projects
	} else {
		projectsToUse = projects
	}

	if len(projectsToUse) > 0 {
		drawSolutionOverlays(&mat, projectsToUse, aiResult.Solutions, aiResult.Detections)
	}

	if fp.cfg != nil && fp.cfg.ShowMetadata {
		drawStatsOverlay(&mat, processedFrame, aiResult, fp.cfg)
	}
	processedFrame.Data = mat.ToBytes()
}

func drawStatsOverlay(mat *gocv.Mat, frame *models.ProcessedFrame, aiResult *models.AIProcessingResult, cfg *config.Config) {
	if mat == nil {
		return
	}
	statsLines := formatStatsLines(frame, aiResult, cfg)
	if len(statsLines) == 0 {
		return
	}
	fontFace := gocv.FontHersheySimplex
	if cfg != nil {
		// OverlayFont directly maps to gocv font constants
		fontFace = gocv.HersheyFont(cfg.OverlayFont)
	}
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
	gocv.Rectangle(mat, image.Rect(5, startY-padding, maxTextWidth+padding*2+5, startY+len(statsLines)*lineHeight+padding), bgColor, -1)
	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if cfg != nil && cfg.OverlayColor != "" {
		if c, err := parseHexColor(cfg.OverlayColor); err == nil {
			// Keep text readable: if chosen color is too dark, use white
			if isDarkColor(c) {
				textColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			} else {
				textColor = c
			}
		}
	}
	for i, line := range statsLines {
		gocv.PutText(mat, line, image.Pt(padding+5, startY+(i*lineHeight)+20), fontFace, fontScale, textColor, thickness)
	}
}

func formatStatsLines(frame *models.ProcessedFrame, aiResult *models.AIProcessingResult, cfg *config.Config) []string {
	var lines []string

	show := func(flag bool) bool { return cfg == nil || flag }

	if show(cfg.ShowCameraID) {
		lines = append(lines, fmt.Sprintf("Camera: %s", frame.CameraID))
	}
	if show(cfg.ShowFrameID) {
		lines = append(lines, fmt.Sprintf("Frame: #%d", frame.FrameID))
	}
	if show(cfg.ShowResolution) {
		lines = append(lines, fmt.Sprintf("Resolution: %dx%d", frame.Width, frame.Height))
	}
	if show(cfg.ShowFPS) {
		if frame.FPS > 0 {
			lines = append(lines, fmt.Sprintf("FPS: %.1f", frame.FPS))
		} else {
			lines = append(lines, "FPS: --.-")
		}
	}
	if show(cfg.ShowLatency) {
		lines = append(lines, fmt.Sprintf("Latency: %dms", frame.Latency.Milliseconds()))
	}

	if frame.AIEnabled {
		if show(cfg.ShowAIStatus) {
			aiStatus := "AI: Enabled"
			if aiResult != nil && aiResult.FrameProcessed {
				if show(cfg.ShowAIDetectionsCount) {
					aiStatus = fmt.Sprintf("AI: Active (%d detections)", len(aiResult.Detections))
				} else {
					aiStatus = "AI: Active"
				}
			} else if aiResult != nil && aiResult.ErrorMessage != "" {
				aiStatus = "AI: Error"
			}
			lines = append(lines, aiStatus)
		}

		if aiResult != nil && aiResult.FrameProcessed {
			if show(cfg.ShowAIProcessingTime) && aiResult.ProcessingTime > 0 {
				lines = append(lines, fmt.Sprintf("AI Time: %dms", aiResult.ProcessingTime.Milliseconds()))
			}
			if show(cfg.ShowProjectCounts) {
				for project, count := range aiResult.ProjectResults {
					if count > 0 {
						lines = append(lines, fmt.Sprintf("%s: %d", project, count))
					}
				}
			}
		}
	} else if show(cfg.ShowAIStatus) {
		lines = append(lines, "AI: Disabled")
	}

	if show(cfg.ShowTime) {
		lines = append(lines, fmt.Sprintf("Time: %s", frame.Timestamp.Format("15:04:05")))
	}
	if show(cfg.ShowQuality) {
		lines = append(lines, fmt.Sprintf("Quality: %d%%", frame.Quality))
	}
	if show(cfg.ShowBitrate) && frame.Bitrate > 0 {
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

// parseHexColor converts a color string like "#RRGGBB" to color.RGBA
func parseHexColor(s string) (color.RGBA, error) {
	var c color.RGBA
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	if len(s) != 6 {
		return c, fmt.Errorf("invalid color length: %s", s)
	}
	r, err := strconv.ParseUint(s[0:2], 16, 8)
	if err != nil {
		return c, err
	}
	g, err := strconv.ParseUint(s[2:4], 16, 8)
	if err != nil {
		return c, err
	}
	b, err := strconv.ParseUint(s[4:6], 16, 8)
	if err != nil {
		return c, err
	}
	c = color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
	return c, nil
}

// isDarkColor determines if a color is considered dark using perceived luminance
func isDarkColor(c color.RGBA) bool {
	// sRGB luminance(Y) from RGB: 0.2126 R + 0.7152 G + 0.0722 B
	luminance := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	// threshold ~128 for 0-255 range
	return luminance < 128
}
