package solutions

import (
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPPE draws PPE violation overlay
func DrawPPE(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil || solution.ViolationDetected == nil {
		return
	}

	var text string
	var textColor color.RGBA

	if *solution.ViolationDetected {
		text = "⚠️ PPE VIOLATION"
		textColor = color.RGBA{R: 255, G: 165, B: 0, A: 255}
	} else {
		text = "✓ PPE Compliant"
		textColor = color.RGBA{R: 0, G: 255, B: 0, A: 255}
	}

	DrawText(mat, text, 15, *y, textColor)
	*y += 30
}
