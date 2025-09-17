package models

import (
	"encoding/json"
	"time"
)

// CameraStatus represents the camera operational status
type CameraStatus string

const (
	CameraStatusStart  CameraStatus = "start"
	CameraStatusStop   CameraStatus = "stop"
	CameraStatusPaused CameraStatus = "paused"
)

// String returns the string representation of CameraStatus
func (cs CameraStatus) String() string {
	return string(cs)
}

// IsValid checks if the camera status is valid
func (cs CameraStatus) IsValid() bool {
	switch cs {
	case CameraStatusStart, CameraStatusStop, CameraStatusPaused:
		return true
	default:
		return false
	}
}

// ROICoordinates represents ROI (Region of Interest) coordinates
type ROICoordinates struct {
	XMin  float64 `json:"x_min"`           // Normalized coordinate 0.0-1.0
	YMin  float64 `json:"y_min"`           // Normalized coordinate 0.0-1.0
	XMax  float64 `json:"x_max"`           // Normalized coordinate 0.0-1.0
	YMax  float64 `json:"y_max"`           // Normalized coordinate 0.0-1.0
	Color *string `json:"color,omitempty"` // Hex color for ROI display (optional)
}

// CameraSolution represents an AI solution configuration for the camera
type CameraSolution struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectName string `json:"projectName"`
}

// Camera represents a single camera with its pipeline
type Camera struct {
	ID        string
	URL       string
	IsActive  bool
	CreatedAt time.Time

	// Camera Status and Control
	Status       CameraStatus // Camera operational status
	IsRecording  bool
	IsPaused     bool
	EnableRecord bool // Whether recording is enabled for this camera

	// AI Configuration (per-camera)
	AIEnabled  bool
	AIEndpoint string
	AITimeout  time.Duration

	// Camera Configuration and ROI Data
	Config          json.RawMessage            `json:"config,omitempty"`   // Camera-specific configuration as JSON
	CameraSolutions []CameraSolution           `json:"camera_solutions"`   // List of enabled AI solutions
	Projects        []string                   `json:"projects"`           // List of enabled AI solutions
	ROIData         map[string]*ROICoordinates `json:"roi_data,omitempty"` // ROI coordinates per solution ID

	// Statistics
	FrameCount    int64
	ErrorCount    int64
	LastFrameTime time.Time
	FPS           float64
	Latency       time.Duration

	// FPS Calculation (rolling window)
	RecentFrameTimes []time.Time // Keep last N frame timestamps for accurate FPS
	FPSWindowSize    int         // Number of frames to use for FPS calculation

	// AI Processing Stats
	AIProcessingTime time.Duration
	LastAIError      string
	AIDetectionCount int64
	AIFrameCounter   int64 // Counter for Nth frame processing

	// Pipeline channels
	RawFrames            chan *RawFrame
	ProcessedFrames      chan *ProcessedFrame
	PostProcessingFrames chan *ProcessedFrame // New: for parallel post processing
	RecorderFrames       chan *ProcessedFrame // New: for parallel video recording

	// Control
	StopChannel chan struct{}

	// URLs
	RTSPUrl   string
	WebRTCUrl string
	HLSUrl    string
	MJPEGUrl  string
}

// RawFrame represents a frame from OpenCV
type RawFrame struct {
	CameraID  string
	Data      []byte
	Timestamp time.Time
	FrameID   int64
	Width     int
	Height    int
	Format    string
}

// ProcessedFrame represents a frame after AI processing and metadata overlay
type ProcessedFrame struct {
	CameraID  string
	Data      []byte // Annotated frame data (with overlays, bounding boxes, stats)
	RawData   []byte // Original raw frame data (without any overlays) - for clean crops
	Timestamp time.Time
	FrameID   int64
	Width     int
	Height    int

	// Metadata
	FPS          float64
	Latency      time.Duration
	AIEnabled    bool
	AIDetections interface{} // Will contain detection results

	// Quality
	Quality int
	Bitrate int
}

