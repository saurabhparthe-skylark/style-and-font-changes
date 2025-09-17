package frameprocessing

import (
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"image/color"
	"math"
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

		// gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, 2)
		// cornerLength := 15
		// cornerThickness := 3

		// Adaptive thickness based on object size and type
		boxWidth := x2 - x1
		boxHeight := y2 - y1
		boxArea := boxWidth * boxHeight

		var thickness, cornerThickness int
		var cornerLength int

		// Smaller objects like license plates get thinner borders
		if det.Label == "license_plate" || det.Label == "numberplate" {
			thickness = 1
			cornerThickness = 1
			cornerLength = 8
		} else if det.Label == "face" || boxArea < 2000 { // Small objects
			thickness = 1
			cornerThickness = 2
			cornerLength = 10
		} else if boxArea < 10000 { // Medium objects
			thickness = 2
			cornerThickness = 2
			cornerLength = 12
		} else { // Large objects
			thickness = 2
			cornerThickness = 3
			cornerLength = 15
		}

		gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, thickness)
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

	// We looping over the projects and draw the solution overlays
	y := 30
	for _, project := range projects {
		switch project {

		case "drone_crowd_detection":
			// case "unified_drone":
			if solution, exists := solutionsMap["drone_crowd_detection"]; exists {
				solutions.DrawCrowdDetection(mat, solution, &y)
			}

		case "drone_person_counter":
			// case "unified_drone":
			if solution, exists := solutionsMap["drone_person_counter"]; exists {
				solutions.DrawPeopleCounter(mat, solution, &y)
			}
			// if solution, exists := solutionsMap["intrusion_detection"]; exists {
			// 	solutions.DrawPeopleCounter(mat, solution, &y)
			// }

		}
	}
}

func (fp *FrameProcessor) ProcessFrame(
	rawFrame *models.RawFrame,
	projects []string,
	ai_solutions []models.CameraSolution,
	roi_data map[string]*models.ROICoordinates,
	currentFPS float64,
	currentLatency time.Duration,
) *models.ProcessedFrame {
	// STREAM-FIRST: Always create a valid processed frame, AI can fail but stream continues
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Str("camera_id", rawFrame.CameraID).
				Interface("panic", r).
				Msg("Frame processor panic recovered - stream continues")
		}
	}()

	// Align connection state with current camera settings dynamically
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

	// ALWAYS create a valid processed frame (stream guaranteed!)
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

	// Default AI result (empty but valid)
	aiResult := &models.AIProcessingResult{
		Detections:     []models.Detection{},
		Solutions:      make(map[string]models.SolutionResults),
		ProjectResults: make(map[string]int),
		FrameProcessed: false,
	}

	// Try AI processing if conditions are met
	shouldProcessAI := aiEnabled && len(projects) > 0 && fp.grpcClient != nil
	if shouldProcessAI {
		if fp.camera != nil {
			shouldProcessAI = (fp.camera.AIFrameCounter % int64(aiFrameInterval)) == 0
		} else {
			shouldProcessAI = (rawFrame.FrameID % int64(aiFrameInterval)) == 0
		}
	}

	if shouldProcessAI {
		startTime := time.Now()
		aiResult = fp.processFrameWithAI(rawFrame, projects, aiTimeout)
		aiResult.ProcessingTime = time.Since(startTime)
	}

	log.Debug().
		Str("camera_id", rawFrame.CameraID).
		Int("detection_count", len(aiResult.Detections)).
		Dur("processing_time", aiResult.ProcessingTime).
		Bool("frame_processed", aiResult.FrameProcessed).
		Str("error_message", aiResult.ErrorMessage).
		Msg("AI processing completed")

	processedFrame.AIDetections = aiResult

	fp.addOverlay(processedFrame, aiResult, projects, ai_solutions, roi_data, aiEnabled)

	return processedFrame
}

