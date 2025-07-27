package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"kepler-worker/internal/api"
	"kepler-worker/internal/config"
	"kepler-worker/internal/worker"
	"kepler-worker/pkg/logger"
)

func main() {
	// Parse command line flags
	var (
		port       = flag.Int("port", 5000, "Worker port")
		workerID   = flag.String("worker-id", "worker-1", "Worker ID")
		configPath = flag.String("config", "config.yaml", "Config file path")
	)
	flag.Parse()

	// Initialize logger
	log := logger.New(*workerID)
	log.Info("Starting Kepler Worker", "worker_id", *workerID, "port", *port)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// Override config with command line args
	cfg.Worker.ID = *workerID
	cfg.Worker.Port = *port

	// Create worker core
	w, err := worker.New(cfg, log)
	if err != nil {
		log.Error("Failed to create worker", "error", err)
		os.Exit(1)
	}

	// Start worker core
	if err := w.Start(); err != nil {
		log.Error("Failed to start worker", "error", err)
		os.Exit(1)
	}

	// Create API server with worker and WebRTC publisher
	apiServer := api.NewServer(cfg, w, w.GetWebRTCPublisher())
	if err := apiServer.Setup(); err != nil {
		log.Error("Failed to setup API server", "error", err)
		os.Exit(1)
	}

	// Start API server in background
	go func() {
		log.Info("Starting API server", "port", cfg.Worker.Port)
		if err := apiServer.Start(); err != nil {
			log.Error("API server error", "error", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down...")

	// Stop API server
	if err := apiServer.Stop(); err != nil {
		log.Error("Error stopping API server", "error", err)
	}

	// Stop worker core
	if err := w.Stop(); err != nil {
		log.Error("Error stopping worker", "error", err)
	}
}
