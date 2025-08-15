package postprocessing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/helpers"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/postprocessing/alerts"
	"kepler-worker-go/internal/services/postprocessing/suppressions"
)

// Service handles alert and suppression processing and publishing
type Service struct {
	cfg        *config.Config
	publisher  models.MessagePublisher
	cooldownMu sync.RWMutex
	lastSent   map[string]time.Time

	// Cooldown periods for different alert types
	normalCooldown       time.Duration
	anomalyCooldown      time.Duration
	selfLearningCooldown time.Duration
	suppressionCooldown  time.Duration
}

// NewService creates a new postprocessing service
func NewService(cfg *config.Config, publisher models.MessagePublisher) (*Service, error) {
	if publisher == nil {
		return nil, fmt.Errorf("message publisher is required")
	}

	s := &Service{
		cfg:                  cfg,
		publisher:            publisher,
		lastSent:             make(map[string]time.Time),
		normalCooldown:       cfg.AlertsCooldown,
		anomalyCooldown:      3 * time.Second, // Shorter cooldown for anomalies
		selfLearningCooldown: 5 * time.Second, // Medium cooldown for self-learning
		suppressionCooldown:  cfg.SuppressionCooldown,
	}

	log.Info().
		Dur("normal_cooldown", s.normalCooldown).
		Dur("anomaly_cooldown", s.anomalyCooldown).
		Dur("self_learning_cooldown", s.selfLearningCooldown).
		Dur("suppression_cooldown", s.suppressionCooldown).
		Msg("Post-processing service initialized")

	return s, nil
}

// Shutdown stops the service gracefully
func (s *Service) Shutdown(ctx context.Context) error {
	log.Info().Msg("Post-processing service shutdown")
	return nil
}

// ProcessDetections processes detections for both alerts and suppressions
func (s *Service) ProcessDetections(cameraID string, detections []models.Detection, frameMetadata models.FrameMetadata, frameData []byte) models.ProcessedDetections {
	result := models.ProcessedDetections{
		TotalDetections: len(detections),
		Errors:          make([]string, 0),
	}

	if len(detections) == 0 {
		return result
	}

	log.Debug().
		Str("camera_id", cameraID).
		Int("total_detections", len(detections)).
		Msg("🔄 Processing detections for alerts and suppressions")

	// Process suppressions and filter valid detections
	validDetections := make([]models.Detection, 0, len(detections))

	for _, det := range detections {
		// Check for suppressions first
		suppressionDecision := s.ShouldCreateSuppression(det, cameraID)
		if suppressionDecision.ShouldSuppress {
			// Check suppression cooldown
			suppressionKey := models.AlertCooldownKey{
				CameraID:    cameraID,
				ProjectName: det.ProjectName,
				TrackID:     strconv.Itoa(int(det.TrackID)),
			}

			if s.CheckCooldown(suppressionKey, "suppression") {
				s.processSuppressionDirectly(det, suppressionDecision, cameraID, frameData, frameMetadata)
				s.UpdateCooldown(suppressionKey, "suppression")
				result.SuppressedDetections++
			} else {
				log.Debug().
					Str("camera_id", cameraID).
					Int32("track_id", det.TrackID).
					Msg("Suppression blocked by cooldown")
			}
			continue
		}

		// Valid detection for alert processing
		validDetections = append(validDetections, det)
	}

	result.ValidDetections = len(validDetections)

	// Group detections by track ID for alert processing
	detectionsByTrack := make(map[int32][]models.Detection)
	for _, det := range validDetections {
		detectionsByTrack[det.TrackID] = append(detectionsByTrack[det.TrackID], det)
	}

	// Process each track separately for alerts
	for trackID, trackDetections := range detectionsByTrack {
		// Use the highest confidence detection as the primary one
		primaryDetection := s.findPrimaryDetection(trackDetections)

		// Determine if alert should be created
		decision := s.ShouldCreateAlert(primaryDetection, primaryDetection.ProjectName)
		if !decision.ShouldAlert {
			continue
		}

		// Check cooldown
		cooldownKey := models.AlertCooldownKey{
			CameraID:    cameraID,
			ProjectName: primaryDetection.ProjectName,
			TrackID:     strconv.Itoa(int(trackID)),
		}

		if !s.CheckCooldown(cooldownKey, decision.CooldownType) {
			log.Debug().
				Str("camera_id", cameraID).
				Int32("track_id", trackID).
				Str("cooldown_type", decision.CooldownType).
				Msg("Alert blocked by cooldown")
			continue
		}

		// Process alert directly
		if err := s.processAlertDirectly(primaryDetection, decision, cameraID, frameData, frameMetadata); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("Alert processing error for track %d: %v", trackID, err))
		} else {
			s.UpdateCooldown(cooldownKey, decision.CooldownType)
			result.AlertsCreated++
		}
	}

	log.Debug().
		Str("camera_id", cameraID).
		Int("alerts_created", result.AlertsCreated).
		Int("valid_detections", result.ValidDetections).
		Int("suppressed_detections", result.SuppressedDetections).
		Msg("Detection processing completed")

	return result
}

