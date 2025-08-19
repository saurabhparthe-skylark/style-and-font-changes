package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "kepler-worker-go/docs"
	"kepler-worker-go/internal/api/handlers"
	"kepler-worker-go/internal/api/middleware"
	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services"
)

type Server struct {
	cfg       *config.Config
	server    *http.Server
	router    *gin.Engine
	container *services.ServiceContainer
}

func NewServer(cfg *config.Config) (*Server, error) {
	container, err := services.NewServiceContainer(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize services: %w", err)
	}
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.RequestContext())
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.CORS())

	server := &Server{cfg: cfg, router: router, container: container}
	server.setupRoutes()
	server.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  0,
	}
	return server, nil
}

func (s *Server) setupRoutes() {
	healthHandler := handlers.NewHealthHandler(s.cfg, s.container)
	s.router.GET("/health", healthHandler.Check)
	s.router.GET("/status", healthHandler.Status)
	s.router.GET("/metrics", healthHandler.Metrics)

	// Test endpoints
	testGroup := s.router.Group("/test")
	{
		testGroup.POST("/nats", healthHandler.TestNATS)
	}

	cameraHandler := handlers.NewCameraHandler(s.container.CameraManager)
	cameraGroup := s.router.Group("cameras")
	{
		cameraGroup.GET("/stats", cameraHandler.GetCameraStats)              // Camera statistics
		cameraGroup.GET("", cameraHandler.ListCameras)                       // List all cameras
		cameraGroup.GET("/:camera_id", cameraHandler.GetCamera)              // Get camera details
		cameraGroup.POST("", cameraHandler.StartCamera)                      // Create/start camera
		cameraGroup.PUT("/:camera_id", cameraHandler.UpsertCamera)           // Upsert camera settings
		cameraGroup.POST("/:camera_id/restart", cameraHandler.RestartCamera) // Hard restart camera
		cameraGroup.DELETE("/:camera_id", cameraHandler.DeleteCamera)        // Delete camera (REST-compliant)
	}
	s.router.GET("/mjpeg/:camera_id", func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		s.container.CameraManager.ServeHTTP(c.Writer, c.Request)
	})

	// Video endpoints for recorded content
	videoHandler := handlers.NewVideoHandler(s.cfg, s.container.RecorderSvc)
	videoGroup := s.router.Group("/videos")
	{
		videoGroup.GET("/:camera_id/chunks", videoHandler.GetCameraChunks)
		videoGroup.GET("/:camera_id/chunks/:chunk_id/stream", videoHandler.StreamChunk)
		videoGroup.GET("/:camera_id/playlist.m3u8", videoHandler.GetHLSPlaylist)
		videoGroup.GET("/:camera_id/playlist/info", videoHandler.GetPlaylistInfo)
	}

	// Dynamic Swagger URL based on request host to allow access from any IP
	s.router.GET("/swagger/*any", func(c *gin.Context) {
		// Determine the scheme (http or https)
		scheme := "http"
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}

		// Use the request host or fall back to config
		host := c.Request.Host
		if host == "" {
			host = fmt.Sprintf("%s:%d", s.cfg.SwaggerHost, s.cfg.SwaggerPort)
		}

		// Construct dynamic URL
		swaggerURL := fmt.Sprintf("%s://%s/swagger/doc.json", scheme, host)

		// Serve with dynamic URL
		ginSwagger.WrapHandler(
			swaggerFiles.Handler,
			ginSwagger.URL(swaggerURL),
			ginSwagger.DefaultModelsExpandDepth(-1),
		)(c)
	})
}

func (s *Server) Start() error {
	log.Info().
		Int("port", s.cfg.Port).
		Str("swagger_url", fmt.Sprintf("http://%s:%d/swagger/index.html", s.cfg.SwaggerHost, s.cfg.SwaggerPort)).
		Str("worker_id", s.cfg.WorkerID).
		Msg("Starting Kepler Worker Server")
	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down server")
	if s.container != nil {
		if err := s.container.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("Error shutting down services")
		}
	}
	return s.server.Shutdown(ctx)
}