// CameraRequest for API
type CameraRequest struct {
	CameraID        string                     `json:"camera_id" binding:"required"`
	URL             string                     `json:"url" binding:"required"`
	AIEnabled       *bool                      `json:"ai_enabled,omitempty"`       // Optional, defaults to config
	AIEndpoint      *string                    `json:"ai_endpoint,omitempty"`      // Optional, defaults to config
	EnableRecord    *bool                      `json:"enable_record,omitempty"`    // Whether to enable recording
	Config          json.RawMessage            `json:"config,omitempty"`           // Camera-specific configuration as JSON
	CameraSolutions []CameraSolution           `json:"camera_solutions,omitempty"` // List of enabled AI solutions
	ROIData         map[string]*ROICoordinates `json:"roi_data,omitempty"`         // ROI coordinates per solution ID
}

// CameraUpsertRequest for PUT upsert operation - supports both creation and updates
type CameraUpsertRequest struct {
	URL             *string                    `json:"url,omitempty"`              // RTSP URL (required for creation, optional for updates)
	Status          *CameraStatus              `json:"status,omitempty"`           // Camera operational status
	AIEnabled       *bool                      `json:"ai_enabled,omitempty"`       // Optional, defaults to config
	AIEndpoint      *string                    `json:"ai_endpoint,omitempty"`      // Optional, defaults to config
	EnableRecord    *bool                      `json:"enable_record,omitempty"`    // Whether to enable recording
	Config          json.RawMessage            `json:"config,omitempty"`           // Camera-specific configuration as JSON
	CameraSolutions []CameraSolution           `json:"camera_solutions,omitempty"` // List of enabled AI solutions
	ROIData         map[string]*ROICoordinates `json:"roi_data,omitempty"`         // ROI coordinates per solution ID
}

// CameraResponse for API
type CameraResponse struct {
	CameraID      string       `json:"camera_id"`
	URL           string       `json:"url"`
	IsActive      bool         `json:"is_active"`
	Status        CameraStatus `json:"status"`        // Camera operational status
	IsRecording   bool         `json:"is_recording"`  // Current recording state
	IsPaused      bool         `json:"is_paused"`     // Current pause state
	EnableRecord  bool         `json:"enable_record"` // Whether recording is enabled
	CreatedAt     time.Time    `json:"created_at"`
	LastFrameTime time.Time    `json:"last_frame_time"`
	FrameCount    int64        `json:"frame_count"`
	ErrorCount    int64        `json:"error_count"`
	FPS           float64      `json:"fps"`
	Latency       string       `json:"latency"`

	// AI Configuration
	AIEnabled        bool   `json:"ai_enabled"`
	AIEndpoint       string `json:"ai_endpoint"`
	AITimeout        string `json:"ai_timeout"`
	AIProcessingTime string `json:"ai_processing_time"`
	LastAIError      string `json:"last_ai_error,omitempty"`
	AIDetectionCount int64  `json:"ai_detection_count"`

	// Streaming URLs
	RTSPUrl   string `json:"rtsp_url"`
	WebRTCUrl string `json:"webrtc_url"`
	HLSUrl    string `json:"hls_url"`
	MJPEGUrl  string `json:"mjpeg_url"`

	// Camera Configuration and ROI Data
	Config          json.RawMessage            `json:"config,omitempty"`   // Camera-specific configuration as JSON
	CameraSolutions []CameraSolution           `json:"camera_solutions"`   // List of enabled AI solutions
	ROIData         map[string]*ROICoordinates `json:"roi_data,omitempty"` // ROI coordinates per solution ID
}

// RTSPCheckRequest for checking RTSP stream
type RTSPCheckRequest struct {
	URL string `json:"url" binding:"required"`
}

// RTSPCheckResponse for RTSP stream check result
type RTSPCheckResponse struct {
	Valid       bool    `json:"valid"`
	Message     string  `json:"message"`
	Thumbnail   string  `json:"thumbnail,omitempty"`    // Base64 encoded HD thumbnail
	Width       int     `json:"width,omitempty"`        // Stream width
	Height      int     `json:"height,omitempty"`       // Stream height
	FPS         float64 `json:"fps,omitempty"`          // Stream FPS
	ErrorDetail string  `json:"error_detail,omitempty"` // Detailed error if validation fails
}
