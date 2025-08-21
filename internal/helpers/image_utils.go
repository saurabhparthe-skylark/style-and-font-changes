package helpers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"strings"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"

	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

const (
	// Maximum image dimensions for compression
	MaxImageWidth  = 800
	MaxImageHeight = 600

	// JPEG quality settings
	HighQuality   = 95
	MediumQuality = 75
	LowQuality    = 50

	// Maximum payload sizes (in bytes)
	MaxContextImageSize   = 500 * 1024  // 500KB for context images
	MaxDetectionImageSize = 200 * 1024  // 200KB for detection images
	MaxTotalPayloadSize   = 1024 * 1024 // 1MB total payload limit
)

// Helper functions
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isJPEGData checks if the byte slice contains JPEG data by checking magic bytes
func isJPEGData(data []byte) bool {
	if len(data) < 2 {
		return false
	}
	// JPEG magic bytes: FF D8
	return data[0] == 0xFF && data[1] == 0xD8
}

// convertBGRToJPEG safely converts BGR raw bytes to JPEG format
func convertBGRToJPEG(bgrData []byte, width, height int, quality int) ([]byte, error) {
	if len(bgrData) == 0 {
		return nil, fmt.Errorf("empty BGR data")
	}

	// Auto-detect dimensions if provided values don't match data length
	totalBytes := len(bgrData)
	if width <= 0 || height <= 0 || width*height*3 != totalBytes {
		if w, h, ok := guessDimensionsFromLength(totalBytes); ok {
			width, height = w, h
		} else {
			return nil, fmt.Errorf("unable to infer frame dimensions from BGR length=%d", totalBytes)
		}
	}

	// Create Mat from BGR bytes
	mat, err := gocv.NewMatFromBytes(height, width, gocv.MatTypeCV8UC3, bgrData)
	if err != nil {
		return nil, fmt.Errorf("failed to create Mat from BGR data: %w", err)
	}
	defer mat.Close()

	// Encode as JPEG
	jpegBuf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, mat, []int{gocv.IMWriteJpegQuality, quality})
	if err != nil {
		return nil, fmt.Errorf("failed to encode BGR as JPEG: %w", err)
	}
	defer jpegBuf.Close()

	return jpegBuf.GetBytes(), nil
}

// convertFrameToJPEG intelligently converts frame data to JPEG format
func convertFrameToJPEG(frameData []byte, width, height int, quality int) ([]byte, error) {
	if len(frameData) == 0 {
		return nil, fmt.Errorf("empty frame data")
	}

	// Check if data is already JPEG
	if isJPEGData(frameData) {
		return frameData, nil
	}

	// Assume BGR format and convert (auto-detect if needed)
	return convertBGRToJPEG(frameData, width, height, quality)
}

// guessDimensionsFromLength tries to infer width/height from BGR byte length
func guessDimensionsFromLength(totalBytes int) (int, int, bool) {
	if totalBytes%3 != 0 {
		return 0, 0, false
	}
	pixels := totalBytes / 3

	// Common resolutions to try (width x height)
	common := [][2]int{
		{3840, 2160}, {2560, 1440}, {2560, 1600}, {2048, 1080},
		{1920, 1080}, {1600, 900}, {1366, 768}, {1280, 960},
		{1280, 800}, {1280, 720}, {1024, 768}, {1024, 576},
		{854, 480}, {800, 600}, {800, 450}, {768, 432},
		{720, 480}, {720, 405}, {640, 480}, {640, 360},
		{480, 360}, {480, 270}, {426, 240}, {320, 240},
	}
	for _, wh := range common {
		if wh[0]*wh[1] == pixels {
			return wh[0], wh[1], true
		}
	}

	// Fallback: try factorization with plausible widths
	plausible := []int{1920, 1600, 1366, 1280, 1024, 960, 854, 800, 768, 720, 640, 480, 426, 400, 384, 352, 320}
	for _, w := range plausible {
		if pixels%w == 0 {
			h := pixels / w
			if h > 0 {
				return w, h, true
			}
		}
	}

	return 0, 0, false
}

