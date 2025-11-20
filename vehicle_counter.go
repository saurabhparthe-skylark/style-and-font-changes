package solutions

import (
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawVehicleCounter draws vehicle counting overlay in compact horizontal format
// Returns the width of the drawn counter
func DrawVehicleCounter(header string, mat *gocv.Mat, solution models.SolutionResults, x, y int) int {
	if mat == nil {
		return 0
	}

	// Professional color scheme - remove DRONE/CCTV prefix
	titleColor := color.RGBA{R: 50, G: 205, B: 50, A: 255} // Professional lime green for vehicles
	currentColor := getVehicleCountColor(solution.VehicleCurrentCount)
	totalColor := color.RGBA{R: 255, G: 223, B: 0, A: 255} // Professional gold/amber for total

	// Draw compact horizontal counter (removed header prefix)
	title := "VEHICLE COUNTER"
	return DrawCompactCounter(mat, title, solution.VehicleCurrentCount, solution.VehicleTotalCount,
		x, y, titleColor, currentColor, totalColor)
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
