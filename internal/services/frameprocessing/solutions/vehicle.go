package solutions

import (
	"fmt"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawVehicle draws vehicle detection overlay
func DrawVehicle(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil {
		return
	}

	text := fmt.Sprintf("Vehicles: %d", solution.CurrentCount)
	DrawText(mat, text, 15, *y, color.RGBA{R: 255, G: 255, B: 0, A: 255})
	*y += 30
}
