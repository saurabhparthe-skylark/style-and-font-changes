package logging

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
)

func NewServiceLogger(cfg *config.Config, service string) zerolog.Logger {
	return log.With().Str("worker_id", cfg.WorkerID).Str("service", service).Logger()
}

func WithCamera(base zerolog.Logger, cameraID string) zerolog.Logger {
	return base.With().Str("camera_id", cameraID).Logger()
}
