package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type Config struct {
	// Application
	Version     string
	Environment string
	WorkerID    string
	Port        int
	LogLevel    string

	// Logdy (lightweight web log viewer)
	LogdyEnabled bool
	LogdyHost    string
	LogdyPort    int

	// External Services
	MediaMTXURL string
	BackendURL  string
	AIGRPCURL   string

	// NATS (for messaging and alerts)
	// Default: nats://localhost:4222 (works with Docker Compose setup)
	// Docker: Use nats://nats:4222 if running worker in Docker
	NatsURL            string
	NatsConnectTimeout time.Duration
	NatsReconnectWait  time.Duration
	NatsMaxReconnects  int
	NatsDrainTimeout   time.Duration // For graceful shutdown

	// Streaming Configuration
	RTSPTimeout       time.Duration
	MaxRetries        int
	MaxCameras        int
	ReconnectInterval time.Duration

	// New: Backoff/Jitter config for reconnections
	ReconnectBackoffMin time.Duration
	ReconnectBackoffMax time.Duration
	ReconnectJitterPct  int

	// Frame Processing
	// When AI is OFF - high FPS for smooth streaming
	MaxFPSNoAI int
	// When AI is ON - lower FPS for processing
	MaxFPSWithAI int

	// Buffer sizes
	FrameBufferSize  int // Raw frames from OpenCV
	ProcessingBuffer int // Frames waiting for AI
	PublishingBuffer int // Frames ready for MediaMTX

	// AI Processing
	AIEnabled       bool
	AITimeout       time.Duration
	AIRetries       int
	AIFrameInterval int // Process every Nth frame for AI (1 = every frame, 2 = every 2nd frame, etc.)

	// Alerting via NATS
	AlertsSubject  string
	AlertsWorkers  int
	AlertsCooldown time.Duration

	// Suppressions via NATS
	SuppressionSubject  string
	SuppressionWorkers  int
	SuppressionCooldown time.Duration

	// Image Compression for Alerts (to prevent NATS payload exceeded errors)
	ImageCompressionEnabled bool
	MaxImageWidth           int
	MaxImageHeight          int
	ImageQuality            int // JPEG quality (1-100)
	MaxContextImageSize     int // bytes
	MaxDetectionImageSize   int // bytes
	MaxTotalPayloadSize     int // bytes

	// Video Recording
	VideoOutputDir string
	VideoChunkTime int // seconds per video chunk (default: 60 seconds)
	VideoMaxChunks int // max chunks to keep per camera

	// Stream Output
	OutputWidth   int
	OutputHeight  int
	OutputQuality int
	OutputBitrate int

	// Publishing
	PublishingFPS int

	// MediaMTX Publishing
	WHIPTimeout   time.Duration
	HLSSegments   int
	RTSPTransport string

	// Swagger Configuration
	SwaggerHost string
	SwaggerPort int

	// Metadata Overlay
	ShowFPS               bool
	ShowLatency           bool
	ShowCameraID          bool
	ShowAIStatus          bool
	ShowMetadata          bool
	ShowFrameID           bool
	ShowResolution        bool
	ShowTime              bool
	ShowQuality           bool
	ShowBitrate           bool
	ShowAIDetectionsCount bool
	ShowAIProcessingTime  bool
	ShowProjectCounts     bool
	OverlayColor          string
	OverlayFont           int

	// WebRTC Configuration
	WebRTCICEServers []string

	// Health Check
	HealthCheckInterval time.Duration

	// New: Treat camera as unhealthy if no frames for this duration
	FrameStaleThreshold time.Duration

	// Graceful Shutdown
	ShutdownTimeout time.Duration

	// New: Delay before restarting a crashed goroutine
	PanicRestartDelay time.Duration
}

