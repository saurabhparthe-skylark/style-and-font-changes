package handlers

import (
	"fmt"
	"kepler-worker/internal/config"
	"kepler-worker/internal/webrtc"
	"net/http"

	"github.com/gin-gonic/gin"
)

type WebRTCHandler struct {
	publisher *webrtc.Publisher
	config    *config.Config
}

func NewWebRTCHandler(publisher *webrtc.Publisher, cfg *config.Config) *WebRTCHandler {
	return &WebRTCHandler{
		publisher: publisher,
		config:    cfg,
	}
}

type StreamURLsResponse struct {
	CameraID string `json:"camera_id" example:"test-camera"`
	HLS      string `json:"hls" example:"http://127.0.0.1:8888/camera_test-camera/index.m3u8"`
	WebRTC   string `json:"webrtc" example:"http://127.0.0.1:8889/camera_test-camera/whep"`
	RTSP     string `json:"rtsp" example:"rtsp://127.0.0.1:8554/camera_test-camera"`
}

type WebRTCStatsResponse struct {
	CameraID    string                 `json:"camera_id" example:"test-camera"`
	Active      bool                   `json:"active" example:"true"`
	StreamPath  string                 `json:"stream_path" example:"camera_test-camera"`
	Uptime      string                 `json:"uptime" example:"5m30s"`
	Processing  map[string]interface{} `json:"processing"`
	WHIPSession map[string]interface{} `json:"whip_session"`
}

type AllStatsResponse struct {
	Cameras map[string]interface{} `json:"cameras"`
	Total   int                    `json:"total"`
}

// @Summary Get stream URLs
// @Description Get streaming URLs for all formats (HLS, WebRTC, RTSP) for a camera
// @Tags webrtc
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"test-camera"
// @Success 200 {object} StreamURLsResponse
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /webrtc/{id}/urls [get]
func (h *WebRTCHandler) GetStreamURLs(c *gin.Context) {
	cameraID := c.Param("id")

	if h.publisher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "WebRTC publisher not available",
			"code":  "PUBLISHER_UNAVAILABLE",
		})
		return
	}

	// In the new simplified architecture, we generate URLs directly
	// since FFmpeg publishes to MediaMTX at predictable paths
	streamPath := fmt.Sprintf("camera_%s", cameraID)

	// Generate URLs based on MediaMTX configuration from config
	hls := fmt.Sprintf("%s/%s/index.m3u8", h.config.MediaMTX.HLSEndpoint, streamPath)
	webrtc := fmt.Sprintf("%s/%s/whep", h.config.MediaMTX.WHIPEndpoint, streamPath)
	rtsp := fmt.Sprintf("%s/%s", h.config.MediaMTX.RTSPEndpoint, streamPath)

	response := StreamURLsResponse{
		CameraID: cameraID,
		HLS:      hls,
		WebRTC:   webrtc,
		RTSP:     rtsp,
	}

	c.JSON(http.StatusOK, gin.H{
		"urls":   response,
		"status": "active",
		"method": "ffmpeg_republishing",
		"note":   "Streams are republished from RTSP input to MediaMTX via FFmpeg",
	})
}

// @Summary Get WebRTC statistics
// @Description Get WebRTC publishing statistics for a camera or all cameras
// @Tags webrtc
// @Accept json
// @Produce json
// @Param id query string false "Camera ID (optional, returns all if not specified)" example:"test-camera"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /webrtc/stats [get]
func (h *WebRTCHandler) GetStats(c *gin.Context) {
	if h.publisher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "WebRTC publisher not available",
			"code":  "PUBLISHER_UNAVAILABLE",
		})
		return
	}

	cameraID := c.Query("id")

	// Since we're using FFmpeg transcoding, we return simplified stats
	// In a more complete implementation, you might want to check MediaMTX API
	// or get actual camera status from the worker

	if cameraID != "" {
		// Single camera stats
		stats := map[string]interface{}{
			"camera_id":   cameraID,
			"method":      "ffmpeg_transcoding",
			"stream_path": fmt.Sprintf("camera_%s", cameraID),
			"status":      "active", // In reality, you'd check if the camera is actually running
			"urls": map[string]string{
				"hls":    fmt.Sprintf("%s/camera_%s/index.m3u8", h.config.MediaMTX.HLSEndpoint, cameraID),
				"webrtc": fmt.Sprintf("%s/camera_%s/whep", h.config.MediaMTX.WHIPEndpoint, cameraID),
				"rtsp":   fmt.Sprintf("%s/camera_%s", h.config.MediaMTX.RTSPEndpoint, cameraID),
			},
		}
		c.JSON(http.StatusOK, stats)
	} else {
		// All cameras stats - simplified response
		response := AllStatsResponse{
			Cameras: map[string]interface{}{
				"note": "In FFmpeg transcoding mode. Use /cameras endpoint for actual camera status.",
			},
			Total: 0,
		}
		c.JSON(http.StatusOK, response)
	}
}
