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

	_ "kepler-worker-go/docs" // Import docs for swagger
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
	// Initialize services
	container, err := services.NewServiceContainer(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize services: %w", err)
	}

	// Initialize Gin router
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(middleware.CORS())

	// Create server instance
	server := &Server{
		cfg:       cfg,
		router:    router,
		container: container,
	}

	// Setup routes
	server.setupRoutes()

	// Create HTTP server
	server.server = &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     router,
		ReadTimeout: 15 * time.Second,
		// Disable write timeout to support long-lived streaming (MJPEG/WebRTC signaling)
		WriteTimeout: 0,
		IdleTimeout:  0,
	}

	return server, nil
}

func (s *Server) setupRoutes() {
	// Health endpoints - simplified for now
	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"worker_id": s.cfg.WorkerID,
			"port":      s.cfg.Port,
		})
	})

	s.router.GET("/metrics", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"worker_id": s.cfg.WorkerID,
			"metrics":   gin.H{"status": "ok"},
		})
	})

	// Camera management endpoints
	cameraHandler := handlers.NewCameraHandler(s.container.CameraManager)
	cameraGroup := s.router.Group("cameras")
	{
		cameraGroup.POST("", cameraHandler.StartCamera)       // POST /cameras for compatibility
		cameraGroup.POST("/start", cameraHandler.StartCamera) // POST /cameras/start
		cameraGroup.POST("/:camera_id/stop", cameraHandler.StopCamera)
		cameraGroup.GET("", cameraHandler.ListCameras)
		cameraGroup.GET("/:camera_id", cameraHandler.GetCamera)
		cameraGroup.GET("/stats", cameraHandler.GetCameraStats)

		// AI configuration endpoints
		cameraGroup.GET("/:camera_id/ai", cameraHandler.GetCameraAI)
		cameraGroup.PUT("/:camera_id/ai", cameraHandler.UpdateCameraAI)
		cameraGroup.POST("/:camera_id/ai/toggle", cameraHandler.ToggleCameraAI)
	}

	// MJPEG streaming endpoint
	s.router.GET("/mjpeg/:camera_id", func(c *gin.Context) {
		cameraID := c.Param("camera_id")
		// Stream directly from publisher
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		s.container.CameraManager.PublisherStreamMJPEG(c.Writer, c.Request, cameraID)
	})

	// Worker info endpoint - simplified
	s.router.GET("/worker/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"worker_id":  s.cfg.WorkerID,
			"version":    s.cfg.Version,
			"port":       s.cfg.Port,
			"ai_enabled": s.cfg.AIEnabled,
		})
	})

	// Swagger documentation
	s.router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler,
		ginSwagger.URL("/swagger/doc.json"),
		ginSwagger.DefaultModelsExpandDepth(-1)))
}

// @title Kepler Worker API
// @version 1.0
// @description Enterprise-grade video processing worker with AI integration
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8000
// @BasePath /
// @schemes http

func (s *Server) Start() error {
	log.Info().
		Str("worker_id", s.cfg.WorkerID).
		Int("port", s.cfg.Port).
		Bool("ai_enabled", s.cfg.AIEnabled).
		Msg("Starting Kepler Worker server with enterprise pipeline")

	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	log.Info().Msg("Shutting down server")

	// Shutdown services
	if s.container != nil {
		if err := s.container.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("Error shutting down services")
		}
	}

	// Shutdown HTTP server
	return s.server.Shutdown(ctx)
}
