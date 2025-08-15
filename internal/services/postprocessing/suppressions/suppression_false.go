package suppressions

import (
	"fmt"
	"time"

	"kepler-worker-go/internal/helpers"
	"kepler-worker-go/internal/models"

	"github.com/rs/zerolog/log"
)

// HandleFalseSuppression processes false positive suppressions and returns decision
func HandleFalseSuppression(detection models.Detection, cameraID string) models.SuppressionDecision {
	log.Info().
		Str("camera_id", cameraID).
		Str("false_match_id", detection.FalseMatchID).
		Int32("track_id", detection.TrackID).
		Msg("🔴 Processing false suppression")

	// Create suppression decision
	decision := models.SuppressionDecision{
		ShouldSuppress:  true,
		SuppressionType: models.SuppressionTypeFalse,
		FalseMatchID:    detection.FalseMatchID,
		Metadata: map[string]interface{}{
			"false_match_id":   detection.FalseMatchID,
			"suppression_type": "false_positive",
			"timestamp":        time.Now().Format(time.RFC3339),
		},
	}

	return decision
}

// BuildFalseSuppression creates a complete false suppression payload with images
func BuildFalseSuppression(detection models.Detection, cameraID string, frame []byte) models.SuppressionPayload {
	log.Info().
		Str("camera_id", cameraID).
		Int32("track_id", detection.TrackID).
		Str("false_match_id", detection.FalseMatchID).
		Msg("🏗️ Building false suppression with images")

	// Create metadata
	metadata := map[string]interface{}{
		"false_match_id":   detection.FalseMatchID,
		"suppression_type": "false_positive",
		"timestamp":        time.Now().Format(time.RFC3339),
	}

	// Build suppression payload with images using helpers
	payload := models.SuppressionPayload{
		CameraID:          cameraID,
		SuppressionRecord: helpers.CreateSuppressionRecord(detection, models.SuppressionTypeFalse),
		Suppression: models.Suppression{
			SuppressionType:     models.SuppressionTypeFalse,
			DetectionConfidence: detection.Score,
			DetectionType:       detection.ClassName,
			TrackerID:           detection.TrackID,
			ProjectName:         detection.ProjectName,
			Timestamp:           time.Now(),
			CameraID:            cameraID,
			FalseMatchID:        detection.FalseMatchID,
		},
		Metadata: metadata,
	}

	// Add context image using helper
	helpers.AddContextImage(&payload, frame, cameraID, detection.TrackID, "false suppression")

	// Add detection image using helper with false suppression specific metadata
	helpers.AddDetectionImage(&payload, detection, frame, cameraID, fmt.Sprintf("false_suppression_%d", detection.TrackID), map[string]interface{}{
		"suppression_type": "false_positive",
		"false_match_id":   detection.FalseMatchID,
	})

	return payload
}
