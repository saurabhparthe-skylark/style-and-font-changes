package models

import (
	"time"
)

// DetectionType represents different types of detections supported
type DetectionType string

const (
	DetectionTypePPE         DetectionType = "ppe_detection_lr"
	DetectionTypeCellphone   DetectionType = "cellphone_detection"
	DetectionTypeDrone       DetectionType = "drone_detection"
	DetectionTypeAircraft    DetectionType = "aircraft_detection"
	DetectionTypeThermal     DetectionType = "thermal_aircraft_detection"
	DetectionTypeFireSmoke   DetectionType = "fire_smoke_indoor_detection"
	DetectionTypeVehicle     DetectionType = "vehicle_detection"
	DetectionTypeAnomaly     DetectionType = "anomaly_detection"
	DetectionTypeSelfLearned DetectionType = "self_learned_detection"
	DetectionTypeGeneral     DetectionType = "general_detection"
)

// DetectionLevel represents the level/priority of detection from AI
type DetectionLevel string

const (
	DetectionLevelPrimary   DetectionLevel = "primary"
	DetectionLevelSecondary DetectionLevel = "secondary"
	DetectionLevelTertiary  DetectionLevel = "tertiary"
)

// AlertType represents different types of alerts that can be generated
type AlertType string

const (
	AlertTypePPEViolation     AlertType = "PPE_VIOLATION"
	AlertTypeCellphoneUsage   AlertType = "CELLPHONE_USAGE"
	AlertTypeDroneDetection   AlertType = "DRONE_DETECTION"
	AlertTypeFireSmoke        AlertType = "FIRE_SMOKE_DETECTION"
	AlertTypeVehicleDetection AlertType = "VEHICLE_DETECTION"
	AlertTypeAnomalyDetection AlertType = "ANOMALY_DETECTION"
	AlertTypeSelfLearned      AlertType = "SELF_LEARNED_DETECTION"
	AlertTypeHighConfidence   AlertType = "HIGH_CONFIDENCE_DETECTION"
	AlertTypeIntrusion        AlertType = "INTRUSION_DETECTION"
)

// AlertSeverity represents the severity level of alerts
type AlertSeverity string

const (
	AlertSeverityLow      AlertSeverity = "LOW"
	AlertSeverityMedium   AlertSeverity = "MEDIUM"
	AlertSeverityHigh     AlertSeverity = "HIGH"
	AlertSeverityCritical AlertSeverity = "CRITICAL"
)

// Detection represents a standardized detection from the AI server
type Detection struct {
	// Core detection fields
	TrackID        int32          `json:"track_id"`
	Score          float32        `json:"score"`
	Label          string         `json:"label"`
	ClassName      string         `json:"class_name"`
	BBox           []float32      `json:"bbox"`
	Timestamp      time.Time      `json:"timestamp"`
	DetectionLevel DetectionLevel `json:"detection_level"`
	ModelName      string         `json:"model_name"`

	// Project and processing info
	ProjectName string `json:"project_name"`
	ParentID    int32  `json:"parent_id"`
	SendAlert   bool   `json:"send_alert"`
	IsRPN       bool   `json:"is_rpn"`

	// Suppression fields
	FalseMatchID   string `json:"false_match_id,omitempty"`
	TrueMatchID    string `json:"true_match_id,omitempty"`
	IsSuppressed   bool   `json:"is_suppressed"`
	TrueSuppressed bool   `json:"is_true_suppressed"`

	// PPE specific fields
	HasVest    *bool    `json:"has_vest,omitempty"`
	HasHelmet  *bool    `json:"has_helmet,omitempty"`
	HasHead    *bool    `json:"has_head,omitempty"`
	HasGloves  *bool    `json:"has_gloves,omitempty"`
	HasGlasses *bool    `json:"has_glasses,omitempty"`
	Compliance *string  `json:"compliance,omitempty"`
	Violations []string `json:"violations,omitempty"`
	PPEItems   []string `json:"ppe_items,omitempty"`

	// Fire/Smoke specific fields
	IsFire        *bool    `json:"is_fire,omitempty"`
	IsSmoke       *bool    `json:"is_smoke,omitempty"`
	FireIntensity *float32 `json:"fire_intensity,omitempty"`
	SmokeDensity  *float32 `json:"smoke_density,omitempty"`
	FireType      *string  `json:"fire_type,omitempty"`
	SmokeColor    *string  `json:"smoke_color,omitempty"`

	// Intrusion specific fields
	IsIntrusion *bool `json:"is_intrusion,omitempty"`

	// License plate specific fields
	Plate *string `json:"plate,omitempty"`

	// Color detection specific fields
	Color *string `json:"color,omitempty"`

	// Gender classification specific fields
	Gender *string `json:"gender,omitempty"`

	// Loitering specific fields
	IsLoitering  *bool    `json:"is_loitering,omitempty"`
	TimeInRegion *float32 `json:"time_in_region,omitempty"`

	// Vessel classification specific fields
	VesselType *string `json:"vessel_type,omitempty"`

	// Face recognition specific fields
	RecognitionID    *string  `json:"recognition_id,omitempty"`
	RecognitionScore *float32 `json:"recognition_score,omitempty"`
}

