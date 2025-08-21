package models

import "time"

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

// Camera represents a single camera with its pipeline
type Camera struct {
	ID        string
	URL       string
	Projects  []string
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
	RawFrames       chan *RawFrame
	ProcessedFrames chan *ProcessedFrame
	AlertFrames     chan *ProcessedFrame // New: for parallel alert processing
	RecorderFrames  chan *ProcessedFrame // New: for parallel video recording

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
	CameraID     string   `json:"camera_id" binding:"required"`
	URL          string   `json:"url" binding:"required"`
	Projects     []string `json:"projects"`
	AIEnabled    *bool    `json:"ai_enabled,omitempty"`    // Optional, defaults to config
	AIEndpoint   *string  `json:"ai_endpoint,omitempty"`   // Optional, defaults to config
	EnableRecord *bool    `json:"enable_record,omitempty"` // Whether to enable recording
}

// CameraUpsertRequest for PUT upsert operation - supports both creation and updates
type CameraUpsertRequest struct {
	URL          *string       `json:"url,omitempty"`           // RTSP URL (required for creation, optional for updates)
	Projects     []string      `json:"projects,omitempty"`      // Project names
	Status       *CameraStatus `json:"status,omitempty"`        // Camera operational status
	AIEnabled    *bool         `json:"ai_enabled,omitempty"`    // Optional, defaults to config
	AIEndpoint   *string       `json:"ai_endpoint,omitempty"`   // Optional, defaults to config
	EnableRecord *bool         `json:"enable_record,omitempty"` // Whether to enable recording
}

// CameraResponse for API
type CameraResponse struct {
	CameraID      string       `json:"camera_id"`
	URL           string       `json:"url"`
	Projects      []string     `json:"projects"`
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
}
