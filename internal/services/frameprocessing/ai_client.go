package frameprocessing

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"kepler-worker-go/internal/config"
	pb "kepler-worker-go/proto"
)

// AIClient manages gRPC connections to AI detection services with automatic retry
type AIClient struct {
	cfg *config.Config

	mu         sync.RWMutex
	grpcConn   *grpc.ClientConn
	grpcClient pb.DetectionServiceClient
	endpoint   string
	cameraID   string

	// Retry management
	lastFailTime     time.Time
	consecutiveFails int
	maxRetryBackoff  time.Duration
}

// NewAIClient creates a new AI client instance
func NewAIClient(cfg *config.Config, cameraID string) *AIClient {
	return &AIClient{
		cfg:             cfg,
		cameraID:        cameraID,
		maxRetryBackoff: 30 * time.Second, // Maximum backoff time
	}
}

// Connect establishes a gRPC connection to the AI service
func (ac *AIClient) Connect(endpoint string) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	// If already connected to this endpoint, skip
	if ac.grpcConn != nil && ac.endpoint == endpoint {
		return nil
	}

	// Close existing connection if any
	if ac.grpcConn != nil {
		ac.grpcConn.Close()
		ac.grpcConn = nil
		ac.grpcClient = nil
	}

	normalizedEndpoint, creds, err := ac.parseGRPCEndpoint(endpoint)
	if err != nil {
		return fmt.Errorf("failed to parse AI endpoint %s: %w", endpoint, err)
	}

	log.Info().
		Str("camera_id", ac.cameraID).
		Str("original_endpoint", endpoint).
		Str("normalized_endpoint", normalizedEndpoint).
		Bool("use_tls", creds.Info().SecurityProtocol == "tls").
		Msg("Connecting to AI gRPC service")

	conn, err := grpc.NewClient(normalizedEndpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to AI service at %s: %w", normalizedEndpoint, err)
	}

	client := pb.NewDetectionServiceClient(conn)

	// Perform health check asynchronously to not block camera restart
	// If it fails, the caller can retry on the next attempt
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := client.HealthCheck(ctx, &pb.Empty{}); err != nil {
			log.Warn().
				Err(err).
				Str("camera_id", ac.cameraID).
				Str("ai_endpoint", normalizedEndpoint).
				Msg("Initial AI health check failed - will retry on next frame")
		} else {
			log.Info().
				Str("camera_id", ac.cameraID).
				Str("ai_endpoint", normalizedEndpoint).
				Msg("AI service health check passed")
		}
	}()

	ac.grpcConn = conn
	ac.grpcClient = client
	ac.endpoint = endpoint
	ac.consecutiveFails = 0 // Reset failure count on successful connection

	log.Info().
		Str("camera_id", ac.cameraID).
		Str("ai_endpoint", normalizedEndpoint).
		Msg("AI gRPC connection initialized (health check in progress)")

	return nil
}

// Disconnect closes the gRPC connection
func (ac *AIClient) Disconnect() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.grpcConn != nil {
		ac.grpcConn.Close()
		ac.grpcConn = nil
		ac.grpcClient = nil
		ac.endpoint = ""

		log.Info().
			Str("camera_id", ac.cameraID).
			Msg("AI gRPC connection closed")
	}
}

// GetClient returns the gRPC client if connected
func (ac *AIClient) GetClient() pb.DetectionServiceClient {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.grpcClient
}

// IsConnected checks if the client is connected and healthy
func (ac *AIClient) IsConnected() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if ac.grpcConn == nil || ac.grpcClient == nil {
		return false
	}

	state := ac.grpcConn.GetState()
	return state == connectivity.Ready || state == connectivity.Idle || state == connectivity.Connecting
}

// IsInBadState checks if the connection is in a failure state
func (ac *AIClient) IsInBadState() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if ac.grpcConn == nil {
		return false
	}

	state := ac.grpcConn.GetState()
	return state == connectivity.TransientFailure || state == connectivity.Shutdown
}

// GetConnectionState returns the current connection state
func (ac *AIClient) GetConnectionState() connectivity.State {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if ac.grpcConn == nil {
		return connectivity.Shutdown
	}

	return ac.grpcConn.GetState()
}

