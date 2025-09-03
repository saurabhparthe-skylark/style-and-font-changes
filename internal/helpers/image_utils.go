package helpers

import (
	"encoding/base64"
	"fmt"
	"image"

	"kepler-worker-go/internal/models"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

// DefaultCropPaddingRatio is the default percentage of bbox size added as padding
const DefaultCropPaddingRatio float32 = 0.4

// CreateCroppedImageB64 creates a base64 encoded cropped image with padding using OpenCV
func CreateCroppedImageB64(frame []byte, bbox []float32, cameraID, identifier string) (string, error) {
	if len(bbox) != 4 || len(frame) == 0 {
		return "", fmt.Errorf("invalid input: bbox length=%d, frame length=%d", len(bbox), len(frame))
	}

	// Load image with OpenCV
	mat, err := gocv.IMDecode(frame, gocv.IMReadColor)
	if err != nil || mat.Empty() {
		// Try as raw BGR data
		if width, height, ok := guessDimensionsFromLength(len(frame)); ok {
			mat, err = gocv.NewMatFromBytes(height, width, gocv.MatTypeCV8UC3, frame)
			if err != nil {
				return "", fmt.Errorf("failed to load image: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to decode image")
		}
	}
	defer mat.Close()

	imgWidth := float32(mat.Cols())
	imgHeight := float32(mat.Rows())

	// Convert normalized coordinates to pixels
	var x1, y1, x2, y2 int
	if bbox[0] <= 1.0 && bbox[1] <= 1.0 && bbox[2] <= 1.0 && bbox[3] <= 1.0 {
		x1 = int(bbox[0] * imgWidth)
		y1 = int(bbox[1] * imgHeight)
		x2 = int(bbox[2] * imgWidth)
		y2 = int(bbox[3] * imgHeight)
	} else {
		x1, y1, x2, y2 = int(bbox[0]), int(bbox[1]), int(bbox[2]), int(bbox[3])
	}

	// Add padding
	bboxWidth := x2 - x1
	bboxHeight := y2 - y1
	padX := int(float32(bboxWidth) * DefaultCropPaddingRatio)
	padY := int(float32(bboxHeight) * DefaultCropPaddingRatio)

	x1 -= padX
	y1 -= padY
	x2 += padX
	y2 += padY

	// Create crop with black padding
	cropWidth := x2 - x1
	cropHeight := y2 - y1
	croppedMat := gocv.NewMatWithSize(cropHeight, cropWidth, gocv.MatTypeCV8UC3)
	defer croppedMat.Close()

	// Copy valid region
	srcX1 := max(0, x1)
	srcY1 := max(0, y1)
	srcX2 := min(int(imgWidth), x2)
	srcY2 := min(int(imgHeight), y2)

	if srcX2 > srcX1 && srcY2 > srcY1 {
		srcRect := image.Rect(srcX1, srcY1, srcX2, srcY2)
		srcRegion := mat.Region(srcRect)
		defer srcRegion.Close()

		dstX := srcX1 - x1
		dstY := srcY1 - y1
		dstRect := image.Rect(dstX, dstY, dstX+(srcX2-srcX1), dstY+(srcY2-srcY1))
		dstRegion := croppedMat.Region(dstRect)
		defer dstRegion.Close()

		srcRegion.CopyTo(&dstRegion)
	}

	// Encode to JPEG
	jpegBuf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, croppedMat, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		return "", fmt.Errorf("failed to encode image: %w", err)
	}
	defer jpegBuf.Close()

	// Return base64 data URL
	jpegBytes := jpegBuf.GetBytes()
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes), nil
}