func (fp *FrameProcessor) processFrameWithAI(rawFrame *models.RawFrame, projects []string, aiTimeout time.Duration) *models.AIProcessingResult {
	// ROBUST AI: Always return a valid result, never crash the stream
	result := &models.AIProcessingResult{
		Detections:     []models.Detection{},
		Solutions:      make(map[string]models.SolutionResults),
		ProjectResults: make(map[string]int),
		FrameProcessed: false,
	}

	defer func() {
		if r := recover(); r != nil {
			result.ErrorMessage = fmt.Sprintf("AI processing panic: %v", r)
			result.FrameProcessed = false
			log.Error().
				Str("camera_id", rawFrame.CameraID).
				Interface("panic", r).
				Msg("AI processing panic recovered - returning safe result")
		}
	}()

	// Convert frame to Mat safely
	mat, err := gocv.NewMatFromBytes(rawFrame.Height, rawFrame.Width, gocv.MatTypeCV8UC3, rawFrame.Data)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to create Mat: %v", err)
		log.Warn().Err(err).Str("camera_id", rawFrame.CameraID).Msg("AI Mat creation failed (stream continues)")
		return result
	}
	defer mat.Close()

	// Encode to JPEG safely
	buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to encode JPEG: %v", err)
		log.Warn().Err(err).Str("camera_id", rawFrame.CameraID).Msg("AI JPEG encoding failed (stream continues)")
		return result
	}
	defer buf.Close()

	jpegBytes := buf.GetBytes()

	// fmt.Println("Camera ID", rawFrame.CameraID)
	// fmt.Println("Projects", projects)

	// reqRight := &pb.FrameRequest{Image: jpegBytes, CameraId: rawFrame.CameraID, ProjectNames: []string{"drone_person_counter"}}
	req := &pb.FrameRequest{Image: jpegBytes, CameraId: rawFrame.CameraID, ProjectNames: projects}

	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	// log.Info().Msgf("AI request: %v %v", req.ProjectNames, req.CameraId)
	resp, err := fp.grpcClient.InferDetection(ctx, req)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("AI service call failed: %v", err)
		log.Warn().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Dur("timeout", aiTimeout).
			Msg("AI gRPC call failed (stream continues)")
		return result
	}

	// AI succeeded!
	result.FrameProcessed = true
	fp.extractDetectionsFromResponse(resp, result)

	log.Debug().
		Str("camera_id", rawFrame.CameraID).
		Int("detections", len(result.Detections)).
		Bool("success", true).
		Msg("AI processing successful")

	return result
}