// GetEndpoint returns the current endpoint
func (ac *AIClient) GetEndpoint() string {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.endpoint
}

// EnsureConnected ensures the connection is healthy and reconnects if needed
// This method implements automatic retry with exponential backoff
func (ac *AIClient) EnsureConnected(endpoint string) error {
	// Check if we should attempt reconnection based on backoff
	if !ac.shouldRetry() {
		return fmt.Errorf("in backoff period after consecutive failures")
	}

	// Check if we need to connect or reconnect
	needsConnection := false

	ac.mu.RLock()
	if ac.grpcClient == nil || ac.endpoint != endpoint {
		needsConnection = true
	} else if ac.grpcConn != nil {
		state := ac.grpcConn.GetState()
		if state == connectivity.TransientFailure || state == connectivity.Shutdown {
			needsConnection = true
		}
	}
	ac.mu.RUnlock()

	if needsConnection {
		if err := ac.Connect(endpoint); err != nil {
			ac.recordFailure()
			return fmt.Errorf("failed to ensure connection: %w", err)
		}
	}

	return nil
}

// InferDetection performs AI detection on a frame with automatic retry on failure
func (ac *AIClient) InferDetection(ctx context.Context, req *pb.FrameRequest) (*pb.DetectionResponse, error) {
	ac.mu.RLock()
	client := ac.grpcClient
	ac.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("AI client not connected")
	}

	resp, err := client.InferDetection(ctx, req)

	if err != nil {
		ac.recordFailure()
		return nil, fmt.Errorf("inference failed: %w", err)
	}

	// Reset failure count on successful inference
	ac.mu.Lock()
	ac.consecutiveFails = 0
	ac.mu.Unlock()

	return resp, nil
}

// shouldRetry determines if we should attempt a connection based on exponential backoff
func (ac *AIClient) shouldRetry() bool {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if ac.consecutiveFails == 0 {
		return true
	}

	// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s (max)
	backoffDuration := time.Duration(1<<uint(ac.consecutiveFails-1)) * time.Second
	if backoffDuration > ac.maxRetryBackoff {
		backoffDuration = ac.maxRetryBackoff
	}

	timeSinceLastFail := time.Since(ac.lastFailTime)
	return timeSinceLastFail >= backoffDuration
}

// recordFailure records a connection failure for backoff calculation
func (ac *AIClient) recordFailure() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.consecutiveFails++
	ac.lastFailTime = time.Now()

	if ac.consecutiveFails <= 5 {
		log.Warn().
			Str("camera_id", ac.cameraID).
			Int("consecutive_fails", ac.consecutiveFails).
			Msg("AI connection failure recorded")
	}
}

// parseGRPCEndpoint parses and normalizes the gRPC endpoint URL
func (ac *AIClient) parseGRPCEndpoint(endpoint string) (string, credentials.TransportCredentials, error) {
	// Add scheme if missing
	if !strings.Contains(endpoint, "://") {
		if strings.Contains(endpoint, ".") && !strings.Contains(endpoint, ":") {
			endpoint = "https://" + endpoint + ":443"
		} else if strings.Contains(endpoint, ":") {
			parts := strings.Split(endpoint, ":")
			if len(parts) == 2 {
				if port, err := strconv.Atoi(parts[1]); err == nil {
					if port == 443 || port == 8443 || port == 9443 {
						endpoint = "https://" + endpoint
					} else {
						endpoint = "http://" + endpoint
					}
				} else {
					endpoint = "http://" + endpoint
				}
			}
		} else {
			endpoint = "https://" + endpoint + ":443"
		}
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	// Ensure port is set
	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https":
			host = u.Hostname() + ":443"
		case "http":
			host = u.Hostname() + ":80"
		default:
			return "", nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
		}
	}

	// Setup credentials based on scheme
	var creds credentials.TransportCredentials
	switch u.Scheme {
	case "https":
		tlsConfig := &tls.Config{ServerName: u.Hostname()}
		creds = credentials.NewTLS(tlsConfig)
	case "http":
		creds = insecure.NewCredentials()
	default:
		return "", nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	return host, creds, nil
}
