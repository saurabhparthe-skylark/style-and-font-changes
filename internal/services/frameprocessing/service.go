package frameprocessing

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/frameprocessing/solutions"
	pb "kepler-worker-go/proto"
)

// FrameProcessor handles AI processing and metadata overlay
type FrameProcessor struct {
	cfg      *config.Config
	camera   *models.Camera
	aiClient *AIClient // Dedicated AI client with auto-retry
}

func NewFrameProcessor(cfg *config.Config) (*FrameProcessor, error) {
	return &FrameProcessor{cfg: cfg}, nil
}

func NewFrameProcessorWithCamera(cfg *config.Config, camera *models.Camera) (*FrameProcessor, error) {
	fp := &FrameProcessor{
		cfg:    cfg,
		camera: camera,
	}

	// Create AI client if camera is provided
	if camera != nil {
		fp.aiClient = NewAIClient(cfg, camera.ID)

		// Try to connect if AI is enabled
		if camera.AIEnabled && camera.AIEndpoint != "" {
			log.Info().
				Str("camera_id", camera.ID).
				Str("ai_endpoint", camera.AIEndpoint).
				Msg("Initializing frame processor with AI client")

			// Non-blocking connection attempt - will auto-retry on first frame if it fails
			if err := fp.aiClient.Connect(camera.AIEndpoint); err != nil {
				log.Warn().
					Err(err).
					Str("camera_id", camera.ID).
					Msg("Initial AI connection failed - will auto-retry during processing")
			}
		} else {
			log.Info().
				Str("camera_id", camera.ID).
				Bool("ai_enabled", camera.AIEnabled).
				Msg("Frame processor created without AI connection")
		}
	}

	return fp, nil
}

// Shutdown cleans up frame processor resources
func (fp *FrameProcessor) Shutdown() {
	if fp.aiClient != nil {
		fp.aiClient.Disconnect()
	}
}

