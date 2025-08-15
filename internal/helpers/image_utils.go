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

// CreateCroppedImageB64 creates a base64 encoded cropped image from frame and bbox (optimized for performance)
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
		Msg("📸 Creating cropped image from frame")

	// Decode the image
	img, _, err := image.Decode(bytes.NewReader(frame))
	if err != nil {
		log.Debug().
			Err(err).
			Str("camera_id", cameraID).
			Msg("Failed to decode image for cropping, skipping for performance")
		return "", fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	imgWidth := float32(bounds.Max.X)
	imgHeight := float32(bounds.Max.Y)

	// Convert normalized coordinates to pixel coordinates
	// bbox format: [x_min, y_min, x_max, y_max] (normalized 0-1)
	x1 := int(bbox[0] * imgWidth)
	y1 := int(bbox[1] * imgHeight)
	x2 := int(bbox[2] * imgWidth)
	y2 := int(bbox[3] * imgHeight)

	// Ensure coordinates are within image bounds
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > int(imgWidth) {
		x2 = int(imgWidth)
	}
	if y2 > int(imgHeight) {
		y2 = int(imgHeight)
	}

	// Ensure valid crop rectangle
	if x1 >= x2 || y1 >= y2 {
		log.Debug().
			Str("camera_id", cameraID).
			Ints("crop_coords", []int{x1, y1, x2, y2}).
			Msg("Invalid crop coordinates, skipping")
		return "", fmt.Errorf("invalid crop coordinates")
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

	croppedImageB64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	log.Debug().
		Str("camera_id", cameraID).
		Str("identifier", identifier).
		Int("original_frame_size", len(frame)).
		Int("cropped_encoded_size", buf.Len()).
		Int("base64_size", len(croppedImageB64)).
		Int("crop_width", x2-x1).
		Int("crop_height", y2-y1).
		Msg("📸 Successfully created raw cropped image for performance")

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
	if frame == nil || len(frame) == 0 {
		return
	}

	if !cfg.ImageCompressionEnabled {
		// Use original frame without compression
		contextImageB64 := base64.StdEncoding.EncodeToString(frame)

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
			Msg("📸 Added uncompressed context image")
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

	contextImageB64 := base64.StdEncoding.EncodeToString(compressed)

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

// AddContextImage keeps the original interface for backward compatibility (optimized for performance)
func AddContextImage(payload interface{}, frame []byte, cameraID string, trackID int32, messageType string) {
	if frame == nil || len(frame) == 0 {
		return
	}

	// Send raw frame as base64 for maximum performance (no compression)
	contextImageB64 := base64.StdEncoding.EncodeToString(frame)

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
		Int("raw_frame_size", len(frame)).
		Int("context_image_b64_size", len(contextImageB64)).
		Str("type", messageType).
		Msg("📸 Added raw context image for performance")
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
	if frame == nil || len(frame) == 0 || len(detection.BBox) != 4 {
		return
	}

	croppedImageB64, err := CreateCroppedImageB64(frame, detection.BBox, cameraID, identifier)
	if err != nil {
		log.Warn().Err(err).Str("identifier", identifier).Msg("Failed to create cropped detection image")
		return
	}

	if croppedImageB64 == "" {
		return
	}

	// Create base detection metadata
	metadata := map[string]interface{}{
		"track_id":     detection.TrackID,
		"class_name":   detection.ClassName,
		"project_name": detection.ProjectName,
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
		p.DetectionImages = []models.DetectionImage{detectionImage}
	case *models.SuppressionPayload:
		p.DetectionImages = []models.DetectionImage{detectionImage}
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
			"bbox":         detection.BBox,
			"class_name":   detection.ClassName,
			"project_name": detection.ProjectName,
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
			"bbox":         detection.BBox,
			"class_name":   detection.ClassName,
			"project_name": detection.ProjectName,
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