// FrameMetadata contains frame-level information
type FrameMetadata struct {
	FrameID     int64     `json:"frame_id"`
	Timestamp   time.Time `json:"timestamp"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	AllDetCount int       `json:"all_detections_count"`
	CameraID    string    `json:"camera_id"`
}

// AlertPayload represents the structure sent to the backend/NATS
type AlertPayload struct {
	CameraID        string                 `json:"camera_id"`
	DetectionRecord DetectionRecord        `json:"detection_record"`
	Alert           Alert                  `json:"alert"`
	ContextImage    *string                `json:"context_image,omitempty"`    // Remove for detection-only
	DetectionImages []DetectionImage       `json:"detection_images,omitempty"` // Remove for detection-only
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// DetectionRecord represents the detection data for the backend
type DetectionRecord struct {
	DetectionType         DetectionType          `json:"detection_type"`
	Confidence            float32                `json:"confidence"`
	TrackID               string                 `json:"track_id"`
	FrameID               int64                  `json:"frame_id"`
	FrameTimestamp        time.Time              `json:"frame_timestamp"`
	DetectionCountInFrame int                    `json:"detection_count_in_frame"`
	Metadata              map[string]interface{} `json:"metadata"`
}

// Alert represents the alert information
type Alert struct {
	AlertType           AlertType     `json:"alert_type"`
	Severity            AlertSeverity `json:"severity"`
	Title               string        `json:"title"`
	Description         string        `json:"description"`
	DetectionConfidence float32       `json:"detection_confidence"`
	DetectionType       string        `json:"detection_type"`
	TrackerID           int32         `json:"tracker_id"`
	ProjectName         string        `json:"project_name"`
	ModelName           string        `json:"model_name,omitempty"`
	AutoGenerated       bool          `json:"auto_generated"`
	Timestamp           time.Time     `json:"timestamp"`

	// PPE specific alert fields
	PPEViolations  []string `json:"ppe_violations,omitempty"`
	PPECompliance  *string  `json:"ppe_compliance,omitempty"`
	ViolationCount int      `json:"violation_count,omitempty"`
	ViolationTypes []string `json:"violation_types,omitempty"`

	// Fire/Smoke specific alert fields
	IsFire        *bool    `json:"is_fire,omitempty"`
	IsSmoke       *bool    `json:"is_smoke,omitempty"`
	FireIntensity *float32 `json:"fire_intensity,omitempty"`
	SmokeDensity  *float32 `json:"smoke_density,omitempty"`

	// Anomaly specific alert fields
	AnomalyLabel *string `json:"anomaly_label,omitempty"`

	// Self-learning specific alert fields
	SelfLearnedLabel *string `json:"self_learned_label,omitempty"`

	// Additional metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// DetectionImage represents detection image data (will be removed in detection-only approach)
type DetectionImage struct {
	CroppedImage      string                 `json:"cropped_image"`
	BBoxCoordinates   []float32              `json:"bbox_coordinates"`
	Confidence        float32                `json:"confidence"`
	DetectionMetadata map[string]interface{} `json:"detection_metadata"`
}

// SuppressionType represents different types of suppressions
type SuppressionType string

const (
	SuppressionTypeTrue  SuppressionType = "TRUE_POSITIVE"
	SuppressionTypeFalse SuppressionType = "FALSE_POSITIVE"
)

// SuppressionPayload represents the structure sent to the backend/NATS for suppressions
type SuppressionPayload struct {
	CameraID          string                 `json:"camera_id"`
	SuppressionRecord SuppressionRecord      `json:"suppression_record"`
	Suppression       Suppression            `json:"suppression"`
	ContextImage      *string                `json:"context_image,omitempty"`
	DetectionImages   []DetectionImage       `json:"detection_images,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// SuppressionRecord represents the suppression data for the backend
type SuppressionRecord struct {
	SuppressionType SuppressionType        `json:"suppression_type"`
	DetectionType   DetectionType          `json:"detection_type"`
	Confidence      float32                `json:"confidence"`
	TrackID         string                 `json:"track_id"`
	FrameID         int64                  `json:"frame_id"`
	FrameTimestamp  time.Time              `json:"frame_timestamp"`
	Timestamp       time.Time              `json:"timestamp"`
	TrueMatchID     string                 `json:"true_match_id,omitempty"`
	FalseMatchID    string                 `json:"false_match_id,omitempty"`
	Metadata        map[string]interface{} `json:"metadata"`
}

// Suppression represents the suppression information
type Suppression struct {
	SuppressionType     SuppressionType `json:"suppression_type"`
	DetectionConfidence float32         `json:"detection_confidence"`
	DetectionType       string          `json:"detection_type"`
	TrackerID           int32           `json:"tracker_id"`
	ProjectName         string          `json:"project_name"`
	ModelName           string          `json:"model_name,omitempty"`
	Timestamp           time.Time       `json:"timestamp"`
	CameraID            string          `json:"camera_id"`
	TrueMatchID         string          `json:"true_match_id,omitempty"`
	FalseMatchID        string          `json:"false_match_id,omitempty"`
}

// SuppressionDecision represents the decision whether to create a suppression
type SuppressionDecision struct {
	ShouldSuppress  bool
	SuppressionType SuppressionType
	TrueMatchID     string
	FalseMatchID    string
	Metadata        map[string]interface{}
}

// AlertCooldownKey represents a unique key for alert cooldown tracking
type AlertCooldownKey struct {
	CameraID    string
	ProjectName string
	TrackID     string
}

// String returns a string representation of the cooldown key
func (k AlertCooldownKey) String() string {
	return k.CameraID + "|" + k.ProjectName + "|" + k.TrackID
}

// AlertDecision represents the decision whether to create an alert
type AlertDecision struct {
	ShouldAlert  bool
	AlertType    AlertType
	Severity     AlertSeverity
	Title        string
	Description  string
	CooldownType string // "normal", "anomaly", "self_learning"
	Metadata     map[string]interface{}
}

// ProcessedDetections represents the result of processing detections for alerts
type ProcessedDetections struct {
	TotalDetections      int
	ValidDetections      int
	SuppressedDetections int
	AlertsCreated        int
	Errors               []string
}

// DetectionProcessor interface defines the contract for processing detections
type DetectionProcessor interface {
	ProcessDetectionsForAlerts(cameraID string, detections []Detection, frameMetadata FrameMetadata) ProcessedDetections
	ShouldCreateAlert(detection Detection, projectName string) AlertDecision
	BuildAlert(detection Detection, decision AlertDecision, frameMetadata FrameMetadata) Alert
	CheckCooldown(key AlertCooldownKey, cooldownType string) bool
	UpdateCooldown(key AlertCooldownKey, cooldownType string)
}

// MessagePublisher interface for publishing alerts
type MessagePublisher interface {
	Publish(subject string, data interface{}) error
}

// SolutionResults represents the results from AI solution processing
type SolutionResults struct {
	CurrentCount      int32 `json:"current_count"`
	TotalCount        int32 `json:"total_count"`
	MaxCount          int32 `json:"max_count"`
	OutRegionCount    int32 `json:"out_region_count"`
	ViolationDetected *bool `json:"violation_detected,omitempty"`
	IntrusionDetected *bool `json:"intrusion_detected,omitempty"`
}

// AIProcessingResult represents the complete result from AI processing
type AIProcessingResult struct {
	Detections     []Detection                `json:"detections"`
	Solutions      map[string]SolutionResults `json:"solutions"`
	ProcessingTime time.Duration              `json:"processing_time"`
	ErrorMessage   string                     `json:"error_message,omitempty"`
	FrameProcessed bool                       `json:"frame_processed"`
	ProjectResults map[string]int             `json:"project_results"`
}
