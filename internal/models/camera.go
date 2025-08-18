package models

import "time"

// Camera represents a single camera with its pipeline
type Camera struct {
	ID        string
	URL       string
	Projects  []string
	IsActive  bool
	CreatedAt time.Time

	// Camera Status and Control
	Status       string // "start", "stop", "paused"
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
	Data      []byte
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
	EnableRecord *bool    `json:"enable_record,omitempty"` // Whether to enable recording
	Status       *string  `json:"status,omitempty"`        // "start", "stop", "paused"
	AIEnabled    *bool    `json:"ai_enabled,omitempty"`    // Optional, defaults to config
	AIEndpoint   *string  `json:"ai_endpoint,omitempty"`   // Optional, defaults to config
	AITimeout    *string  `json:"ai_timeout,omitempty"`    // Optional, e.g., "5s"
}

// CameraResponse for API
type CameraResponse struct {
	CameraID      string    `json:"camera_id"`
	URL           string    `json:"url"`
	Projects      []string  `json:"projects"`
	IsActive      bool      `json:"is_active"`
	Status        string    `json:"status"`        // "start", "stop", "paused"
	IsRecording   bool      `json:"is_recording"`  // Current recording state
	IsPaused      bool      `json:"is_paused"`     // Current pause state
	EnableRecord  bool      `json:"enable_record"` // Whether recording is enabled
	CreatedAt     time.Time `json:"created_at"`
	LastFrameTime time.Time `json:"last_frame_time"`
	FrameCount    int64     `json:"frame_count"`
	ErrorCount    int64     `json:"error_count"`
	FPS           float64   `json:"fps"`
	Latency       string    `json:"latency"`

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

// AIConfigRequest for updating camera AI configuration (duplicate from handlers, but needed here)
type AIConfigRequest struct {
	AIEnabled  *bool    `json:"ai_enabled,omitempty"`
	AIEndpoint *string  `json:"ai_endpoint,omitempty"`
	AITimeout  *string  `json:"ai_timeout,omitempty"`
	Projects   []string `json:"projects,omitempty"`
}

// AIConfigResponse for returning camera AI configuration
type AIConfigResponse struct {
	CameraID         string   `json:"camera_id"`
	AIEnabled        bool     `json:"ai_enabled"`
	AIEndpoint       string   `json:"ai_endpoint"`
	AITimeout        string   `json:"ai_timeout"`
	Projects         []string `json:"projects"`
	AIProcessingTime string   `json:"ai_processing_time"`
	LastAIError      string   `json:"last_ai_error,omitempty"`
	AIDetectionCount int64    `json:"ai_detection_count"`
}

// CameraUpdateRequest for updating camera settings dynamically
type CameraUpdateRequest struct {
	URL          *string  `json:"url,omitempty"`           // Update RTSP URL
	xProjects    []string `json:"projects,omitempty"`      // Update projects
	AIEnabled    *bool    `json:"ai_enabled,omitempty"`    // Update AI settings
	AIEndpoint   *string  `json:"ai_endpoint,omitempty"`   // Update AI endpoint
	AITimeout    *string  `json:"ai_timeout,omitempty"`    // Update AI timeout
	EnableRecord *bool    `json:"enable_record,omitempty"` // Enable/disable recording
	Status       *string  `json:"status,omitempty"`        // Update status: "start", "stop", "paused"
}
