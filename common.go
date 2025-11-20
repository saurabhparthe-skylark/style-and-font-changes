package solutions

import (
	"fmt"
	"image"
	"image/color"

	"gocv.io/x/gocv"
)

// PanelConfig holds configuration for drawing professional panels
type PanelConfig struct {
	X            int
	Y            int
	Width        int
	HeaderColor  color.RGBA
	BorderColor  color.RGBA
	BgColor      color.RGBA
	HeaderHeight int
	LineSpacing  int
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

// DrawPanelHeader draws a professional panel header with title
func DrawPanelHeader(mat *gocv.Mat, title string, config PanelConfig) int {
	if mat == nil {
		return config.Y
	}

	fontFace := gocv.FontHersheySimplex
	fontScale := 0.75
	thickness := 2
	textSize := gocv.GetTextSize(title, fontFace, fontScale, thickness)

	// Draw header background
	headerRect := image.Rect(config.X, config.Y, config.X+config.Width, config.Y+config.HeaderHeight)
	gocv.Rectangle(mat, headerRect, config.HeaderColor, -1)

	// Draw header border
	gocv.Rectangle(mat, headerRect, config.BorderColor, 2)

	// Draw header text (centered)
	textX := config.X + (config.Width-textSize.X)/2
	textY := config.Y + textSize.Y + (config.HeaderHeight-textSize.Y)/2
	textColor := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	gocv.PutText(mat, title, image.Pt(textX, textY), fontFace, fontScale, textColor, thickness)

	return config.Y + config.HeaderHeight
}

// DrawPanelItem draws a single item within a panel (label: value format)
func DrawPanelItem(mat *gocv.Mat, label string, value string, x, y int, labelColor, valueColor color.RGBA, fontScale float64) int {
	if mat == nil {
		return y
	}

	fontFace := gocv.FontHersheySimplex
	thickness := 2

	// Draw label
	labelText := label + ":"
	labelSize := gocv.GetTextSize(labelText, fontFace, fontScale, thickness)
	gocv.PutText(mat, labelText, image.Pt(x, y), fontFace, fontScale, labelColor, thickness)

	// Draw value (with spacing)
	valueX := x + labelSize.X + 8
	gocv.PutText(mat, value, image.Pt(valueX, y), fontFace, fontScale, valueColor, thickness)

	// Return next Y position
	return y + int(float64(labelSize.Y)*1.5)
}

// DrawMetricCard draws a compact metric card (for counters) - DEPRECATED, use DrawCompactCounter instead
func DrawMetricCard(mat *gocv.Mat, title string, currentValue int32, totalValue int32, x, y int, titleColor, currentColor, totalColor color.RGBA) int {
	if mat == nil {
		return y
	}

	fontFace := gocv.FontHersheySimplex
	cardPadding := 10
	lineHeight := 28

	// Calculate card dimensions
	titleSize := gocv.GetTextSize(title, fontFace, 0.7, 2)
	currentText := fmt.Sprintf("Current: %d", currentValue)
	currentSize := gocv.GetTextSize(currentText, fontFace, 0.65, 2)
	totalText := fmt.Sprintf("Total: %d", totalValue)
	totalSize := gocv.GetTextSize(totalText, fontFace, 0.65, 2)

	cardWidth := max(titleSize.X, max(currentSize.X, totalSize.X)) + cardPadding*2
	cardHeight := lineHeight*3 + cardPadding*2

	// Draw card background
	cardRect := image.Rect(x, y, x+cardWidth, y+cardHeight)
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 220}
	gocv.Rectangle(mat, cardRect, bgColor, -1)

	// Draw card border
	borderColor := color.RGBA{R: 60, G: 60, B: 60, A: 255}
	gocv.Rectangle(mat, cardRect, borderColor, 1)

	// Draw title
	titleY := y + cardPadding + titleSize.Y
	gocv.PutText(mat, title, image.Pt(x+cardPadding, titleY), fontFace, 0.7, titleColor, 2)

	// Draw current value
	currentY := titleY + lineHeight
	gocv.PutText(mat, currentText, image.Pt(x+cardPadding, currentY), fontFace, 0.65, currentColor, 2)

	// Draw total value
	totalY := currentY + lineHeight
	gocv.PutText(mat, totalText, image.Pt(x+cardPadding, totalY), fontFace, 0.65, totalColor, 2)

	return y + cardHeight + 8
}