// CompressAndResizeImage compresses and resizes an image to fit within size limits
func CompressAndResizeImage(img image.Image, maxWidth, maxHeight int, targetSizeBytes int) ([]byte, error) {
	// Calculate resize dimensions while maintaining aspect ratio
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Calculate scale factor
	scaleX := float64(maxWidth) / float64(width)
	scaleY := float64(maxHeight) / float64(height)
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	// Don't upscale images
	if scale > 1.0 {
		scale = 1.0
	}

	newWidth := int(float64(width) * scale)
	newHeight := int(float64(height) * scale)

	// Create resized image if needed
	if scale < 1.0 {
		resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		for y := 0; y < newHeight; y++ {
			for x := 0; x < newWidth; x++ {
				srcX := int(float64(x) / scale)
				srcY := int(float64(y) / scale)
				resized.Set(x, y, img.At(srcX, srcY))
			}
		}
		img = resized
	}

	// Try different quality levels to meet target size
	qualities := []int{HighQuality, MediumQuality, LowQuality}

	for _, quality := range qualities {
		var buf bytes.Buffer
		err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
		if err != nil {
			continue
		}

		// If within target size or this is the lowest quality, use it
		if buf.Len() <= targetSizeBytes || quality == LowQuality {
			log.Debug().
				Int("original_size", width*height*3). // Rough estimate
				Int("compressed_size", buf.Len()).
				Int("quality", quality).
				Int("width", newWidth).
				Int("height", newHeight).
				Msg("🗜️ Image compressed successfully")
			return buf.Bytes(), nil
		}
	}

	return nil, fmt.Errorf("unable to compress image to target size")
}

