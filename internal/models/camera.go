package models

import "time"

// Camera represents a single camera with its pipeline
type Camera struct {
	ID        string
	URL       string
	Projects  []string
	IsActive  bool
	CreatedAt time.Time

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

	// AI Processing Stats
	AIProcessingTime time.Duration
	LastAIError      string
	AIDetectionCount int64

	// Pipeline channels
	RawFrames       chan *RawFrame
	ProcessedFrames chan *ProcessedFrame

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
	CameraID   string   `json:"camera_id" binding:"required"`
	URL        string   `json:"url" binding:"required"`
	Projects   []string `json:"projects"`
	AIEnabled  *bool    `json:"ai_enabled,omitempty"`  // Optional, defaults to config
	AIEndpoint *string  `json:"ai_endpoint,omitempty"` // Optional, defaults to config
	AITimeout  *string  `json:"ai_timeout,omitempty"`  // Optional, e.g., "5s"
}

// CameraResponse for API
type CameraResponse struct {
	CameraID      string    `json:"camera_id"`
	URL           string    `json:"url"`
	Projects      []string  `json:"projects"`
	IsActive      bool      `json:"is_active"`
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
