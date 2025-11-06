package solutions

import (
	"image"
	"image/color"

	"gocv.io/x/gocv"
)

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

// getCrowdColor returns color based on alert level
func getCrowdColor(alertLevel string) color.RGBA {
	switch alertLevel {
	case "high":
		return color.RGBA{R: 255, G: 0, B: 0, A: 255} // Red
	case "medium":
		return color.RGBA{R: 255, G: 165, B: 0, A: 255} // Orange
	case "low":
		return color.RGBA{R: 255, G: 255, B: 0, A: 255} // Yellow
	default:
		return color.RGBA{R: 0, G: 255, B: 0, A: 255} // Green
	}
}

// getCrowdTextColor returns text color based on alert level
func getCrowdTextColor(alertLevel string) color.RGBA {
	switch alertLevel {
	case "high":
		return color.RGBA{R: 255, G: 50, B: 50, A: 255} // Light Red
	case "medium":
		return color.RGBA{R: 255, G: 200, B: 50, A: 255} // Light Orange
	case "low":
		return color.RGBA{R: 255, G: 255, B: 100, A: 255} // Light Yellow
	default:
		return color.RGBA{R: 100, G: 255, B: 100, A: 255} // Light Green
	}
}

// getCrowdThickness returns border thickness based on alert level
func getCrowdThickness(alertLevel string) int {
	switch alertLevel {
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	default:
		return 2
	}
}

// drawCrowdCorners draws corner highlights for better visibility
func drawCrowdCorners(mat *gocv.Mat, x1, y1, x2, y2 int, crowdColor color.RGBA) {
	cornerLength := 20
	cornerThickness := 3

	// Top-left corner
	gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1+cornerLength, y1), crowdColor, cornerThickness)
	gocv.Line(mat, image.Pt(x1, y1), image.Pt(x1, y1+cornerLength), crowdColor, cornerThickness)

	// Top-right corner
	gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2-cornerLength, y1), crowdColor, cornerThickness)
	gocv.Line(mat, image.Pt(x2, y1), image.Pt(x2, y1+cornerLength), crowdColor, cornerThickness)

	// Bottom-left corner
	gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1+cornerLength, y2), crowdColor, cornerThickness)
	gocv.Line(mat, image.Pt(x1, y2), image.Pt(x1, y2-cornerLength), crowdColor, cornerThickness)

	// Bottom-right corner
	gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2-cornerLength, y2), crowdColor, cornerThickness)
	gocv.Line(mat, image.Pt(x2, y2), image.Pt(x2, y2-cornerLength), crowdColor, cornerThickness)
}
