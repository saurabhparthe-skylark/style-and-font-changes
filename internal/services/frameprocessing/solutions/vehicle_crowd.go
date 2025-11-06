package solutions

import (
	"fmt"
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawVehicleCrowdDetection draws vehicle crowd detection overlay with beautiful visual elements
func DrawVehicleCrowdDetection(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil || solution.VehicleCrowdDetection == nil {
		return
	}

	crowdData := solution.VehicleCrowdDetection

	// Main header with beautiful styling
	headerText := "VEHICLE CROWD DETECTION"
	DrawTextEnhanced(mat, headerText, 15, *y, color.RGBA{R: 255, G: 140, B: 0, A: 255}, 0.8, 2)
	*y += 35

	// Draw individual crowd regions
	for i, crowd := range crowdData.Crowds {
		// Draw crowd bounding box with color based on alert level
		crowdColor := getCrowdColor(crowd.AlertLevel)

		if len(crowd.Rectangle) >= 4 {
			x1, y1, x2, y2 := int(crowd.Rectangle[0]), int(crowd.Rectangle[1]), int(crowd.Rectangle[2]), int(crowd.Rectangle[3])

			// Draw main bounding rectangle with thick border
			thickness := getCrowdThickness(crowd.AlertLevel)
			gocv.Rectangle(mat, image.Rect(x1, y1, x2, y2), crowdColor, thickness)

			// Draw corner highlights for better visibility
			drawCrowdCorners(mat, x1, y1, x2, y2, crowdColor)

			// Draw crowd info text inside or near the box
			crowdText := fmt.Sprintf("%s: %d", crowd.AlertLevel, crowd.Count)
			textY := y1 - 10
			if textY < 25 {
				textY = y1 + 25
			}

			// Background for crowd text
			textSize := gocv.GetTextSize(crowdText, gocv.FontHersheySimplex, 0.6, 2)
			bgRect := image.Rect(x1-5, textY-textSize.Y-5, x1+textSize.X+10, textY+5)
			gocv.Rectangle(mat, bgRect, color.RGBA{R: 0, G: 0, B: 0, A: 200}, -1)

			// Crowd text
			gocv.PutText(mat, crowdText, image.Pt(x1, textY), gocv.FontHersheySimplex, 0.6, crowdColor, 2)
		}

		// Side panel info for each crowd
		crowdInfoText := fmt.Sprintf("Area %d: %d people (%s)", i+1, crowd.Count, crowd.AlertLevel)
		textColor := getCrowdTextColor(crowd.AlertLevel)
		DrawText(mat, crowdInfoText, 15, *y, textColor)
		*y += 30
	}

	// Add spacing after crowd detection info
	*y += 15
}