// ShouldCreateSuppression determines if a suppression should be created
func (s *Service) ShouldCreateSuppression(detection models.Detection, cameraID string) models.SuppressionDecision {
	decision := models.SuppressionDecision{
		ShouldSuppress: false,
		Metadata:       make(map[string]interface{}),
	}

	// Check for false suppressions
	if detection.FalseMatchID != "" || detection.IsSuppressed {
		return suppressions.HandleFalseSuppression(detection, cameraID)
	}

	// Check for true suppressions
	if detection.TrueMatchID != "" || detection.TrueSuppressed {
		return suppressions.HandleTrueSuppression(detection, cameraID)
	}

	return decision
}

// ShouldCreateAlert determines if an alert should be created
func (s *Service) ShouldCreateAlert(detection models.Detection, projectName string) models.AlertDecision {
	decision := models.AlertDecision{
		ShouldAlert:  false,
		CooldownType: "normal",
		Metadata:     make(map[string]interface{}),
	}

	// Handle anomaly detections using proper handler
	if strings.Contains(strings.ToUpper(detection.Label), "ANOMALY") {
		return alerts.HandleAnomalyDetection(detection, decision)
	}

	// Handle self-learning detections using proper handler
	if detection.IsRPN && !strings.Contains(strings.ToLower(detection.Label), "rtdetr") {
		return alerts.HandleSelfLearningDetection(detection, decision)
	}

	// Use switch case for different project types
	detectionType := s.getDetectionType(projectName, detection.Label)

	switch detectionType {
	case models.DetectionTypePPE:
		return alerts.HandlePPEDetection(detection, decision)

	case models.DetectionTypeCellphone:
		return alerts.HandleCellphoneDetection(detection, decision)

	case models.DetectionTypeDrone, models.DetectionTypeAircraft, models.DetectionTypeThermal:
		return alerts.HandleDroneDetection(detection, decision)

	case models.DetectionTypeFireSmoke:
		return alerts.HandleFireSmokeDetection(detection, decision)

	case models.DetectionTypeGeneral:
		return alerts.HandleGeneralDetection(detection, decision)

	default:
		log.Warn().
			Str("project_name", projectName).
			Str("label", detection.Label).
			Msg("Unknown detection type, using general handler")
		return alerts.HandleGeneralDetection(detection, decision)
	}
}

