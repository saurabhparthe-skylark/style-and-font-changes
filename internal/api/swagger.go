package api

import (
	"net/http"

	_ "kepler-worker/docs"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func (s *Server) setupSwagger() {
	s.router.GET("/api/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"title":       "Kepler Worker API",
			"version":     "1.0.0",
			"description": "Video processing worker API for RTSP streams, AI processing, and WebRTC publishing",
			"swagger_ui":  "/docs/index.html",
			"endpoints": gin.H{
				"health":        "/health",
				"worker_info":   "/",
				"cameras":       "/cameras",
				"webrtc":        "/webrtc",
				"visualization": "/visualization",
				"ppe":           "/ppe",
				"workers":       "/workers",
				"system":        "/system",
			},
			"worker_id": s.config.Worker.ID,
			"port":      s.config.Worker.Port,
		})
	})

	s.router.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	s.router.GET("/docs", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/docs/index.html")
	})
}

var SwaggerInfo = struct {
	Version     string
	Host        string
	BasePath    string
	Schemes     []string
	Title       string
	Description string
}{
	Version:     "1.0.0",
	Host:        "localhost:5000",
	BasePath:    "/",
	Schemes:     []string{"http", "https"},
	Title:       "Kepler Worker API",
	Description: "A video processing worker that handles RTSP streams, AI processing via gRPC, and WebRTC publishing to MediaMTX",
}
