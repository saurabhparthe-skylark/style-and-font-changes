package camera

import (
	"kepler-worker-go/internal/config"

	"github.com/rs/zerolog/log"
)

// StreamManager manages stream configurations
type StreamManager struct {
	cfg *config.Config
}

// NewStreamManager creates a new stream manager
func NewStreamManager(cfg *config.Config) (*StreamManager, error) {
	return &StreamManager{cfg: cfg}, nil
}

// Shutdown shuts down the stream manager
func (sm *StreamManager) Shutdown() {
	log.Info().Msg("Stream manager shutdown")
}
