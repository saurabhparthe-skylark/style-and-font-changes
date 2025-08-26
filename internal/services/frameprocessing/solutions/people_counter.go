package solutions

import (
	"fmt"
	"image"
	"image/color"

	"kepler-worker-go/internal/models"

	"gocv.io/x/gocv"
)

// DrawPeopleCounter draws people counting overlay
func DrawPeopleCounter(mat *gocv.Mat, solution models.SolutionResults, y *int) {
	if mat == nil {
		return
	}

	text := fmt.Sprintf("People: %d", solution.CurrentCount)
	DrawText(mat, text, 15, *y, color.RGBA{R: 0, G: 255, B: 255, A: 255})
	*y += 30
}

// DrawText helper function to draw text with background
func DrawText(mat *gocv.Mat, text string, x, y int, textColor color.RGBA) {
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.7
	thickness := 2

	textSize := gocv.GetTextSize(text, fontFace, fontScale, thickness)
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180}

	// Background rectangle
	gocv.Rectangle(mat, image.Rect(x-5, y-textSize.Y-5, x+textSize.X+5, y+5), bgColor, -1)

	// Text
	gocv.PutText(mat, text, image.Pt(x, y), fontFace, fontScale, textColor, thickness)
}