// CreateCroppedImageB64 creates a base64 encoded cropped image from frame data and bbox (supports both JPEG and BGR)
func CreateCroppedImageB64(frame []byte, bbox []float32, cameraID, identifier string) (string, error) {
	if len(bbox) != 4 {
		return "", fmt.Errorf("invalid bbox length: %d", len(bbox))
	}

	if len(frame) == 0 {
		log.Debug().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Msg("📸 No frame data available for cropping")
		return "", nil
	}

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Floats32("bbox", bbox).
		Msg("📸 Creating cropped image from frame data")

	// Try to decode as image first (works for JPEG data)
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		// If decoding fails, assume it's BGR data and convert to JPEG first
		log.Debug().
			Str("camera_id", cameraID).
			Msg("Frame data is not in image format, converting BGR to JPEG")

		// Try to auto-detect dimensions for BGR conversion
		jpegData, convertErr := convertBGRToJPEG(frame, 0, 0, 95)
		if convertErr != nil {
			log.Warn().
				Err(convertErr).
				Str("camera_id", cameraID).
				Msg("Failed to convert BGR to JPEG for cropping")
			return "", fmt.Errorf("failed to convert BGR to JPEG: %w", convertErr)
		}

		// Now decode the converted JPEG
		img, _, err = image.Decode(bytes.NewReader(jpegData))
		if err != nil {
			log.Debug().
				Err(err).
				Str("camera_id", cameraID).
				Msg("Failed to decode converted JPEG for cropping")
			return "", fmt.Errorf("failed to decode converted JPEG: %w", err)
		}
	}

	bounds := img.Bounds()
	imgWidth := float32(bounds.Max.X)
	imgHeight := float32(bounds.Max.Y)

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Floats32("raw_bbox", bbox).
		Float32("img_width", imgWidth).
		Float32("img_height", imgHeight).
		Msg("📸 Processing bbox coordinates")

	var x1, y1, x2, y2 int

	// Detect if coordinates are normalized (0-1) or already in pixel coordinates
	if bbox[0] <= 1.0 && bbox[1] <= 1.0 && bbox[2] <= 1.0 && bbox[3] <= 1.0 {
		// Normalized coordinates (0-1) - convert to pixels
		x1 = int(bbox[0] * imgWidth)
		y1 = int(bbox[1] * imgHeight)
		x2 = int(bbox[2] * imgWidth)
		y2 = int(bbox[3] * imgHeight)

		log.Debug().
			Str("camera_id", cameraID).
			Msg("📸 Using normalized coordinates (0-1)")
	} else {
		// Already pixel coordinates - use directly
		x1 = int(bbox[0])
		y1 = int(bbox[1])
		x2 = int(bbox[2])
		y2 = int(bbox[3])

		log.Debug().
			Str("camera_id", cameraID).
			Msg("📸 Using pixel coordinates directly")
	}

	// Ensure coordinates are within image bounds
	x1 = max(0, min(int(imgWidth)-1, x1))
	y1 = max(0, min(int(imgHeight)-1, y1))
	x2 = max(0, min(int(imgWidth), x2))
	y2 = max(0, min(int(imgHeight), y2))

	// Ensure minimum crop size (at least 10x10 pixels for visibility)
	minCropSize := 10
	cropWidth := x2 - x1
	cropHeight := y2 - y1

	if cropWidth < minCropSize {
		// Expand width around center
		center := (x1 + x2) / 2
		x1 = max(0, center-minCropSize/2)
		x2 = min(int(imgWidth), x1+minCropSize)

		// If still too small, adjust
		if x2-x1 < minCropSize {
			x2 = min(int(imgWidth), x1+minCropSize)
		}
	}

	if cropHeight < minCropSize {
		// Expand height around center
		center := (y1 + y2) / 2
		y1 = max(0, center-minCropSize/2)
		y2 = min(int(imgHeight), y1+minCropSize)

		// If still too small, adjust
		if y2-y1 < minCropSize {
			y2 = min(int(imgHeight), y1+minCropSize)
		}
	}

	// Final bounds check
	if x2 > int(imgWidth) {
		x2 = int(imgWidth)
	}
	if y2 > int(imgHeight) {
		y2 = int(imgHeight)
	}

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Ints("final_coords", []int{x1, y1, x2, y2}).
		Int("crop_width", x2-x1).
		Int("crop_height", y2-y1).
		Msg("📸 Final crop coordinates")

	// Validate crop rectangle
	if x1 >= x2 || y1 >= y2 || x2-x1 < 1 || y2-y1 < 1 {
		log.Warn().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Floats32("original_bbox", bbox).
			Ints("calculated_coords", []int{x1, y1, x2, y2}).
			Float32("img_width", imgWidth).
			Float32("img_height", imgHeight).
			Msg("📸 Invalid crop coordinates after processing")
		return "", fmt.Errorf("invalid crop coordinates: [%d,%d,%d,%d] from bbox [%.3f,%.3f,%.3f,%.3f] on image %dx%d",
			x1, y1, x2, y2, bbox[0], bbox[1], bbox[2], bbox[3], int(imgWidth), int(imgHeight))
	}

	// Create cropped image
	cropRect := image.Rect(x1, y1, x2, y2)
	croppedImg := image.NewRGBA(cropRect)

	// Copy the cropped portion
	for y := y1; y < y2; y++ {
		for x := x1; x < x2; x++ {
			croppedImg.Set(x, y, img.At(x, y))
		}
	}

	// Encode cropped image directly without compression for performance
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, croppedImg, &jpeg.Options{Quality: 95}) // High quality, no compression
	if err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cameraID).
			Msg("Failed to encode cropped image")
		return "", fmt.Errorf("failed to encode cropped image: %w", err)
	}

	// Create data URL format for proper client consumption
	croppedImageB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Int("original_frame_size", len(frame)).
		Int("cropped_encoded_size", buf.Len()).
		Int("base64_size", len(croppedImageB64)).
		Int("crop_width", x2-x1).
		Int("crop_height", y2-y1).
		Msg("📸 Successfully created cropped image from frame data")

	return croppedImageB64, nil
}

// GetDetectionTypeFromLabel maps detection labels to DetectionType enum
func GetDetectionTypeFromLabel(label string) models.DetectionType {
	labelLower := strings.ToLower(label)
	switch {
	case strings.Contains(labelLower, "person") || strings.Contains(labelLower, "ppe"):
		return models.DetectionTypePPE
	case strings.Contains(labelLower, "phone") || strings.Contains(labelLower, "cell"):
		return models.DetectionTypeCellphone
	case strings.Contains(labelLower, "drone") || strings.Contains(labelLower, "aircraft"):
		return models.DetectionTypeDrone
	case strings.Contains(labelLower, "fire") || strings.Contains(labelLower, "smoke"):
		return models.DetectionTypeFireSmoke
	default:
		return models.DetectionTypeGeneral
	}
}

