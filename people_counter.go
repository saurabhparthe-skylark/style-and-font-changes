package solutions

import (
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPeopleCounter draws people counting overlay in compact horizontal format
// Returns the width of the drawn counter
func DrawPeopleCounter(header string, mat *gocv.Mat, solution models.SolutionResults, x, y int) int {
	if mat == nil {
		return 0
	}

	// Professional color scheme - remove DRONE/CCTV prefix
	titleColor := color.RGBA{R: 64, G: 224, B: 208, A: 255} // Professional turquoise/cyan for people
	currentColor := getPeopleCountColor(solution.PeopleCurrentCount)
	totalColor := color.RGBA{R: 255, G: 223, B: 0, A: 255} // Professional gold/amber for total

	// Draw compact horizontal counter (removed header prefix)
	title := "PEOPLE COUNTER"
	return DrawCompactCounter(mat, title, solution.PeopleCurrentCount, solution.PeopleTotalCount,
		x, y, titleColor, currentColor, totalColor)
}

// getPeopleCountColor returns dynamic color based on people count
func getPeopleCountColor(count int32) color.RGBA {
	switch {
	case count == 0:
		return color.RGBA{R: 128, G: 128, B: 128, A: 255} // Gray for no people
	case count <= 5:
		return color.RGBA{R: 0, G: 255, B: 0, A: 255} // Green for low count
	case count <= 15:
		return color.RGBA{R: 0, G: 255, B: 255, A: 255} // Cyan for medium count
	case count <= 25:
		return color.RGBA{R: 255, G: 165, B: 0, A: 255} // Orange for high count
	default:
		return color.RGBA{R: 255, G: 0, B: 0, A: 255} // Red for very high count
	}
}
