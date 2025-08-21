package alerts

import (
	"kepler-worker-go/internal/models"
)

// Placeholder for crowd detection logic. No alerts are generated currently.
// This exists for structural completeness and future extension.

// HandleCrowdDetection currently does not produce alerts
func HandleCrowdDetection(detection models.Detection, decision models.AlertDecision) models.AlertDecision {
	return decision
}