// DrawCompactCounter draws a professional horizontal counter in one line with industry-standard styling
// Format: "TITLE | Crr: X | T: Y"
// Returns the width of the drawn counter for horizontal positioning
func DrawCompactCounter(mat *gocv.Mat, title string, currentValue int32, totalValue int32, x, y int, titleColor, currentColor, totalColor color.RGBA) int {
	if mat == nil {
		return 0
	}

	// Industry-standard font settings: larger, bolder, more readable
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.65 // Increased from 0.45 for better readability
	thickness := 2    // Thicker for better visibility
	padding := 10     // Increased padding for better spacing
	spacing := 10     // Increased spacing between elements

	// Calculate individual text sizes to ensure proper width
	titleSize := gocv.GetTextSize(title, fontFace, fontScale, thickness)
	currentText := fmt.Sprintf("Curr: %d", currentValue)
	currentSize := gocv.GetTextSize(currentText, fontFace, fontScale, thickness)
	totalText := fmt.Sprintf("T: %d", totalValue)
	totalSize := gocv.GetTextSize(totalText, fontFace, fontScale, thickness)
	separator := "|"
	sepSize := gocv.GetTextSize(separator, fontFace, fontScale, thickness)

	// Calculate total width needed (title + 2 separators + current + total + spacing between all)
	totalWidth := titleSize.X + (sepSize.X * 2) + currentSize.X + totalSize.X + (spacing * 4) + (padding * 2)
	textHeight := titleSize.Y

	// Draw professional background with better styling
	bgRect := image.Rect(x, y-textHeight-padding, x+totalWidth, y+padding)

	// Dark semi-transparent background for better contrast
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 240} // More opaque for better readability
	gocv.Rectangle(mat, bgRect, bgColor, -1)

	// Draw subtle inner border for depth
	innerBorderColor := color.RGBA{R: 40, G: 40, B: 40, A: 255}
	gocv.Rectangle(mat, bgRect, innerBorderColor, 1)

	// Draw outer border with accent color
	borderColor := color.RGBA{R: 80, G: 80, B: 80, A: 255}
	gocv.Rectangle(mat, image.Rect(x-1, y-textHeight-padding-1, x+totalWidth+1, y+padding+1), borderColor, 1)

	// Draw text parts with different colors and shadow for depth
	textX := x + padding
	textY := y

	// Draw title with subtle shadow for depth
	shadowOffset := 1
	shadowColor := color.RGBA{R: 0, G: 0, B: 0, A: 150}
	gocv.PutText(mat, title, image.Pt(textX+shadowOffset, textY+shadowOffset), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, title, image.Pt(textX, textY), fontFace, fontScale, titleColor, thickness)
	textX += titleSize.X + spacing

	// Draw separator with better styling
	sepColor := color.RGBA{R: 120, G: 120, B: 120, A: 255} // More visible separator
	gocv.PutText(mat, separator, image.Pt(textX, textY), fontFace, fontScale, sepColor, thickness)
	textX += sepSize.X + spacing

	// Draw current value with shadow
	gocv.PutText(mat, currentText, image.Pt(textX+shadowOffset, textY+shadowOffset), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, currentText, image.Pt(textX, textY), fontFace, fontScale, currentColor, thickness)
	textX += currentSize.X + spacing

	// Draw separator
	gocv.PutText(mat, separator, image.Pt(textX, textY), fontFace, fontScale, sepColor, thickness)
	textX += sepSize.X + spacing

	// Draw total value with shadow
	gocv.PutText(mat, totalText, image.Pt(textX+shadowOffset, textY+shadowOffset), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, totalText, image.Pt(textX, textY), fontFace, fontScale, totalColor, thickness)

	// Return the width of the counter for horizontal positioning
	return totalWidth
}