// ensureAIConnection manages AI connection state dynamically based on camera settings
// This allows AI to be toggled on/off without restarting the camera
func (fp *FrameProcessor) ensureAIConnection() {
	if fp.camera == nil || fp.aiClient == nil {
		return
	}

	// If AI is disabled or endpoint is missing, disconnect
	if !fp.camera.AIEnabled || fp.camera.AIEndpoint == "" {
		if fp.aiClient.IsConnected() {
			log.Info().Str("camera_id", fp.camera.ID).Msg("AI disabled - closing connection")
			fp.aiClient.Disconnect()
		}
		return
	}

	// If AI is enabled, ensure connection with auto-retry
	if err := fp.aiClient.EnsureConnected(fp.camera.AIEndpoint); err != nil {
		// Error is already logged by AIClient with backoff logic
		return
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
		"person": {R: 0, G: 0, B: 255, A: 255},

		"vehicle":           {R: 0, G: 255, B: 0, A: 255},
		"motorcycle":        {R: 0, G: 255, B: 0, A: 255},
		"machinary-vehicle": {R: 0, G: 255, B: 0, A: 255},
		"truck":             {R: 0, G: 255, B: 0, A: 255},
		"3wheeler":          {R: 0, G: 255, B: 0, A: 255},
		"car":               {R: 0, G: 255, B: 0, A: 255},
		"bicycle":           {R: 0, G: 255, B: 0, A: 255},
		"van":               {R: 0, G: 255, B: 0, A: 255},

		"license_plate": {R: 255, G: 0, B: 255, A: 255},
		"numberplate":   {R: 255, G: 0, B: 255, A: 255},

		"default": {R: 0, G: 0, B: 255, A: 255},
	}
	for _, det := range detections {

		// fmt.Println(det.ProjectName, " ", det.Label)

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

		// gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, 2)
		// cornerLength := 15
		// cornerThickness := 3

		// Premium adaptive thickness based on object size and type
		// boxWidth := x2 - x1
		// boxHeight := y2 - y1
		// boxArea := boxWidth * boxHeight

		var thickness int
		// var useCorners bool
		// var cornerLength int

		thickness = 2 // Crisp thin border
		// useCorners = false // No corners for very small objects

		// Crisp, sharp borders - no blurry 1px lines
		// if det.Label == "license_plate" || det.Label == "numberplate" {
		// 	thickness = 2      // Crisp thin border
		// 	useCorners = false // No corners for very small objects
		// } else if det.Label == "face" || boxArea < 1500 { // Very small objects
		// 	thickness = 2      // Crisp thin border
		// 	useCorners = false // No corners to avoid hashtag effect
		// } else if boxArea < 5000 { // Small objects
		// 	thickness = 2 // Crisp thin border
		// 	useCorners = true
		// 	cornerLength = 6 // Very subtle corners
		// } else if boxArea < 15000 { // Medium objects
		// 	thickness = 2 // Crisp thin border
		// 	useCorners = true
		// 	cornerLength = 8
		// } else { // Large objects
		// 	thickness = 3 // Slightly thicker for large objects
		// 	useCorners = true
		// 	cornerLength = 10
		// }

		// Draw main rectangle with thin, elegant border
		gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), detColor, thickness)

		// Draw subtle corner accents only for medium/large objects
		// if useCorners && cornerLength > 0 {
		// 	// Make corners crisp and sharp - no blurry 1px lines
		// 	cornerThickness := 2

		// 	// Ensure corners don't extend too far on small boxes
		// 	maxCornerLength := min(cornerLength, min(boxWidth/4, boxHeight/4))
		// 	if maxCornerLength >= 3 { // Only draw if meaningful size
		// 		// Top-left corner
		// 		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1+maxCornerLength, y1), detColor, cornerThickness)
		// 		gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1, y1+maxCornerLength), detColor, cornerThickness)
		// 		// Top-right corner
		// 		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2-maxCornerLength, y1), detColor, cornerThickness)
		// 		gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2, y1+maxCornerLength), detColor, cornerThickness)
		// 		// Bottom-left corner
		// 		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1+maxCornerLength, y2), detColor, cornerThickness)
		// 		gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1, y2-maxCornerLength), detColor, cornerThickness)
		// 		// Bottom-right corner
		// 		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2-maxCornerLength, y2), detColor, cornerThickness)
		// 		gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2, y2-maxCornerLength), detColor, cornerThickness)
		// 	}

		// }

		// Print Track ID
		// gocv.PutText(mat, fmt.Sprintf("%d", det.TrackID), image.Pt(x1, y1), gocv.FontHersheySimplex, 0.4, detColor, 2)
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
			if solution, exists := solutionsMap["drone_crowd_detection"]; exists {
				solutions.DrawPeopleCrowdDetection(mat, solution, &y)
			}

		case "drone_vehicle_crowd":
			if solution, exists := solutionsMap["drone_vehicle_crowd"]; exists {
				solutions.DrawVehicleCrowdDetection(mat, solution, &y)
			}

		case "drone_person_counter":
			if solution, exists := solutionsMap["drone_person_counter"]; exists {
				solutions.DrawPeopleCounter("DRONE", mat, solution, &y)
			}

		case "cctv_person_counter":
			if solution, exists := solutionsMap["cctv_person_counter"]; exists {
				solutions.DrawPeopleCounter("CCTV", mat, solution, &y)
			}

		case "drone_vehicle_counter":
			if solution, exists := solutionsMap["drone_vehicle_counter"]; exists {
				solutions.DrawVehicleCounter("DRONE", mat, solution, &y)
			}
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
	fp.ensureAIConnection()

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
	shouldProcessAI := aiEnabled && len(projects) > 0 && fp.aiClient != nil && fp.aiClient.IsConnected()
	if shouldProcessAI {
		if fp.camera != nil {
			shouldProcessAI = (fp.camera.AIFrameCounter % int64(aiFrameInterval)) == 0
		} else {
			shouldProcessAI = (rawFrame.FrameID % int64(aiFrameInterval)) == 0
		}
		// if err != nil {
		// 	shouldProcessAI = false
		// }
		// if healthy.Status != "healthy" {
		// 	shouldProcessAI = false
		// }
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

	req := &pb.FrameRequest{Image: jpegBytes, CameraId: rawFrame.CameraID, ProjectNames: projects}

	ctx, cancel := context.WithTimeout(context.Background(), aiTimeout)
	defer cancel()

	// Call AI service using the dedicated AI client with auto-retry
	resp, err := fp.aiClient.InferDetection(ctx, req)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("AI service call failed: %v", err)
		log.Warn().
			Err(err).
			Str("camera_id", rawFrame.CameraID).
			Dur("timeout", aiTimeout).
			Msg("AI inference failed (stream continues, will auto-retry)")
		return result
	}

	// Pretty print the AI response as JSON
	// respJson, err := json.MarshalIndent(resp, "", "  ")
	// if err != nil {
	// 	log.Warn().Err(err).Msg("Failed to pretty print AI response as JSON")
	// } else {
	// 	log.Info().Msgf("AI Response:\n%s", string(respJson))
	// }

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
			if v := projectDetections.GetPrimaryDetections(); v != nil {
				primaryModelName = v[0].GetClassName()
			}
			if v := projectDetections.GetSecondaryDetections(); v != nil {
				secondaryModelName = v[0].GetClassName()
			}
			if v := projectDetections.GetTertiaryDetections(); v != nil {
				tertiaryModelName = v[0].GetClassName()
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
					PeopleCurrentCount: solutionData.GetCurrentCount(),
					PeopleTotalCount:   solutionData.GetTotalCount(),
					PeopleMaxCount:     solutionData.GetMaxCount(),
					PeopleOutCount:     solutionData.GetOutRegionCount(),

					VehicleCurrentCount: solutionData.GetVehicleCurrentCount(),
					VehicleTotalCount:   solutionData.GetVehicleTotalCount(),
					VehicleMaxCount:     solutionData.GetVehicleMaxCount(),
					VehicleOutCount:     solutionData.GetVehicleOutRegionCount(),

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

				if vehicleCrowdData := solutionData.GetVehicleCrowdDetection(); vehicleCrowdData != nil {
					var crowds []models.VehicleCrowdInfo
					for _, crowd := range vehicleCrowdData.GetCrowds() {
						crowds = append(crowds, models.VehicleCrowdInfo{
							Rectangle:  crowd.GetRectangle(),
							Count:      crowd.GetCount(),
							AlertLevel: crowd.GetAlertLevel(),
						})
					}
					solutionResult.VehicleCrowdDetection = &models.VehicleCrowdDetectionResult{
						Crowds:             crowds,
						TotalCrowdVehicles: vehicleCrowdData.GetTotalCrowdVehicles(),
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
