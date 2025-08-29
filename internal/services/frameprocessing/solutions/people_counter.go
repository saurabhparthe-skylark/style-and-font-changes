package solutions

import (
	"fmt"
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPeopleCounter draws people counting overlay with comprehensive stats
func DrawPeopleCounter(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil {
		return
	}

	// Main current count with larger text
	currentText := fmt.Sprintf("👥 Current: %d", solution.CurrentCount)
	DrawTextEnhanced(mat, currentText, 15, *y, color.RGBA{R: 0, G: 255, B: 255, A: 255}, 0.8, 2)
	*y += 35

	// Total count
	if solution.TotalCount > 0 {
		totalText := fmt.Sprintf("📊 Total: %d", solution.TotalCount)
		DrawText(mat, totalText, 15, *y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		*y += 25
	}

	// Max count
	if solution.MaxCount > 0 {
		maxText := fmt.Sprintf("📈 Peak: %d", solution.MaxCount)
		DrawText(mat, maxText, 15, *y, color.RGBA{R: 255, G: 215, B: 0, A: 255})
		*y += 25
	}

	// Out region count if applicable
	if solution.OutRegionCount > 0 {
		outText := fmt.Sprintf("🚪 Exited: %d", solution.OutRegionCount)
		DrawText(mat, outText, 15, *y, color.RGBA{R: 255, G: 165, B: 0, A: 255})
		*y += 25
	}

	// Add separator line for visual clarity
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
