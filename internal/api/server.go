package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"kepler-worker/internal/api/handlers"
	"kepler-worker/internal/config"
	"kepler-worker/internal/webrtc"
	"kepler-worker/internal/worker"

	"github.com/gin-gonic/gin"
)

type Server struct {
	config *config.Config
	router *gin.Engine
	server *http.Server

	healthHandler *handlers.HealthHandler
	cameraHandler *handlers.CameraHandler
	webrtcHandler *handlers.WebRTCHandler
	systemHandler *handlers.SystemHandler
}

func NewServer(cfg *config.Config, w *worker.Worker, publisher *webrtc.Publisher) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	return &Server{
		config:        cfg,
		router:        router,
		healthHandler: handlers.NewHealthHandler(cfg.Worker.ID),
		cameraHandler: handlers.NewCameraHandler(w),
		webrtcHandler: handlers.NewWebRTCHandler(publisher),
		systemHandler: handlers.NewSystemHandler(cfg.Worker.ID),
	}
}

func (s *Server) Setup() error {
	s.setupMiddleware()
	s.setupRoutes()
	s.setupSwagger()

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.config.Worker.Port),
		Handler: s.router,
	}

	return nil
}

func (s *Server) Start() error {
	fmt.Printf("🚀 Starting Kepler Worker API on port %d\n", s.config.Worker.Port)
	return s.server.ListenAndServe()
}

func (s *Server) Stop() error {
	fmt.Println("🛑 Stopping Kepler Worker API...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *Server) GetServer() *http.Server {
	return s.server
}
