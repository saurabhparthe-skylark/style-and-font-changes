package services

import (
	"context"
	"fmt"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services/camera"
	"kepler-worker-go/internal/services/messaging"
	"kepler-worker-go/internal/services/postprocessing"
	"kepler-worker-go/internal/services/publisher"
)

// ServiceContainer holds all services
type ServiceContainer struct {
	Config            *config.Config
	CameraManager     *camera.CameraManager
	PostProcessingSvc *postprocessing.Service
	MessageSvc        *messaging.Service
	PublisherSvc      *publisher.Service
}

// NewServiceContainer creates a new service container
func NewServiceContainer(cfg *config.Config) (*ServiceContainer, error) {
	// Initialize messaging service (NATS)
	messageSvc, err := messaging.NewService(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize messaging service: %w", err)
	}

	// Initialize publisher service (MediaMTX)
	publisherSvc, err := publisher.NewService(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize publisher service: %w", err)
	}

	// Initialize post-processing service with message publisher
	postProcessingSvc, err := postprocessing.NewService(cfg, messageSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize post-processing service: %w", err)
	}

	// Initialize camera manager
	cameraManager, err := camera.NewCameraManager(cfg, postProcessingSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize camera manager: %w", err)
	}

	return &ServiceContainer{
		Config:            cfg,
		CameraManager:     cameraManager,
		PostProcessingSvc: postProcessingSvc,
		MessageSvc:        messageSvc,
		PublisherSvc:      publisherSvc,
	}, nil
}

// Shutdown gracefully shuts down all services
func (sc *ServiceContainer) Shutdown(ctx context.Context) error {
	// Shutdown in reverse order
	if sc.CameraManager != nil {
		if err := sc.CameraManager.Shutdown(ctx); err != nil {
			return fmt.Errorf("camera manager shutdown error: %w", err)
		}
	}

	if sc.PublisherSvc != nil {
		if err := sc.PublisherSvc.Shutdown(ctx); err != nil {
			return fmt.Errorf("publisher service shutdown error: %w", err)
		}
	}

	if sc.PostProcessingSvc != nil {
		if err := sc.PostProcessingSvc.Shutdown(ctx); err != nil {
			return fmt.Errorf("post-processing service shutdown error: %w", err)
		}
	}

	if sc.MessageSvc != nil {
		if err := sc.MessageSvc.Shutdown(ctx); err != nil {
			return fmt.Errorf("messaging service shutdown error: %w", err)
		}
	}

	return nil
}
