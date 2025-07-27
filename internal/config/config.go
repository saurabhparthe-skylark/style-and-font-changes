package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the worker
type Config struct {
	Worker   WorkerConfig   `yaml:"worker"`
	Stream   StreamConfig   `yaml:"stream"`
	AI       AIConfig       `yaml:"ai"`
	WebRTC   WebRTCConfig   `yaml:"webrtc"`
	MediaMTX MediaMTXConfig `yaml:"mediamtx"`
}

// WorkerConfig contains worker-specific settings
type WorkerConfig struct {
	ID              string        `yaml:"id"`
	Port            int           `yaml:"port"`
	MaxCameras      int           `yaml:"max_cameras"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// StreamConfig contains RTSP stream settings
type StreamConfig struct {
	BufferSize        int           `yaml:"buffer_size"`
	MaxRetries        int           `yaml:"max_retries"`
	RetryDelay        time.Duration `yaml:"retry_delay"`
	ConnectionTimeout time.Duration `yaml:"connection_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
}

// AIConfig contains AI processing settings
type AIConfig struct {
	Enabled       bool          `yaml:"enabled"`
	Endpoint      string        `yaml:"endpoint"`
	ProcessEveryN int           `yaml:"process_every_n"`
	Timeout       time.Duration `yaml:"timeout"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	RetryAttempts int           `yaml:"retry_attempts"`
}

// WebRTCConfig contains WebRTC publishing settings
type WebRTCConfig struct {
	Enabled    bool     `yaml:"enabled"`
	ICEServers []string `yaml:"ice_servers"`
	TargetFPS  int      `yaml:"target_fps"`
	MaxBitrate int      `yaml:"max_bitrate"`
	Resolution string   `yaml:"resolution"`
}

// MediaMTXConfig contains MediaMTX server settings
type MediaMTXConfig struct {
	WHIPEndpoint    string `yaml:"whip_endpoint"`
	HLSEndpoint     string `yaml:"hls_endpoint"`
	RTSPEndpoint    string `yaml:"rtsp_endpoint"`
	APIEndpoint     string `yaml:"api_endpoint"`
	MetricsEndpoint string `yaml:"metrics_endpoint"`
}

// Load loads configuration from environment and file
func Load(configPath string) (*Config, error) {
	// Default configuration
	cfg := &Config{
		Worker: WorkerConfig{
			ID:              getEnvString("WORKER_ID", "worker-1"),
			Port:            getEnvInt("WORKER_PORT", 5000),
			MaxCameras:      getEnvInt("MAX_CAMERAS", 10),
			ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		},
		Stream: StreamConfig{
			BufferSize:        getEnvInt("STREAM_BUFFER_SIZE", 5),
			MaxRetries:        getEnvInt("STREAM_MAX_RETRIES", 3),
			RetryDelay:        getEnvDuration("STREAM_RETRY_DELAY", 2*time.Second),
			ConnectionTimeout: getEnvDuration("STREAM_CONNECTION_TIMEOUT", 10*time.Second),
			ReadTimeout:       getEnvDuration("STREAM_READ_TIMEOUT", 5*time.Second),
		},
		AI: AIConfig{
			Enabled:       getEnvBool("AI_ENABLED", true),
			Endpoint:      getEnvString("AI_ENDPOINT", "localhost:50051"),
			ProcessEveryN: getEnvInt("AI_PROCESS_EVERY_N", 3),
			Timeout:       getEnvDuration("AI_TIMEOUT", 10*time.Second),
			MaxConcurrent: getEnvInt("AI_MAX_CONCURRENT", 2),
			RetryAttempts: getEnvInt("AI_RETRY_ATTEMPTS", 3),
		},
		WebRTC: WebRTCConfig{
			Enabled:    getEnvBool("WEBRTC_ENABLED", true),
			ICEServers: []string{"stun:stun.l.google.com:19302"},
			TargetFPS:  getEnvInt("WEBRTC_TARGET_FPS", 15),
			MaxBitrate: getEnvInt("WEBRTC_MAX_BITRATE", 2000000),
			Resolution: getEnvString("WEBRTC_RESOLUTION", "1280x720"),
		},
		MediaMTX: MediaMTXConfig{
			WHIPEndpoint:    getEnvString("MEDIAMTX_WHIP", "http://localhost:8889"),
			HLSEndpoint:     getEnvString("MEDIAMTX_HLS", "http://localhost:8888"),
			RTSPEndpoint:    getEnvString("MEDIAMTX_RTSP", "rtsp://localhost:8554"),
			APIEndpoint:     getEnvString("MEDIAMTX_API", "http://localhost:9997"),
			MetricsEndpoint: getEnvString("MEDIAMTX_METRICS", "http://localhost:9998"),
		},
	}

	// TODO: Add YAML file loading if needed

	return cfg, nil
}

// Helper functions for environment variables
func getEnvString(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