// AddDetectionImage adds a cropped detection image to the payload
func AddDetectionImage(payload interface{}, detection models.Detection, frame []byte, cameraID, identifier string, extraMetadata map[string]interface{}) {
	croppedImageB64, err := CreateCroppedImageB64(frame, detection.BBox, cameraID, identifier)
	if err != nil {
		log.Warn().Err(err).Str("camera_id", cameraID).Msg("Failed to create detection image")
		return
	}

	if croppedImageB64 == "" {
		return
	}

	metadata := map[string]interface{}{
		"track_id":        detection.TrackID,
		"class_name":      detection.ClassName,
		"project_name":    detection.ProjectName,
		"detection_level": detection.DetectionLevel,
		"model_name":      detection.ModelName,
	}

	for k, v := range extraMetadata {
		metadata[k] = v
	}

	detectionImage := models.DetectionImage{
		CroppedImage:      croppedImageB64,
		BBoxCoordinates:   detection.BBox,
		Confidence:        detection.Score,
		DetectionMetadata: metadata,
	}

	switch p := payload.(type) {
	case *models.AlertPayload:
		p.DetectionImages = append(p.DetectionImages, detectionImage)
	case *models.SuppressionPayload:
		p.DetectionImages = append(p.DetectionImages, detectionImage)
	}
}

// AddContextImage adds a context image to the payload
func AddContextImage(payload interface{}, frame []byte, cameraID string, trackID int32, messageType string) {
	if len(frame) == 0 {
		return
	}

	// Convert to JPEG if needed
	mat, err := gocv.IMDecode(frame, gocv.IMReadColor)
	if err != nil || mat.Empty() {
		// Try as raw BGR data
		if width, height, ok := guessDimensionsFromLength(len(frame)); ok {
			mat, err = gocv.NewMatFromBytes(height, width, gocv.MatTypeCV8UC3, frame)
			if err != nil {
				return
			}
		} else {
			return
		}
	}
	defer mat.Close()

	// Encode as JPEG
	jpegBuf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		return
	}
	defer jpegBuf.Close()

	jpegBytes := jpegBuf.GetBytes()
	contextImageB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)

	switch p := payload.(type) {
	case *models.AlertPayload:
		p.ContextImage = &contextImageB64
	case *models.SuppressionPayload:
		p.ContextImage = &contextImageB64
	}
}

// CreateDetectionRecord creates a detection record
func CreateDetectionRecord(detection models.Detection) models.DetectionRecord {
	return models.DetectionRecord{
		DetectionType:         models.DetectionTypeGeneral, // Simple default
		Confidence:            detection.Score,
		TrackID:               fmt.Sprintf("%d", detection.TrackID),
		FrameTimestamp:        detection.Timestamp,
		DetectionCountInFrame: 1,
		Metadata: map[string]interface{}{
			"bbox":            detection.BBox,
			"class_name":      detection.ClassName,
			"project_name":    detection.ProjectName,
			"detection_level": detection.DetectionLevel,
			"model_name":      detection.ModelName,
		},
	}
}

// CreateSuppressionRecord creates a suppression record
func CreateSuppressionRecord(detection models.Detection, suppressionType models.SuppressionType) models.SuppressionRecord {
	record := models.SuppressionRecord{
		SuppressionType: suppressionType,
		DetectionType:   models.DetectionTypeGeneral, // Simple default
		Confidence:      detection.Score,
		TrackID:         fmt.Sprintf("%d", detection.TrackID),
		FrameTimestamp:  detection.Timestamp,
		Timestamp:       detection.Timestamp,
		Metadata: map[string]interface{}{
			"bbox":            detection.BBox,
			"class_name":      detection.ClassName,
			"project_name":    detection.ProjectName,
			"detection_level": detection.DetectionLevel,
			"model_name":      detection.ModelName,
		},
	}

	switch suppressionType {
	case models.SuppressionTypeTrue:
		record.TrueMatchID = detection.TrueMatchID
	case models.SuppressionTypeFalse:
		record.FalseMatchID = detection.FalseMatchID
	}

	return record
}

// guessDimensionsFromLength tries to infer width/height from BGR byte length
func guessDimensionsFromLength(totalBytes int) (int, int, bool) {
	if totalBytes%3 != 0 {
		return 0, 0, false
	}
	pixels := totalBytes / 3

	// Common resolutions
	common := [][2]int{
		{1920, 1080}, {1280, 720}, {1024, 768}, {800, 600}, {640, 480},
	}
	for _, wh := range common {
		if wh[0]*wh[1] == pixels {
			return wh[0], wh[1], true
		}
	}

	return 0, 0, false
}
