package solutions

import (
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawIntrusion draws intrusion detection overlay
func DrawIntrusion(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil || solution.IntrusionDetected == nil {
		return
	}

	var text string
	var textColor color.RGBA

	if *solution.IntrusionDetected {
		text = "⚠️ INTRUSION DETECTED"
		textColor = color.RGBA{R: 255, G: 0, B: 0, A: 255}
		// Draw red border
		gocv.Rectangle(mat, image.Rect(0, 0, mat.Cols(), mat.Rows()), textColor, 5)
	} else {
		text = "✓ Area Secure"
		textColor = color.RGBA{R: 0, G: 255, B: 0, A: 255}
	}

	DrawText(mat, text, 15, *y, textColor)
	*y += 30
}
