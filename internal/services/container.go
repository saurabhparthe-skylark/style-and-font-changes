package services

import (
	"context"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services/camera"
	"kepler-worker-go/internal/services/detection"
)

// ServiceContainer holds all services
type ServiceContainer struct {
	Config        *config.Config
	DetectionSvc  *detection.Service
	CameraManager *camera.CameraManager
}

// NewServiceContainer creates a new service container
func NewServiceContainer(cfg *config.Config) (*ServiceContainer, error) {
	// Initialize detection service
	detectionSvc, err := detection.NewService(cfg.AIGRPCURL)
	if err != nil {
		return nil, err
	}

	// Initialize camera manager with new architecture
	cameraManager, err := camera.NewCameraManager(cfg, detectionSvc)
	if err != nil {
		return nil, err
	}

	return &ServiceContainer{
		Config:        cfg,
		DetectionSvc:  detectionSvc,
		CameraManager: cameraManager,
	}, nil
}

// Shutdown gracefully shuts down all services
func (sc *ServiceContainer) Shutdown(ctx context.Context) error {
	if sc.CameraManager != nil {
		if err := sc.CameraManager.Shutdown(ctx); err != nil {
			return err
		}
	}

	if sc.DetectionSvc != nil {
		sc.DetectionSvc.Shutdown(ctx)
	}

	return nil
}
