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
	AIEnabled         bool
	AITimeout         time.Duration
	AIRetries         int
	ProcessingWorkers int
	AIFrameInterval   int // Process every Nth frame for AI (1 = every frame, 2 = every 2nd frame, etc.)

	// Stream Output
	OutputWidth   int
	OutputHeight  int
	OutputQuality int
	OutputBitrate int

	// MediaMTX Publishing
	WHIPTimeout   time.Duration
	HLSSegments   int
	RTSPTransport string

	// Swagger Configuration
	SwaggerHost string
	SwaggerPort int

	// Metadata Overlay
	ShowFPS      bool
	ShowLatency  bool
	ShowCameraID bool
	ShowAIStatus bool
	ShowMetadata bool
	OverlayColor string
	OverlayFont  int

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
		LogdyHost:    getEnv("LOGDY_HOST", "127.0.0.1"),
		LogdyPort:    getEnvInt("LOGDY_PORT", 8080),

		// External Services
		MediaMTXURL: getEnv("MEDIAMTX_URL", "http://localhost:8889"),
		BackendURL:  getEnv("BACKEND_URL", "http://localhost:8500"),
		AIGRPCURL:   getEnv("AI_GRPC_URL", "54.89.211.207:50052"),

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
		MaxFPSWithAI:     getEnvInt("MAX_FPS_WITH_AI", 10),
		FrameBufferSize:  getEnvInt("FRAME_BUFFER_SIZE", 100),
		ProcessingBuffer: getEnvInt("PROCESSING_BUFFER_SIZE", 50),
		PublishingBuffer: getEnvInt("PUBLISHING_BUFFER_SIZE", 20),

		// AI Processing
		AIEnabled:         getEnvBool("AI_ENABLED", false),
		AITimeout:         getEnvDuration("AI_TIMEOUT", 5*time.Second),
		AIRetries:         getEnvInt("AI_RETRIES", 3),
		ProcessingWorkers: getEnvInt("PROCESSING_WORKERS", 4),
		AIFrameInterval:   getEnvInt("AI_FRAME_INTERVAL", 1), // Process every 3rd frame by default

		// Stream Output
		OutputWidth:   getEnvInt("OUTPUT_WIDTH", 1280),
		OutputHeight:  getEnvInt("OUTPUT_HEIGHT", 720),
		OutputQuality: getEnvInt("OUTPUT_QUALITY", 75),   // 0-100
		OutputBitrate: getEnvInt("OUTPUT_BITRATE", 2000), // kbps

		// MediaMTX Publishing
		WHIPTimeout:   getEnvDuration("WHIP_TIMEOUT", 10*time.Second),
		HLSSegments:   getEnvInt("HLS_SEGMENTS", 10),
		RTSPTransport: getEnv("RTSP_TRANSPORT", "udp"), // "udp", "tcp", "http"

		// Swagger Configuration
		SwaggerHost: getEnv("SWAGGER_HOST", "localhost"),
		SwaggerPort: getEnvInt("SWAGGER_PORT", 8000), // Use same as main port by default

		// Metadata Overlay
		ShowFPS:      getEnvBool("SHOW_FPS", true),
		ShowLatency:  getEnvBool("SHOW_LATENCY", false),
		ShowCameraID: getEnvBool("SHOW_CAMERA_ID", true),
		ShowAIStatus: getEnvBool("SHOW_AI_STATUS", false),
		ShowMetadata: getEnvBool("SHOW_METADATA", true),
		OverlayColor: getEnv("OVERLAY_COLOR", "#FF0000"), // Red
		OverlayFont:  getEnvInt("OVERLAY_FONT", 2),       // 1-5

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