func Load() *Config {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		log.Debug().Err(err).Msg("No .env file found or error loading .env file, using environment variables and defaults")
	} else {
		log.Info().Msg("Loaded configuration from .env file")
	}

	return &Config{
		// Application
		Version:     getEnv("VERSION", "1.0.0"),
		Environment: getEnv("ENVIRONMENT", "development"),
		WorkerID:    getEnv("WORKER_ID", "worker-1"),
		Port:        getEnvInt("PORT", 8000),
		LogLevel:    getEnv("LOG_LEVEL", "info"),

		// Logdy (lightweight web log viewer)
		LogdyEnabled: getEnvBool("LOGDY_ENABLED", true),
		LogdyHost:    getEnv("LOGDY_HOST", "localhost"),
		LogdyPort:    getEnvInt("LOGDY_PORT", 8080),

		// External Services
		MediaMTXURL: getEnv("MEDIAMTX_URL", "http://localhost:8889"),
		BackendURL:  getEnv("BACKEND_URL", "http://localhost:8500"),
		AIGRPCURL:   getEnv("AI_GRPC_URL", "192.168.1.76:50052"),

		// NATS (configured for Docker Compose setup)
		NatsURL:            getNatsURL(),
		NatsConnectTimeout: getEnvDuration("NATS_CONNECT_TIMEOUT", 10*time.Second),
		NatsReconnectWait:  getEnvDuration("NATS_RECONNECT_WAIT", 2*time.Second),
		NatsMaxReconnects:  getEnvInt("NATS_MAX_RECONNECTS", -1), // -1 = unlimited
		NatsDrainTimeout:   getEnvDuration("NATS_DRAIN_TIMEOUT", 5*time.Second),

		// Streaming Configuration
		RTSPTimeout:       getEnvDuration("RTSP_TIMEOUT", 10*time.Second),
		MaxRetries:        getEnvInt("MAX_RETRIES", 3),
		MaxCameras:        getEnvInt("MAX_CAMERAS", 10),
		ReconnectInterval: getEnvDuration("RECONNECT_INTERVAL", 5*time.Second),

		// Backoff/Jitter
		ReconnectBackoffMin: getEnvDuration("RECONNECT_BACKOFF_MIN", 1*time.Second),
		ReconnectBackoffMax: getEnvDuration("RECONNECT_BACKOFF_MAX", 30*time.Second),
		ReconnectJitterPct:  getEnvInt("RECONNECT_JITTER_PCT", 20),

		// Frame Processing
		MaxFPSNoAI:       getEnvInt("MAX_FPS_NO_AI", 30),
		MaxFPSWithAI:     getEnvInt("MAX_FPS_WITH_AI", 30),
		FrameBufferSize:  getEnvInt("FRAME_BUFFER_SIZE", 100),
		ProcessingBuffer: getEnvInt("PROCESSING_BUFFER_SIZE", 50),
		PublishingBuffer: getEnvInt("PUBLISHING_BUFFER_SIZE", 20),

		// AI Processing
		AIEnabled:       getEnvBool("AI_ENABLED", false),
		AITimeout:       getEnvDuration("AI_TIMEOUT", 5*time.Second),
		AIRetries:       getEnvInt("AI_RETRIES", 3),
		AIFrameInterval: getEnvInt("AI_FRAME_INTERVAL", 1),

		// Alerting via NATS
		AlertsSubject:  getEnv("ALERTS_SUBJECT", "alerts"),
		AlertsWorkers:  getEnvInt("ALERTS_WORKERS", 2),
		AlertsCooldown: getEnvDuration("ALERTS_COOLDOWN", 10*time.Second),

		// Suppressions via NATS
		SuppressionSubject:  getEnv("SUPPRESSION_SUBJECT", "suppressions"),
		SuppressionWorkers:  getEnvInt("SUPPRESSION_WORKERS", 2),
		SuppressionCooldown: getEnvDuration("SUPPRESSION_COOLDOWN", 10*time.Second),

		// Image Compression for Alerts (disabled for performance)
		ImageCompressionEnabled: getEnvBool("IMAGE_COMPRESSION_ENABLED", false),
		MaxImageWidth:           getEnvInt("MAX_IMAGE_WIDTH", 1280),
		MaxImageHeight:          getEnvInt("MAX_IMAGE_HEIGHT", 720),
		ImageQuality:            getEnvInt("IMAGE_QUALITY", 95),
		MaxContextImageSize:     getEnvInt("MAX_CONTEXT_IMAGE_SIZE", 10*1024*1024),  // 10MB
		MaxDetectionImageSize:   getEnvInt("MAX_DETECTION_IMAGE_SIZE", 5*1024*1024), // 5MB
		MaxTotalPayloadSize:     getEnvInt("MAX_TOTAL_PAYLOAD_SIZE", 50*1024*1024),  // 50MB

		// Video Recording
		VideoOutputDir: getEnv("VIDEO_OUTPUT_DIR", "/home/skylark/worker-reim/kepler-worker-go/kepler-videos"),
		VideoChunkTime: getEnvInt("VIDEO_CHUNK_TIME", 60), // 60 seconds per chunk
		VideoMaxChunks: getEnvInt("VIDEO_MAX_CHUNKS", 50), // Keep 50 chunks per camera (~50 minutes)

		// Stream Output
		OutputWidth:   getEnvInt("OUTPUT_WIDTH", 1280),
		OutputHeight:  getEnvInt("OUTPUT_HEIGHT", 720),
		OutputQuality: getEnvInt("OUTPUT_QUALITY", 75),
		OutputBitrate: getEnvInt("OUTPUT_BITRATE", 2000),

		// Publishing
		PublishingFPS: getEnvInt("PUBLISHING_FPS", 30),

		// MediaMTX Publishing
		WHIPTimeout:   getEnvDuration("WHIP_TIMEOUT", 10*time.Second),
		HLSSegments:   getEnvInt("HLS_SEGMENTS", 10),
		RTSPTransport: getEnv("RTSP_TRANSPORT", "udp"),

		// Swagger Configuration
		SwaggerHost: getEnv("SWAGGER_HOST", "localhost"),
		SwaggerPort: getEnvInt("SWAGGER_PORT", 8000),

		// Metadata Overlay
		ShowFPS:               getEnvBool("SHOW_FPS", true),
		ShowLatency:           getEnvBool("SHOW_LATENCY", true),
		ShowCameraID:          getEnvBool("SHOW_CAMERA_ID", true),
		ShowAIStatus:          getEnvBool("SHOW_AI_STATUS", true),
		ShowMetadata:          getEnvBool("SHOW_METADATA", true),
		ShowFrameID:           getEnvBool("SHOW_FRAME_ID", true),
		ShowResolution:        getEnvBool("SHOW_RESOLUTION", true),
		ShowTime:              getEnvBool("SHOW_TIME", true),
		ShowQuality:           getEnvBool("SHOW_QUALITY", true),
		ShowBitrate:           getEnvBool("SHOW_BITRATE", true),
		ShowAIDetectionsCount: getEnvBool("SHOW_AI_DETECTIONS_COUNT", true),
		ShowAIProcessingTime:  getEnvBool("SHOW_AI_PROCESSING_TIME", true),
		ShowProjectCounts:     getEnvBool("SHOW_PROJECT_COUNTS", true),
		OverlayColor:          getEnv("OVERLAY_COLOR", "#FF0000"),
		OverlayFont:           getEnvInt("OVERLAY_FONT", 2),

		// WebRTC Configuration
		WebRTCICEServers: []string{
			"stun:stun.l.google.com:19302",
		},

		// Health Check
		HealthCheckInterval: getEnvDuration("HEALTH_CHECK_INTERVAL", 30*time.Second),
		FrameStaleThreshold: getEnvDuration("FRAME_STALE_THRESHOLD", 10*time.Second),

		// Graceful Shutdown
		ShutdownTimeout:   getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		PanicRestartDelay: getEnvDuration("PANIC_RESTART_DELAY", 2*time.Second),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// Helper functions for Docker environment detection
func isRunningInDocker() bool {
	// Check for Docker-specific environment indicators
	if os.Getenv("DOCKER_CONTAINER") == "true" {
		return true
	}

	// Check for .dockerenv file
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	return false
}

// getNatsURL returns the appropriate NATS URL based on environment
func getNatsURL() string {
	if envURL := os.Getenv("NATS_URL"); envURL != "" {
		return envURL
	}

	// If running in Docker, use service name; otherwise use localhost
	if isRunningInDocker() {
		return "nats://nats:4222"
	}

	return "nats://localhost:4222"
}