// processAlertDirectly processes an alert immediately without workers
func (s *Service) processAlertDirectly(detection models.Detection, decision models.AlertDecision, cameraID string, frameData []byte, frameMetadata models.FrameMetadata) error {
	start := time.Now()

	// Use specialized build functions based on alert type
	var payload models.AlertPayload

	switch decision.AlertType {
	case models.AlertTypePPEViolation:
		payload = alerts.BuildPPEAlert(detection, cameraID, frameData)

	case models.AlertTypeCellphoneUsage:
		payload = alerts.BuildCellphoneAlert(detection, cameraID, frameData)

	case models.AlertTypeDroneDetection:
		payload = alerts.BuildDroneAlert(detection, cameraID, frameData)

	case models.AlertTypeFireSmoke:
		payload = alerts.BuildFireSmokeAlert(detection, cameraID, frameData)

	case models.AlertTypeAnomalyDetection:
		payload = alerts.BuildAnomalyAlert(detection, cameraID, frameData)

	case models.AlertTypeSelfLearned:
		payload = alerts.BuildSelfLearningAlert(detection, cameraID, frameData)

	case models.AlertTypeHighConfidence:
		payload = alerts.BuildGeneralAlert(detection, cameraID, frameData)

	default:
		log.Warn().
			Str("alert_type", string(decision.AlertType)).
			Msg("Unknown alert type, using general alert builder")
		payload = alerts.BuildGeneralAlert(detection, cameraID, frameData)
	}

	// Add processing metadata
	if payload.Metadata == nil {
		payload.Metadata = make(map[string]interface{})
	}
	payload.Metadata["processing_time_ms"] = time.Since(start).Milliseconds()
	payload.Metadata["frame_dimensions"] = map[string]interface{}{
		"width":  frameMetadata.Width,
		"height": frameMetadata.Height,
	}
	payload.Metadata["processing_timestamp"] = time.Now()

	// Update detection record with frame metadata
	if payload.DetectionRecord.FrameID == 0 {
		payload.DetectionRecord.FrameID = frameMetadata.FrameID
		payload.DetectionRecord.DetectionCountInFrame = frameMetadata.AllDetCount
	}

	// Skip payload optimization for performance (NATS limits will be increased)
	if s.cfg.ImageCompressionEnabled {
		if err := helpers.OptimizePayloadForSizeWithConfig(&payload, s.cfg); err != nil {
			log.Warn().
				Err(err).
				Str("camera_id", cameraID).
				Int32("track_id", detection.TrackID).
				Str("alert_type", string(payload.Alert.AlertType)).
				Msg("Payload size validation failed, but continuing for performance")
		}
	}

	// Publish alert
	subject := s.cfg.AlertsSubject
	if subject == "" {
		subject = "alerts.ppe"
	}

	if err := s.publisher.Publish(subject, payload); err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cameraID).
			Int32("track_id", detection.TrackID).
			Str("alert_type", string(payload.Alert.AlertType)).
			Msg("Failed to publish alert")
		return err
	}

	processingTime := time.Since(start)
	log.Info().
		Str("camera_id", cameraID).
		Int32("track_id", detection.TrackID).
		Str("alert_type", string(payload.Alert.AlertType)).
		Str("severity", string(payload.Alert.Severity)).
		Dur("processing_time", processingTime).
		Msg("🚀 Alert published successfully")

	return nil
}

// processSuppressionDirectly processes a suppression immediately without workers
func (s *Service) processSuppressionDirectly(detection models.Detection, decision models.SuppressionDecision, cameraID string, frameData []byte, frameMetadata models.FrameMetadata) error {
	start := time.Now()

	// Use specialized build functions based on suppression type
	var payload models.SuppressionPayload

	switch decision.SuppressionType {
	case models.SuppressionTypeTrue:
		payload = suppressions.BuildTrueSuppression(detection, cameraID, frameData)

	case models.SuppressionTypeFalse:
		payload = suppressions.BuildFalseSuppression(detection, cameraID, frameData)

	default:
		log.Warn().
			Str("suppression_type", string(decision.SuppressionType)).
			Msg("Unknown suppression type")
		return fmt.Errorf("unknown suppression type: %s", decision.SuppressionType)
	}

	// Add processing metadata
	if payload.Metadata == nil {
		payload.Metadata = make(map[string]interface{})
	}
	payload.Metadata["processing_time_ms"] = time.Since(start).Milliseconds()
	payload.Metadata["frame_dimensions"] = map[string]interface{}{
		"width":  frameMetadata.Width,
		"height": frameMetadata.Height,
	}
	payload.Metadata["processing_timestamp"] = time.Now()

	// Update suppression record with frame metadata
	if payload.SuppressionRecord.FrameID == 0 {
		payload.SuppressionRecord.FrameID = frameMetadata.FrameID
	}

	// Skip payload optimization for performance (NATS limits will be increased)
	if s.cfg.ImageCompressionEnabled {
		if err := helpers.OptimizePayloadForSizeWithConfig(&payload, s.cfg); err != nil {
			log.Warn().
				Err(err).
				Str("camera_id", cameraID).
				Int32("track_id", detection.TrackID).
				Str("suppression_type", string(payload.Suppression.SuppressionType)).
				Msg("Suppression payload size validation failed, but continuing for performance")
		}
	}

	// Publish suppression
	subject := s.cfg.SuppressionSubject
	if subject == "" {
		subject = "suppressions.all"
	}

	if err := s.publisher.Publish(subject, payload); err != nil {
		log.Error().
			Err(err).
			Str("camera_id", cameraID).
			Int32("track_id", detection.TrackID).
			Str("suppression_type", string(payload.Suppression.SuppressionType)).
			Msg("Failed to publish suppression")
		return err
	}

	processingTime := time.Since(start)
	log.Info().
		Str("camera_id", cameraID).
		Int32("track_id", detection.TrackID).
		Str("suppression_type", string(payload.Suppression.SuppressionType)).
		Dur("processing_time", processingTime).
		Msg("🔄 Suppression published successfully")

	return nil
}