// AddContextImageWithConfig adds a compressed base64 encoded context image to the payload using configuration
func AddContextImageWithConfig(payload interface{}, frame []byte, cameraID string, trackID int32, messageType string, cfg *config.Config) {
	if len(frame) == 0 {
		return
	}

	if !cfg.ImageCompressionEnabled {
		// Convert frame to JPEG format if needed
		jpegData, err := convertFrameToJPEG(frame, 1280, 720, 95)
		if err != nil {
			log.Warn().
				Err(err).
				Str("camera_id", cameraID).
				Msg("Failed to convert frame to JPEG for context image, skipping")
			return
		}

		// Create data URL format for proper client consumption
		contextImageB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData)

		switch p := payload.(type) {
		case *models.AlertPayload:
			p.ContextImage = &contextImageB64
		case *models.SuppressionPayload:
			p.ContextImage = &contextImageB64
		}

		log.Debug().
			Str("camera_id", cameraID).
			Int32("track_id", trackID).
			Str("type", messageType).
			Int("original_frame_size", len(frame)).
			Int("jpeg_size", len(jpegData)).
			Int("base64_size", len(contextImageB64)).
			Msg("📸 Added context image with proper JPEG encoding")
		return
	}

	// Decode the original frame
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cameraID).
			Msg("Failed to decode frame for context image, skipping")
		return
	}

	// Compress the context image to reduce payload size
	compressed, err := CompressAndResizeImageWithConfig(img, cfg.MaxImageWidth, cfg.MaxImageHeight, cfg.MaxContextImageSize, cfg.ImageQuality)
	if err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cameraID).
			Msg("Failed to compress context image, skipping")
		return
	}

	// Create data URL format for proper client consumption
	contextImageB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(compressed)

	// Handle both AlertPayload and SuppressionPayload
	switch p := payload.(type) {
	case *models.AlertPayload:
		p.ContextImage = &contextImageB64
	case *models.SuppressionPayload:
		p.ContextImage = &contextImageB64
	}

	log.Debug().
		Str("camera_id", cameraID).
		Int32("track_id", trackID).
		Int("original_frame_size", len(frame)).
		Int("compressed_size", len(compressed)).
		Int("context_image_b64_size", len(contextImageB64)).
		Str("type", messageType).
		Msg("📸 Added compressed context image")
}

// AddContextImage keeps the original interface for backward compatibility (handles both JPEG and BGR)
func AddContextImage(payload interface{}, frame []byte, cameraID string, trackID int32, messageType string) {
	if len(frame) == 0 {
		return
	}

	// Convert frame to JPEG format if needed (handles both JPEG and BGR)
	jpegData, err := convertFrameToJPEG(frame, 1280, 720, 95)
	if err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cameraID).
			Msg("Failed to convert frame to JPEG for context image, skipping")
		return
	}

	// Create data URL format for proper client consumption
	contextImageB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData)

	// Handle both AlertPayload and SuppressionPayload
	switch p := payload.(type) {
	case *models.AlertPayload:
		p.ContextImage = &contextImageB64
	case *models.SuppressionPayload:
		p.ContextImage = &contextImageB64
	}

	log.Debug().
		Str("camera_id", cameraID).
		Int32("track_id", trackID).
		Int("original_frame_size", len(frame)).
		Int("jpeg_size", len(jpegData)).
		Int("context_image_b64_size", len(contextImageB64)).
		Str("type", messageType).
		Msg("📸 Added context image with proper JPEG encoding")
}

// CompressAndResizeImageWithConfig compresses and resizes an image using configuration
func CompressAndResizeImageWithConfig(img image.Image, maxWidth, maxHeight int, targetSizeBytes int, quality int) ([]byte, error) {
	// Calculate resize dimensions while maintaining aspect ratio
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Calculate scale factor
	scaleX := float64(maxWidth) / float64(width)
	scaleY := float64(maxHeight) / float64(height)
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	// Don't upscale images
	if scale > 1.0 {
		scale = 1.0
	}

	newWidth := int(float64(width) * scale)
	newHeight := int(float64(height) * scale)

	// Create resized image if needed
	if scale < 1.0 {
		resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		for y := 0; y < newHeight; y++ {
			for x := 0; x < newWidth; x++ {
				srcX := int(float64(x) / scale)
				srcY := int(float64(y) / scale)
				resized.Set(x, y, img.At(srcX, srcY))
			}
		}
		img = resized
	}

	// Try different quality levels to meet target size, starting with configured quality
	qualities := []int{quality, quality - 25, quality - 50}
	if quality < 25 {
		qualities = []int{quality, 25, 50} // Fallback for very low quality settings
	}

	for _, q := range qualities {
		if q < 1 {
			q = 1
		}
		if q > 100 {
			q = 100
		}

		var buf bytes.Buffer
		err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
		if err != nil {
			continue
		}

		// If within target size or this is the lowest quality, use it
		if buf.Len() <= targetSizeBytes || q == qualities[len(qualities)-1] {
			log.Debug().
				Int("original_size", width*height*3). // Rough estimate
				Int("compressed_size", buf.Len()).
				Int("quality", q).
				Int("width", newWidth).
				Int("height", newHeight).
				Msg("🗜️ Image compressed successfully")
			return buf.Bytes(), nil
		}
	}

	return nil, fmt.Errorf("unable to compress image to target size")
}

