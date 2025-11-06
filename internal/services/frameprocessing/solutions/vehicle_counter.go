package solutions

import (
	"fmt"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawVehicleCounter draws vehicle counting overlay with beautiful design
func DrawVehicleCounter(header string, mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil {
		return
	}

	// Title header with beautiful styling
	headerText := header + " VEHICLE COUNTER"
	DrawTextEnhanced(mat, headerText, 15, *y, color.RGBA{R: 0, G: 255, B: 255, A: 255}, 0.8, 2)
	*y += 35

	// Current count with enhanced styling and icon-like prefix
	currentText := fmt.Sprintf("Current: %d", solution.VehicleCurrentCount)
	currentColor := getVehicleCountColor(solution.VehicleCurrentCount)
	DrawTextEnhanced(mat, currentText, 15, *y, currentColor, 0.75, 2)
	*y += 32

	// Total count with golden color
	totalText := fmt.Sprintf("Total: %d", solution.VehicleTotalCount)
	DrawTextEnhanced(mat, totalText, 15, *y, color.RGBA{R: 255, G: 215, B: 0, A: 255}, 0.7, 2)
	*y += 30

	// Add some spacing after people counter
	*y += 10
}

// getVehicleCountColor returns dynamic color based on vehicle count
func getVehicleCountColor(count int32) color.RGBA {
	switch {
	case count == 0:
		return color.RGBA{R: 128, G: 128, B: 128, A: 255} // Gray for no vehicles
	case count <= 5:
		return color.RGBA{R: 0, G: 255, B: 0, A: 255} // Green for low count
	default:
		return color.RGBA{R: 255, G: 0, B: 0, A: 255} // Red for very high count
	}
}