func (fp *FrameProcessor) extractDetectionsFromResponse(resp *pb.DetectionResponse, result *models.AIProcessingResult) {
	if resp == nil || resp.Results == nil {
		return
	}
	for projectName, projectDetections := range resp.Results {
		// Map detection levels to model names from model_map
		var primaryModelName, secondaryModelName, tertiaryModelName string
		if projectDetections != nil {
			if mm := projectDetections.GetModelMap(); mm != nil {
				if v, ok := mm["primary"]; ok {
					primaryModelName = v
				}
				if v, ok := mm["secondary"]; ok {
					secondaryModelName = v
				}
				if v, ok := mm["tertiary"]; ok {
					tertiaryModelName = v
				}
			}
		}
		projectCount := 0
		for _, det := range projectDetections.GetPrimaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelPrimary)
			if modelDet != nil {
				modelDet.ModelName = primaryModelName
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		for _, det := range projectDetections.GetSecondaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelSecondary)
			if modelDet != nil {
				modelDet.ModelName = secondaryModelName
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		for _, det := range projectDetections.GetTertiaryDetections() {
			modelDet := fp.convertProtoToModels(det, projectName, models.DetectionLevelTertiary)
			if modelDet != nil {
				modelDet.ModelName = tertiaryModelName
				result.Detections = append(result.Detections, *modelDet)
				projectCount++
			}
		}
		result.ProjectResults[projectName] = projectCount

		// Extract solutions from the response
		if projectDetections.Solutions != nil {
			for solutionKey, solutionData := range projectDetections.Solutions {
				solutionResult := models.SolutionResults{
					PeopleCurrentCount:   solutionData.GetCurrentCount(),
					PeopleTotalCount:     solutionData.GetTotalCount(),
					PeopleMaxCount:       solutionData.GetMaxCount(),
					PeopleOutCount:       solutionData.GetOutRegionCount(),
					PPEViolationDetected: solutionData.ViolationDetected,
					IntrusionDetected:    solutionData.IntrusionDetected,
				}

				// Handle crowd detection data
				if crowdData := solutionData.GetCrowdDetection(); crowdData != nil {
					var crowds []models.CrowdInfo
					for _, crowd := range crowdData.GetCrowds() {
						crowds = append(crowds, models.CrowdInfo{
							Rectangle:  crowd.GetRectangle(),
							Count:      crowd.GetCount(),
							AlertLevel: crowd.GetAlertLevel(),
						})
					}
					solutionResult.CrowdDetection = &models.CrowdDetectionResult{
						Crowds:           crowds,
						TotalCrowdPeople: crowdData.GetTotalCrowdPeople(),
					}
				}

				result.Solutions[solutionKey] = solutionResult
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

func (fp *FrameProcessor) addOverlay(processedFrame *models.ProcessedFrame, aiResult *models.AIProcessingResult, projects []string, ai_solutions []models.CameraSolution, roi_data map[string]*models.ROICoordinates, ai_enabled bool) {
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
	if len(projects) > 0 {
		drawSolutionOverlays(&mat, projects, aiResult.Solutions, aiResult.Detections)
	}

	// if len(roi_data && ai_enabled) > 0 {
	// 	drawRoiOverlays(&mat, roi_data, ai_solutions)
	// }

	if fp.cfg != nil && fp.cfg.ShowMetadata {
		drawStatsOverlay(&mat, processedFrame, aiResult, fp.cfg)
	}
	processedFrame.Data = mat.ToBytes()
}

func drawRoiOverlays(mat *gocv.Mat, roi_data map[string]*models.ROICoordinates, ai_solutions []models.CameraSolution) {
	if mat == nil || len(roi_data) == 0 {
		return
	}

	// Create a map of solution ID to solution for quick lookup
	solutionMap := make(map[string]models.CameraSolution)
	for _, solution := range ai_solutions {
		solutionMap[solution.ID] = solution
	}

	frameWidth := float64(mat.Cols())
	frameHeight := float64(mat.Rows())

	// Default colors if ROI doesn't have one
	defaultColors := []color.RGBA{
		{30, 144, 255, 255}, // DodgerBlue
		{255, 20, 147, 255}, // DeepPink
		{255, 165, 0, 255},  // Orange
		{50, 205, 50, 255},  // LimeGreen
		{138, 43, 226, 255}, // BlueViolet
		{255, 69, 0, 255},   // OrangeRed
		{0, 191, 255, 255},  // DeepSkyBlue
		{255, 140, 0, 255},  // DarkOrange
		{124, 252, 0, 255},  // LawnGreen
		{186, 85, 211, 255}, // MediumOrchid
	}
	colorIndex := 0

	for solutionID, roiCoords := range roi_data {
		if roiCoords == nil {
			continue
		}

		// Get solution info
		solution, solutionExists := solutionMap[solutionID]
		solutionName := solutionID // fallback to ID
		if solutionExists && solution.Name != "" {
			solutionName = solution.Name
		} else if solutionExists && solution.ProjectName != "" {
			solutionName = solution.ProjectName
		}

		// Convert normalized coordinates to pixel coordinates
		x1 := int(roiCoords.XMin * frameWidth)
		y1 := int(roiCoords.YMin * frameHeight)
		x2 := int(roiCoords.XMax * frameWidth)
		y2 := int(roiCoords.YMax * frameHeight)

		// Ensure coordinates are within frame bounds
		x1 = max(0, min(x1, int(frameWidth)-1))
		y1 = max(0, min(y1, int(frameHeight)-1))
		x2 = max(0, min(x2, int(frameWidth)-1))
		y2 = max(0, min(y2, int(frameHeight)-1))

		// Parse ROI color or use default
		roiColor := defaultColors[colorIndex%len(defaultColors)]
		if roiCoords.Color != nil && *roiCoords.Color != "" {
			if parsedColor, err := parseHexColor(*roiCoords.Color); err == nil {
				roiColor = parsedColor
			}
		}
		colorIndex++

		// Create transparent overlay using alpha blending
		// Create a temporary mat for the overlay
		overlay := gocv.NewMatWithSize(mat.Rows(), mat.Cols(), gocv.MatTypeCV8UC3)
		defer overlay.Close()

		// Fill the overlay with the ROI color
		solidColor := color.RGBA{roiColor.R, roiColor.G, roiColor.B, 255}
		gocv.Rectangle(&overlay, image.Rect(x1, y1, x2, y2), solidColor, -1)

		// Blend the overlay with the original frame using alpha blending (10% opacity)
		alpha := 0.1 // 10% opacity
		gocv.AddWeighted(*mat, 1.0-alpha, overlay, alpha, 0, mat)

		// Draw ROI border with dashed lines
		drawDashedRectangle(mat, image.Pt(x1, y1), image.Pt(x2, y2), roiColor, 3, 12, 8)

		// Draw corner handles (small squares at corners)
		handleSize := 12
		handleColor := color.RGBA{roiColor.R, roiColor.G, roiColor.B, 255}

		// Top-left handle
		gocv.Rectangle(mat, image.Rect(x1-handleSize/2, y1-handleSize/2, x1+handleSize/2, y1+handleSize/2), handleColor, -1)
		// Top-right handle
		gocv.Rectangle(mat, image.Rect(x2-handleSize/2, y1-handleSize/2, x2+handleSize/2, y1+handleSize/2), handleColor, -1)
		// Bottom-left handle
		gocv.Rectangle(mat, image.Rect(x1-handleSize/2, y2-handleSize/2, x1+handleSize/2, y2+handleSize/2), handleColor, -1)
		// Bottom-right handle
		gocv.Rectangle(mat, image.Rect(x2-handleSize/2, y2-handleSize/2, x2+handleSize/2, y2+handleSize/2), handleColor, -1)

		// Draw solution name label with background
		if solutionName != "" {
			fontSize := 0.7
			thickness := 2
			fontFace := gocv.FontHersheyDuplex

			// Get text size
			textSize := gocv.GetTextSize(solutionName, fontFace, fontSize, thickness)
			textWidth := textSize.X
			textHeight := textSize.Y

			// Position label at top-left of ROI with some padding
			labelX := x1 + 8
			labelY := y1 - 8

			// Ensure label stays within frame
			if labelY-textHeight-8 < 0 {
				labelY = y1 + textHeight + 16 // Move below ROI if no space above
			}
			if labelX+textWidth+16 > int(frameWidth) {
				labelX = int(frameWidth) - textWidth - 16 // Move left if no space on right
			}

			// Draw label background with rounded corners effect
			bgPadding := 6
			labelBg := color.RGBA{roiColor.R, roiColor.G, roiColor.B, 200}
			gocv.Rectangle(mat,
				image.Rect(labelX-bgPadding, labelY-textHeight-bgPadding, labelX+textWidth+bgPadding, labelY+bgPadding),
				labelBg, -1)

			// Draw label border
			borderColor := color.RGBA{255, 255, 255, 180}
			gocv.Rectangle(mat,
				image.Rect(labelX-bgPadding, labelY-textHeight-bgPadding, labelX+textWidth+bgPadding, labelY+bgPadding),
				borderColor, 1)

			// Draw text in white for good contrast
			textColor := color.RGBA{255, 255, 255, 255}
			gocv.PutText(mat, solutionName, image.Pt(labelX, labelY-2), fontFace, fontSize, textColor, thickness)
		}

		// Draw ROI info (coordinates) at bottom-right corner
		coordText := fmt.Sprintf("ROI: %.1f%%, %.1f%% - %.1f%%, %.1f%%",
			roiCoords.XMin*100, roiCoords.YMin*100, roiCoords.XMax*100, roiCoords.YMax*100)

		fontSize := 0.5
		thickness := 1
		fontFace := gocv.FontHersheyDuplex
		textSize := gocv.GetTextSize(coordText, fontFace, fontSize, thickness)

		// Position at bottom-right of ROI
		coordX := x2 - textSize.X - 8
		coordY := y2 - 8

		// Ensure coordinates stay within frame
		if coordX < 0 {
			coordX = x1 + 8
		}
		if coordY > int(frameHeight)-textSize.Y {
			coordY = y2 - textSize.Y - 8
		}

		// Draw coordinate background
		bgPadding := 4
		coordBg := color.RGBA{0, 0, 0, 150}
		gocv.Rectangle(mat,
			image.Rect(coordX-bgPadding, coordY-textSize.Y-bgPadding, coordX+textSize.X+bgPadding, coordY+bgPadding),
			coordBg, -1)

		// Draw coordinate text
		coordColor := color.RGBA{255, 255, 255, 200}
		gocv.PutText(mat, coordText, image.Pt(coordX, coordY-2), fontFace, fontSize, coordColor, thickness)
	}
}

// Helper function to draw dashed rectangle
func drawDashedRectangle(mat *gocv.Mat, pt1, pt2 image.Point, clr color.RGBA, thickness, dashLength, gapLength int) {
	// Top edge
	drawDashedLine(mat, pt1, image.Pt(pt2.X, pt1.Y), clr, thickness, dashLength, gapLength)
	// Right edge
	drawDashedLine(mat, image.Pt(pt2.X, pt1.Y), pt2, clr, thickness, dashLength, gapLength)
	// Bottom edge
	drawDashedLine(mat, pt2, image.Pt(pt1.X, pt2.Y), clr, thickness, dashLength, gapLength)
	// Left edge
	drawDashedLine(mat, image.Pt(pt1.X, pt2.Y), pt1, clr, thickness, dashLength, gapLength)
}

// Helper function to draw dashed line
func drawDashedLine(mat *gocv.Mat, pt1, pt2 image.Point, clr color.RGBA, thickness, dashLength, gapLength int) {
	dx := pt2.X - pt1.X
	dy := pt2.Y - pt1.Y
	length := int(math.Sqrt(float64(dx*dx + dy*dy)))

	if length == 0 {
		return
	}

	unitX := float64(dx) / float64(length)
	unitY := float64(dy) / float64(length)

	totalDash := dashLength + gapLength
	currentPos := 0

	for currentPos < length {
		dashEnd := min(currentPos+dashLength, length)

		startX := pt1.X + int(float64(currentPos)*unitX)
		startY := pt1.Y + int(float64(currentPos)*unitY)
		endX := pt1.X + int(float64(dashEnd)*unitX)
		endY := pt1.Y + int(float64(dashEnd)*unitY)

		gocv.Line(mat, image.Pt(startX, startY), image.Pt(endX, endY), clr, thickness)

		currentPos += totalDash
	}
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
