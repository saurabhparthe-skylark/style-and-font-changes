package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/api"
	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/logging"
)

func main() {
	cfg := config.Load()

	if err := setupLogging(cfg); err != nil {
		log.Error().Err(err).Msg("Failed to setup logging")
	}

	log.Info().
		Str("worker_id", cfg.WorkerID).
		Str("version", cfg.Version).
		Str("environment", cfg.Environment).
		Int("port", cfg.Port).
		Msg("Starting Kepler Worker")

	server, err := setupHTTPServer(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create server")
	}

	startServer(server)
	waitForShutdown(cfg, server)
}

func setupLogging(cfg *config.Config) error {
	zerolog.TimeFieldFormat = time.RFC3339

	// Base writer to stderr
	baseWriter := zerolog.ConsoleWriter{Out: os.Stderr}
	out := zerolog.MultiLevelWriter(baseWriter)

	// Optionally start Logdy and tee logs
	if cfg.LogdyEnabled {
		if lw, url, err := logging.StartLogdy(cfg); err == nil {
			out = zerolog.MultiLevelWriter(baseWriter, lw)
			log.Info().Str("logdy_url", url).Msg("Logdy web log viewer started")
		} else {
			log.Warn().Err(err).Msg("Failed to start Logdy; continuing without it")
		}
	}

	log.Logger = log.Output(out)

	lvl, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.Warn().Str("level", cfg.LogLevel).Msg("Invalid log level, using info")
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)
	return nil
}

func setupHTTPServer(cfg *config.Config) (*api.Server, error) {
	return api.NewServer(cfg)
}

func startServer(server *api.Server) {
	go func() {
		if err := server.Start(); err != nil {
			log.Fatal().Err(err).Msg("Server failed to start")
		}
	}()
}

func waitForShutdown(cfg *config.Config, server *api.Server) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("Shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("Server forced to shutdown")
	} else {
		log.Info().Msg("Server shutdown complete")
	}
}
