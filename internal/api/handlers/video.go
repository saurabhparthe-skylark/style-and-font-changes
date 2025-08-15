package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services/recorder"
)

type VideoHandler struct {
	cfg             *config.Config
	recorderService *recorder.Service
}

type VideoChunk struct {
	ChunkID   string    `json:"chunk_id"`
	CameraID  string    `json:"camera_id"`
	StartTime time.Time `json:"start_time"`
	Duration  float64   `json:"duration"`
	FileSize  int64     `json:"file_size"`
	FilePath  string    `json:"file_path"`
	CreatedAt time.Time `json:"created_at"`
}

type ChunksResponse struct {
	CameraID     string       `json:"camera_id"`
	TotalChunks  int          `json:"total_chunks"`
	TotalSize    int64        `json:"total_size_bytes"`
	EarliestTime time.Time    `json:"earliest_time"`
	LatestTime   time.Time    `json:"latest_time"`
	Chunks       []VideoChunk `json:"chunks"`
}

type HLSPlaylistResponse struct {
	CameraID    string  `json:"camera_id"`
	PlaylistURL string  `json:"playlist_url"`
	TotalChunks int     `json:"total_chunks"`
	Duration    float64 `json:"total_duration_seconds"`
}

func NewVideoHandler(cfg *config.Config, recorderService *recorder.Service) *VideoHandler {
	return &VideoHandler{
		cfg:             cfg,
		recorderService: recorderService,
	}
}

// GetCameraChunks godoc
// @Summary Get available video chunks for a camera
// @Description Get list of all recorded video chunks for a specific camera
// @Tags videos
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Param limit query int false "Maximum number of chunks to return (default: 50)"
// @Param offset query int false "Number of chunks to skip (default: 0)"
// @Success 200 {object} ChunksResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /videos/{camera_id}/chunks [get]
func (h *VideoHandler) GetCameraChunks(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	// Parse query parameters
	limit := 50
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	offset := 0
	if offsetStr := c.Query("offset"); offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	chunks, err := h.getChunksForCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to get chunks")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get video chunks"})
		return
	}

	if len(chunks) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No video chunks found for camera"})
		return
	}

	// Apply pagination
	totalChunks := len(chunks)
	if offset >= totalChunks {
		offset = 0
	}

	end := offset + limit
	if end > totalChunks {
		end = totalChunks
	}

	paginatedChunks := chunks[offset:end]

	// Calculate total size and time range
	var totalSize int64
	var earliestTime, latestTime time.Time

	for i, chunk := range chunks {
		totalSize += chunk.FileSize
		if i == 0 {
			earliestTime = chunk.StartTime
			latestTime = chunk.StartTime
		} else {
			if chunk.StartTime.Before(earliestTime) {
				earliestTime = chunk.StartTime
			}
			if chunk.StartTime.After(latestTime) {
				latestTime = chunk.StartTime
			}
		}
	}

	response := ChunksResponse{
		CameraID:     cameraID,
		TotalChunks:  totalChunks,
		TotalSize:    totalSize,
		EarliestTime: earliestTime,
		LatestTime:   latestTime,
		Chunks:       paginatedChunks,
	}

	c.JSON(http.StatusOK, response)
}

// StreamChunk godoc
// @Summary Stream a video chunk
// @Description Stream a specific video chunk file
// @Tags videos
// @Produce application/octet-stream
// @Param camera_id path string true "Camera ID"
// @Param chunk_id path string true "Chunk ID (filename)"
// @Success 200 {file} video/mp4
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /videos/{camera_id}/chunks/{chunk_id}/stream [get]
func (h *VideoHandler) StreamChunk(c *gin.Context) {
	cameraID := c.Param("camera_id")
	chunkID := c.Param("chunk_id")

	if cameraID == "" || chunkID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id and chunk_id are required"})
		return
	}

	// Validate chunk_id format and security
	if !strings.HasSuffix(chunkID, ".mp4") || strings.Contains(chunkID, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid chunk ID format"})
		return
	}

	chunkPath := filepath.Join(h.cfg.VideoOutputDir, cameraID, chunkID)

	// Check if file exists
	if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video chunk not found"})
		return
	}

	// Set headers for video streaming
	c.Header("Content-Type", "video/mp4")
	c.Header("Accept-Ranges", "bytes")
	c.Header("Cache-Control", "public, max-age=3600") // Cache for 1 hour

	// Serve the file with range support
	c.File(chunkPath)
}