// DrawCompactCrowdDetection draws a professional horizontal crowd detection summary with industry-standard styling
// Format: "TITLE | Areas: X"
// Returns the width of the drawn element for horizontal positioning
func DrawCompactCrowdDetection(mat *gocv.Mat, title string, areaCount int, x, y int, titleColor color.RGBA) int {
	if mat == nil {
		return 0
	}

	// Industry-standard font settings: larger, bolder, more readable
	fontFace := gocv.FontHersheySimplex
	fontScale := 0.65 // Increased from 0.45 for better readability
	thickness := 2    // Thicker for better visibility
	padding := 10     // Increased padding for better spacing
	spacing := 10     // Increased spacing between elements

	// Calculate individual text sizes to ensure proper width
	titleSize := gocv.GetTextSize(title, fontFace, fontScale, thickness)
	areaText := fmt.Sprintf("Areas: %d", areaCount)
	areaSize := gocv.GetTextSize(areaText, fontFace, fontScale, thickness)
	separator := "|"
	sepSize := gocv.GetTextSize(separator, fontFace, fontScale, thickness)

	// Calculate total width needed (title + separator + area + spacing + padding)
	totalWidth := titleSize.X + sepSize.X + areaSize.X + (spacing * 2) + (padding * 2)
	textHeight := titleSize.Y

	// Draw professional background with better styling
	bgRect := image.Rect(x, y-textHeight-padding, x+totalWidth, y+padding)

	// Dark semi-transparent background for better contrast
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 240} // More opaque for better readability
	gocv.Rectangle(mat, bgRect, bgColor, -1)

	// Draw subtle inner border for depth
	innerBorderColor := color.RGBA{R: 40, G: 40, B: 40, A: 255}
	gocv.Rectangle(mat, bgRect, innerBorderColor, 1)

	// Draw outer border with accent color
	borderColor := color.RGBA{R: 80, G: 80, B: 80, A: 255}
	gocv.Rectangle(mat, image.Rect(x-1, y-textHeight-padding-1, x+totalWidth+1, y+padding+1), borderColor, 1)

	// Draw text parts with different colors and shadow for depth
	textX := x + padding
	textY := y
	shadowOffset := 1
	shadowColor := color.RGBA{R: 0, G: 0, B: 0, A: 150}

	// Draw title with shadow
	gocv.PutText(mat, title, image.Pt(textX+shadowOffset, textY+shadowOffset), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, title, image.Pt(textX, textY), fontFace, fontScale, titleColor, thickness)
	textX += titleSize.X + spacing

	// Draw separator with better styling
	sepColor := color.RGBA{R: 120, G: 120, B: 120, A: 255} // More visible separator
	gocv.PutText(mat, separator, image.Pt(textX, textY), fontFace, fontScale, sepColor, thickness)
	textX += sepSize.X + spacing

	// Draw area count with shadow
	areaColor := color.RGBA{R: 255, G: 165, B: 0, A: 255} // Brighter orange for better visibility
	gocv.PutText(mat, areaText, image.Pt(textX+shadowOffset, textY+shadowOffset), fontFace, fontScale, shadowColor, thickness)
	gocv.PutText(mat, areaText, image.Pt(textX, textY), fontFace, fontScale, areaColor, thickness)

	// Return the width of the element for horizontal positioning
	return totalWidth
}

// DrawCrowdInfoItem draws a crowd detection info item in a standardized format with compact font
func DrawCrowdInfoItem(mat *gocv.Mat, areaNum int, count int32, alertLevel string, x, y int, isVehicle bool) int {
	if mat == nil {
		return y
	}

	fontFace := gocv.FontHersheySimplex
	fontScale := 0.5 // Smaller font for compact display
	thickness := 1

	// Determine object type
	objectType := "people"
	if isVehicle {
		objectType = "vehicles"
	}

	// Format text
	alertText := fmt.Sprintf("Area %d: %d %s (%s)", areaNum, count, objectType, alertLevel)
	textColor := getCrowdTextColor(alertLevel)

	// Draw with background using smaller font
	DrawTextEnhanced(mat, alertText, x, y, textColor, fontScale, thickness)

	// Return next Y position
	textSize := gocv.GetTextSize(alertText, fontFace, fontScale, thickness)
	return y + textSize.Y + 6 // Reduced spacing
}

// Helper function for max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
