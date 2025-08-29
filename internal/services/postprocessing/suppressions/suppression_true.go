package suppressions

import (
	"fmt"
	"strings"
	"time"

	"kepler-worker-go/internal/helpers"
	"kepler-worker-go/internal/models"

	"github.com/rs/zerolog/log"
)

// HandleTrueSuppression processes true positive suppressions and returns decision
func HandleTrueSuppression(detection models.Detection, cameraID string) models.SuppressionDecision {
	// Extract label from true_match_id (split by _ and take first part)
	trueLabel := detection.TrueMatchID
	if strings.Contains(trueLabel, "_") {
		trueLabel = strings.Split(detection.TrueMatchID, "_")[0]
	}

	log.Info().
		Str("camera_id", cameraID).
		Str("true_match_id", detection.TrueMatchID).
		Str("extracted_label", trueLabel).
		Int32("track_id", detection.TrackID).
		Msg("🟢 Processing true suppression")

	// Create suppression decision
	decision := models.SuppressionDecision{
		ShouldSuppress:  true,
		SuppressionType: models.SuppressionTypeTrue,
		TrueMatchID:     detection.TrueMatchID,
		Metadata: map[string]interface{}{
			"true_match_label": trueLabel,
			"extracted_label":  trueLabel,
		},
	}

	return decision
}

// BuildTrueSuppression creates a complete true suppression payload with images
func BuildTrueSuppression(detection models.Detection, cameraID string, frame []byte) models.SuppressionPayload {
	log.Info().
		Str("camera_id", cameraID).
		Int32("track_id", detection.TrackID).
		Str("true_match_id", detection.TrueMatchID).
		Msg("🏗️ Building true suppression with images")

	// Extract label from true_match_id
	trueLabel := detection.TrueMatchID
	if strings.Contains(trueLabel, "_") {
		trueLabel = strings.Split(detection.TrueMatchID, "_")[0]
	}

	// Create metadata
	metadata := map[string]interface{}{
		"true_match_label": trueLabel,
		"extracted_label":  trueLabel,
		"suppression_type": "true_positive",
	}

	// Build suppression payload with images using helpers
	payload := models.SuppressionPayload{
		CameraID:          cameraID,
		SuppressionRecord: helpers.CreateSuppressionRecord(detection, models.SuppressionTypeTrue),
		Suppression: models.Suppression{
			SuppressionType:     models.SuppressionTypeTrue,
			DetectionConfidence: detection.Score,
			DetectionType:       detection.ClassName,
			TrackerID:           detection.TrackID,
			ProjectName:         detection.ProjectName,
			ModelName:           detection.ModelName,
			Timestamp:           time.Now(),
			CameraID:            cameraID,
			TrueMatchID:         detection.TrueMatchID,
		},
		Metadata: metadata,
	}

	// Add context image using helper
	helpers.AddContextImage(&payload, frame, cameraID, detection.TrackID, "true suppression")

	// Add detection image using helper with true suppression specific metadata
	helpers.AddDetectionImage(&payload, detection, frame, cameraID, fmt.Sprintf("true_suppression_%d", detection.TrackID), map[string]interface{}{
		"suppression_type": "true_positive",
		"true_match_id":    detection.TrueMatchID,
	})

	return payload
}
