package handlers

import (
	"kepler-worker/internal/webrtc"
	"net/http"

	"github.com/gin-gonic/gin"
)

type WebRTCHandler struct {
	publisher *webrtc.Publisher
}

func NewWebRTCHandler(publisher *webrtc.Publisher) *WebRTCHandler {
	return &WebRTCHandler{publisher: publisher}
}

type StreamURLsResponse struct {
	CameraID string `json:"camera_id" example:"cam1"`
	HLS      string `json:"hls" example:"http://172.17.0.1:8888/camera_cam1/index.m3u8"`
	WebRTC   string `json:"webrtc" example:"http://172.17.0.1:8889/camera_cam1/whep"`
	RTSP     string `json:"rtsp" example:"rtsp://172.17.0.1:8554/camera_cam1"`
}

type WebRTCStatsResponse struct {
	CameraID    string                 `json:"camera_id" example:"cam1"`
	Active      bool                   `json:"active" example:"true"`
	StreamPath  string                 `json:"stream_path" example:"camera_cam1"`
	Uptime      string                 `json:"uptime" example:"5m30s"`
	Processing  map[string]interface{} `json:"processing"`
	WHIPSession map[string]interface{} `json:"whip_session"`
}

// @Summary Get stream URLs
// @Description Get streaming URLs for all formats (HLS, WebRTC, RTSP) for a camera
// @Tags webrtc
// @Accept json
// @Produce json
// @Param id path string true "Camera ID" example:"cam1"
// @Success 200 {object} StreamURLsResponse
// @Failure 404 {object} map[string]string
// @Router /webrtc/{id}/urls [get]
func (h *WebRTCHandler) GetStreamURLs(c *gin.Context) {
	cameraID := c.Param("id")

	if h.publisher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WebRTC publisher not available"})
		return
	}

	hls := h.publisher.GetStreamURL(cameraID, "hls")
	webrtc := h.publisher.GetStreamURL(cameraID, "webrtc")
	rtsp := h.publisher.GetStreamURL(cameraID, "rtsp")

	if hls == "" && webrtc == "" && rtsp == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found or not publishing"})
		return
	}

	c.JSON(http.StatusOK, StreamURLsResponse{
		CameraID: cameraID,
		HLS:      hls,
		WebRTC:   webrtc,
		RTSP:     rtsp,
	})
}

// @Summary Get WebRTC statistics
// @Description Get WebRTC publishing statistics for a camera or all cameras
// @Tags webrtc
// @Accept json
// @Produce json
// @Param id query string false "Camera ID (optional, returns all if not specified)" example:"cam1"
// @Success 200 {object} map[string]interface{}
// @Router /webrtc/stats [get]
func (h *WebRTCHandler) GetStats(c *gin.Context) {
	if h.publisher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "WebRTC publisher not available"})
		return
	}

	cameraID := c.Query("id")
	stats := h.publisher.GetStats(cameraID)

	if cameraID != "" && len(stats) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Camera not found or not publishing"})
		return
	}

	if cameraID != "" {
		c.JSON(http.StatusOK, stats)
	} else {
		c.JSON(http.StatusOK, gin.H{
			"cameras": stats,
			"total":   len(stats),
		})
	}
}
