package solutions

import (
	"fmt"
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPeopleCrowdDetectionCompact draws a compact horizontal people crowd detection summary
// Returns the width of the drawn element for horizontal positioning
func DrawPeopleCrowdDetectionCompact(mat *gocv.Mat, solution models.SolutionResults, x, y int) int {
	if mat == nil || solution.CrowdDetection == nil {
		return 0
	}

	crowdData := solution.CrowdDetection
	areaCount := len(crowdData.Crowds)
	titleColor := color.RGBA{R: 255, G: 140, B: 0, A: 255} // Orange for crowd detection

	return DrawCompactCrowdDetection(mat, "CROWD DETECTION", areaCount, x, y, titleColor)
}

// DrawPeopleCrowdDetection draws people crowd detection overlay with standardized professional design
func DrawPeopleCrowdDetection(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil || solution.CrowdDetection == nil {
		return
	}

	crowdData := solution.CrowdDetection

	// Standardized panel configuration
	panelX := 15
	panelWidth := 300
	headerColor := color.RGBA{R: 255, G: 140, B: 0, A: 255} // Orange for crowd detection
	borderColor := color.RGBA{R: 60, G: 60, B: 60, A: 255}
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 220}

	// Draw panel header
	config := PanelConfig{
		X:            panelX,
		Y:            *y,
		Width:        panelWidth,
		HeaderColor:  headerColor,
		BorderColor:  borderColor,
		BgColor:      bgColor,
		HeaderHeight: 35,
		LineSpacing:  28,
	}
	*y = DrawPanelHeader(mat, "CROWD DETECTION", config)

	// Draw individual crowd regions on the video
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

		// Side panel info for each crowd - standardized format
		*y = DrawCrowdInfoItem(mat, i+1, crowd.Count, crowd.AlertLevel, panelX+8, *y, false)
	}

	// Add spacing after crowd detection info
	*y += 10
}

// DrawPeopleCrowdBoxesOnly draws only the bounding boxes for people crowd detection (no text panel)
func DrawPeopleCrowdBoxesOnly(mat *gocv.Mat, solution models.SolutionResults) {
	if mat == nil || solution.CrowdDetection == nil {
		return
	}

	crowdData := solution.CrowdDetection

	// Draw individual crowd regions on the video
	for _, crowd := range crowdData.Crowds {
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
	}
}