// AddDetectionImage adds a cropped detection image to the payload
func AddDetectionImage(payload interface{}, detection models.Detection, frame []byte, cameraID, identifier string, extraMetadata map[string]interface{}) {
	if len(frame) == 0 {
		return
	}

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Int("frame_size", len(frame)).
		Int("detection_image_size", len(detection.BBox)).
		Msg("📸 Adding detection image")

	// Validate detection bounding box
	if len(detection.BBox) != 4 {
		log.Warn().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Int("bbox_length", len(detection.BBox)).
			Msg("📸 Invalid detection bbox length, skipping detection image")
		return
	}

	// Validate bounding box values
	bbox := detection.BBox
	if bbox[0] < 0 || bbox[1] < 0 || bbox[2] < 0 || bbox[3] < 0 {
		log.Warn().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Floats32("bbox", bbox).
			Msg("📸 Negative bbox coordinates, skipping detection image")
		return
	}

	// Check for zero-size bounding box
	if (bbox[2] <= bbox[0]) || (bbox[3] <= bbox[1]) {
		log.Warn().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Floats32("bbox", bbox).
			Msg("📸 Zero or negative size bbox, skipping detection image")
		return
	}

	croppedImageB64, err := CreateCroppedImageB64(frame, detection.BBox, cameraID, identifier)
	if err != nil {
		log.Warn().
			Err(err).
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Floats32("bbox", detection.BBox).
			Int32("track_id", detection.TrackID).
			Str("class_name", detection.ClassName).
			Msg("📸 Failed to create cropped detection image")
		return
	}

	if croppedImageB64 == "" {
		log.Debug().
			Str("camera_id", cameraID).
			Str("identifier", identifier).
			Msg("📸 No cropped image data returned")
		return
	}

	// Create base detection metadata
	metadata := map[string]interface{}{
		"track_id":        detection.TrackID,
		"class_name":      detection.ClassName,
		"project_name":    detection.ProjectName,
		"detection_level": detection.DetectionLevel,
	}

	// Add extra metadata
	for k, v := range extraMetadata {
		metadata[k] = v
	}

	detectionImage := models.DetectionImage{
		CroppedImage:      croppedImageB64,
		BBoxCoordinates:   detection.BBox,
		Confidence:        detection.Score,
		DetectionMetadata: metadata,
	}

	// Handle both AlertPayload and SuppressionPayload
	switch p := payload.(type) {
	case *models.AlertPayload:
		p.DetectionImages = append(p.DetectionImages, detectionImage)
	case *models.SuppressionPayload:
		p.DetectionImages = append(p.DetectionImages, detectionImage)
	}

	log.Debug().
		Str("camera_id", cameraID).
		Int32("track_id", detection.TrackID).
		Int("detection_image_size", len(croppedImageB64)).
		Str("identifier", identifier).
		Msg("📸 Added detection image")
}

// CreateDetectionRecord creates a standardized DetectionRecord
func CreateDetectionRecord(detection models.Detection) models.DetectionRecord {
	return models.DetectionRecord{
		DetectionType:         GetDetectionTypeFromLabel(detection.Label),
		Confidence:            detection.Score,
		TrackID:               fmt.Sprintf("%d", detection.TrackID),
		FrameTimestamp:        detection.Timestamp,
		DetectionCountInFrame: 1, // Will be updated by service
		Metadata: map[string]interface{}{
			"bbox":            detection.BBox,
			"class_name":      detection.ClassName,
			"project_name":    detection.ProjectName,
			"detection_level": detection.DetectionLevel,
		},
	}
}