// getDetectionType determines the detection type based on project name and label
func (s *Service) getDetectionType(projectName, label string) models.DetectionType {
	projectLower := strings.ToLower(projectName)
	labelLower := strings.ToLower(label)

	// Project name based mapping (primary)
	switch {
	case strings.Contains(projectLower, "ppe_detection"):
		return models.DetectionTypePPE
	case strings.Contains(projectLower, "cellphone"):
		return models.DetectionTypeCellphone
	case strings.Contains(projectLower, "drone") || strings.Contains(projectLower, "aircraft"):
		return models.DetectionTypeDrone
	case strings.Contains(projectLower, "thermal_aircraft"):
		return models.DetectionTypeThermal
	case strings.Contains(projectLower, "fire_smoke"):
		return models.DetectionTypeFireSmoke
	}

	// Label based mapping (secondary)
	switch {
	case strings.Contains(labelLower, "ppe") || strings.Contains(labelLower, "helmet") || strings.Contains(labelLower, "vest"):
		return models.DetectionTypePPE
	case strings.Contains(labelLower, "phone") || strings.Contains(labelLower, "cellphone"):
		return models.DetectionTypeCellphone
	case strings.Contains(labelLower, "drone") || strings.Contains(labelLower, "aircraft") || strings.Contains(labelLower, "quadcopter"):
		return models.DetectionTypeDrone
	case strings.Contains(labelLower, "fire") || strings.Contains(labelLower, "smoke"):
		return models.DetectionTypeFireSmoke
	default:
		return models.DetectionTypeGeneral
	}
}

// findPrimaryDetection finds the detection with highest confidence in a group
func (s *Service) findPrimaryDetection(detections []models.Detection) models.Detection {
	if len(detections) == 1 {
		return detections[0]
	}

	primary := detections[0]
	for _, det := range detections[1:] {
		if det.Score > primary.Score {
			primary = det
		}
	}
	return primary
}

// CheckCooldown checks if enough time has passed since the last alert
func (s *Service) CheckCooldown(key models.AlertCooldownKey, cooldownType string) bool {
	s.cooldownMu.RLock()
	defer s.cooldownMu.RUnlock()

	var cooldownDuration time.Duration
	switch cooldownType {
	case "anomaly":
		cooldownDuration = s.anomalyCooldown
	case "self_learning":
		cooldownDuration = s.selfLearningCooldown
	case "suppression":
		cooldownDuration = s.suppressionCooldown
	default:
		cooldownDuration = s.normalCooldown
	}

	lastSent, exists := s.lastSent[key.String()]
	if !exists {
		return true
	}

	return time.Since(lastSent) >= cooldownDuration
}

// UpdateCooldown updates the last sent time for a cooldown key
func (s *Service) UpdateCooldown(key models.AlertCooldownKey, cooldownType string) {
	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()

	s.lastSent[key.String()] = time.Now()
}
