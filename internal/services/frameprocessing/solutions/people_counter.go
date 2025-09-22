package solutions

import (
	"fmt"
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPeopleCounter draws people counting overlay with beautiful design
func DrawPeopleCounter(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil {
		return
	}

	// Title header with beautiful styling
	headerText := "PEOPLE COUNTER"
	DrawTextEnhanced(mat, headerText, 15, *y, color.RGBA{R: 0, G: 255, B: 255, A: 255}, 0.8, 2)
	*y += 35

	// Current count with enhanced styling and icon-like prefix
	currentText := fmt.Sprintf("Current: %d", solution.PeopleCurrentCount)
	currentColor := getPeopleCountColor(solution.PeopleCurrentCount)
	DrawTextEnhanced(mat, currentText, 15, *y, currentColor, 0.75, 2)
	// *y += 32

	// Peak count with golden color
	// maxText := fmt.Sprintf("Peak: %d", solution.PeopleMaxCount)
	// DrawTextEnhanced(mat, maxText, 15, *y, color.RGBA{R: 255, G: 215, B: 0, A: 255}, 0.7, 2)
	// *y += 30

	// Add some spacing after people counter
	*y += 10
}

// DrawText helper function to draw text with background
func DrawText(mat *gocv.Mat, text string, x, y int, textColor color.RGBA) {
	DrawTextEnhanced(mat, text, x, y, textColor, 0.7, 2)
}

// DrawTextEnhanced draws text with customizable font scale and thickness
func DrawTextEnhanced(mat *gocv.Mat, text string, x, y int, textColor color.RGBA, fontScale float64, thickness int) {
	fontFace := gocv.FontHersheySimplex
	textSize := gocv.GetTextSize(text, fontFace, fontScale, thickness)

	// Enhanced background with rounded corners effect
	padding := 8
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 200}

	// Main background rectangle
	bgRect := image.Rect(x-padding, y-textSize.Y-padding, x+textSize.X+padding, y+padding)
	gocv.Rectangle(mat, bgRect, bgColor, -1)

	// Add subtle border for better definition
	borderColor := color.RGBA{R: 40, G: 40, B: 40, A: 255}
	gocv.Rectangle(mat, bgRect, borderColor, 1)

	// Text with slight shadow effect for better readability
	shadowColor := color.RGBA{R: 0, G: 0, B: 0, A: 100}
	gocv.PutText(mat, text, image.Pt(x+1, y+1), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, text, image.Pt(x, y), fontFace, fontScale, textColor, thickness)
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