// CreateSuppressionRecord creates a standardized SuppressionRecord
func CreateSuppressionRecord(detection models.Detection, suppressionType models.SuppressionType) models.SuppressionRecord {
	record := models.SuppressionRecord{
		SuppressionType: suppressionType,
		DetectionType:   GetDetectionTypeFromLabel(detection.Label),
		Confidence:      detection.Score,
		TrackID:         fmt.Sprintf("%d", detection.TrackID),
		FrameTimestamp:  detection.Timestamp,
		Timestamp:       detection.Timestamp, // You might want to use time.Now() here
		Metadata: map[string]interface{}{
			"bbox":            detection.BBox,
			"class_name":      detection.ClassName,
			"project_name":    detection.ProjectName,
			"detection_level": detection.DetectionLevel,
		},
	}

	// Add suppression-specific fields based on type
	switch suppressionType {
	case models.SuppressionTypeTrue:
		record.TrueMatchID = detection.TrueMatchID
	case models.SuppressionTypeFalse:
		record.FalseMatchID = detection.FalseMatchID
	}

	return record
}

// ValidatePayloadSizeWithConfig checks if a payload exceeds the maximum allowed size using configuration
func ValidatePayloadSizeWithConfig(payload interface{}, cfg *config.Config) error {
	var totalSize int

	switch p := payload.(type) {
	case *models.AlertPayload:
		if p.ContextImage != nil {
			totalSize += len(*p.ContextImage)
		}
		for _, img := range p.DetectionImages {
			totalSize += len(img.CroppedImage)
		}
	case *models.SuppressionPayload:
		if p.ContextImage != nil {
			totalSize += len(*p.ContextImage)
		}
		for _, img := range p.DetectionImages {
			totalSize += len(img.CroppedImage)
		}
	default:
		return fmt.Errorf("unsupported payload type")
	}

	if totalSize > cfg.MaxTotalPayloadSize {
		return fmt.Errorf("payload size (%d bytes) exceeds maximum allowed size (%d bytes)", totalSize, cfg.MaxTotalPayloadSize)
	}

	log.Debug().
		Int("payload_size_bytes", totalSize).
		Int("max_allowed_bytes", cfg.MaxTotalPayloadSize).
		Float64("size_percentage", float64(totalSize)/float64(cfg.MaxTotalPayloadSize)*100).
		Msg("📏 Payload size validation passed")

	return nil
}

// OptimizePayloadForSizeWithConfig removes or compresses images further if payload is too large using configuration
func OptimizePayloadForSizeWithConfig(payload interface{}, cfg *config.Config) error {
	// First check if optimization is needed
	if err := ValidatePayloadSizeWithConfig(payload, cfg); err == nil {
		return nil // Already within limits
	}

	log.Warn().Msg("🔧 Payload too large, optimizing by removing context image")

	// Remove context image first (usually the largest component)
	switch p := payload.(type) {
	case *models.AlertPayload:
		if p.ContextImage != nil {
			p.ContextImage = nil
			log.Debug().Msg("📸 Removed context image to reduce payload size")
		}
	case *models.SuppressionPayload:
		if p.ContextImage != nil {
			p.ContextImage = nil
			log.Debug().Msg("📸 Removed context image to reduce payload size")
		}
	}

	// Check again after removing context image
	if err := ValidatePayloadSizeWithConfig(payload, cfg); err == nil {
		return nil
	}

	// If still too large, remove detection images
	log.Warn().Msg("🔧 Still too large, removing detection images")
	switch p := payload.(type) {
	case *models.AlertPayload:
		p.DetectionImages = nil
	case *models.SuppressionPayload:
		p.DetectionImages = nil
	}

	// Final validation
	return ValidatePayloadSizeWithConfig(payload, cfg)
}

// Keep the original functions for backward compatibility
func ValidatePayloadSize(payload interface{}) error {
	defaultConfig := &config.Config{
		MaxTotalPayloadSize: 1024 * 1024, // 1MB
	}
	return ValidatePayloadSizeWithConfig(payload, defaultConfig)
}

func OptimizePayloadForSize(payload interface{}) error {
	defaultConfig := &config.Config{
		MaxTotalPayloadSize: 1024 * 1024, // 1MB
	}
	return OptimizePayloadForSizeWithConfig(payload, defaultConfig)
}