// GetHLSPlaylist godoc
// @Summary Get HLS playlist for camera
// @Description Generate an HLS playlist from available video chunks
// @Tags videos
// @Produce application/vnd.apple.mpegurl
// @Param camera_id path string true "Camera ID"
// @Param duration query int false "Duration in minutes (default: all available)"
// @Success 200 {object} HLSPlaylistResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /videos/{camera_id}/playlist.m3u8 [get]
func (h *VideoHandler) GetHLSPlaylist(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	// Parse duration filter
	var durationMinutes int
	if durationStr := c.Query("duration"); durationStr != "" {
		if parsed, err := strconv.Atoi(durationStr); err == nil && parsed > 0 {
			durationMinutes = parsed
		}
	}

	chunks, err := h.getChunksForCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to get chunks for playlist")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate playlist"})
		return
	}

	if len(chunks) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No video chunks available for playlist"})
		return
	}

	// Filter chunks by duration if specified
	if durationMinutes > 0 {
		cutoffTime := time.Now().Add(-time.Duration(durationMinutes) * time.Minute)
		var filteredChunks []VideoChunk
		for _, chunk := range chunks {
			if chunk.StartTime.After(cutoffTime) {
				filteredChunks = append(filteredChunks, chunk)
			}
		}
		chunks = filteredChunks
	}

	// Generate HLS playlist content
	playlist := h.generateHLSPlaylist(cameraID, chunks)

	// Set HLS content type
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.Header("Cache-Control", "no-cache")

	// Return playlist content directly
	c.String(http.StatusOK, playlist)
}

// GetPlaylistInfo godoc
// @Summary Get HLS playlist information
// @Description Get information about the HLS playlist without the actual content
// @Tags videos
// @Produce json
// @Param camera_id path string true "Camera ID"
// @Success 200 {object} HLSPlaylistResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /videos/{camera_id}/playlist/info [get]
func (h *VideoHandler) GetPlaylistInfo(c *gin.Context) {
	cameraID := c.Param("camera_id")
	if cameraID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camera_id is required"})
		return
	}

	chunks, err := h.getChunksForCamera(cameraID)
	if err != nil {
		log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to get chunks info")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get playlist info"})
		return
	}

	if len(chunks) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No video chunks available"})
		return
	}

	totalDuration := float64(len(chunks)) * h.getChunkDuration()
	playlistURL := fmt.Sprintf("/videos/%s/playlist.m3u8", cameraID)

	response := HLSPlaylistResponse{
		CameraID:    cameraID,
		PlaylistURL: playlistURL,
		TotalChunks: len(chunks),
		Duration:    totalDuration,
	}

	c.JSON(http.StatusOK, response)
}

// Helper functions

func (h *VideoHandler) getChunksForCamera(cameraID string) ([]VideoChunk, error) {
	cameraDir := filepath.Join(h.cfg.VideoOutputDir, cameraID)

	// Check if camera directory exists
	if _, err := os.Stat(cameraDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("no recordings found for camera %s", cameraID)
	}

	// Find all chunk files
	pattern := filepath.Join(cameraDir, "chunk_*.mp4")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list chunk files: %w", err)
	}

	var chunks []VideoChunk
	for _, file := range files {
		stat, err := os.Stat(file)
		if err != nil {
			log.Warn().Err(err).Str("file", file).Msg("Failed to stat chunk file")
			continue
		}

		chunkID := filepath.Base(file)
		startTime := h.parseChunkTimestamp(chunkID)

		chunk := VideoChunk{
			ChunkID:   chunkID,
			CameraID:  cameraID,
			StartTime: startTime,
			Duration:  h.getChunkDuration(),
			FileSize:  stat.Size(),
			FilePath:  file,
			CreatedAt: stat.ModTime(),
		}
		chunks = append(chunks, chunk)
	}

	// Sort chunks by start time (newest first)
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].StartTime.After(chunks[j].StartTime)
	})

	return chunks, nil
}

func (h *VideoHandler) parseChunkTimestamp(chunkID string) time.Time {
	// Parse timestamp from chunk_2024-01-01_12-00-00.mp4
	parts := strings.Split(chunkID, "_")
	if len(parts) >= 3 {
		dateStr := parts[1]
		timeStr := strings.TrimSuffix(parts[2], ".mp4")
		timestampStr := fmt.Sprintf("%s %s", dateStr, strings.ReplaceAll(timeStr, "-", ":"))

		if parsed, err := time.Parse("2006-01-02 15:04:05", timestampStr); err == nil {
			return parsed
		}
	}

	// Fallback to file modification time would be handled by caller
	return time.Now()
}

func (h *VideoHandler) getChunkDuration() float64 {
	return float64(h.cfg.VideoChunkTime)
}

func (h *VideoHandler) generateHLSPlaylist(cameraID string, chunks []VideoChunk) string {
	var playlist strings.Builder

	// HLS header
	playlist.WriteString("#EXTM3U\n")
	playlist.WriteString("#EXT-X-VERSION:3\n")
	playlist.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", h.cfg.VideoChunkTime))
	playlist.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	playlist.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

	// Add each chunk
	for _, chunk := range chunks {
		playlist.WriteString(fmt.Sprintf("#EXTINF:%.1f,\n", chunk.Duration))
		playlist.WriteString(fmt.Sprintf("/videos/%s/chunks/%s/stream\n", cameraID, chunk.ChunkID))
	}

	// End of playlist
	playlist.WriteString("#EXT-X-ENDLIST\n")

	return playlist.String()
}
