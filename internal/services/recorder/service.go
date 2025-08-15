package recorder

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/services/messaging"
)

type Service struct {
	cfg           *config.Config
	messageSvc    *messaging.Service
	recorders     map[string]*CameraRecorder
	mutex         sync.RWMutex
	stopCh        chan struct{}
	processedSegs sync.Map // Track processed segments to avoid duplicates
}

type CameraRecorder struct {
	CameraID      string
	OutputDir     string
	Process       *exec.Cmd
	IsRecording   bool
	StartedAt     time.Time
	stopCh        chan struct{}
	lastSegment   string // Track last processed segment
	frameChannel  chan *ProcessedFrame
	segmentWriter *SegmentWriter
}

// ProcessedFrame represents a frame ready for recording
type ProcessedFrame struct {
	FrameID   int64
	Timestamp time.Time
	Data      []byte
	Width     int
	Height    int
	CameraID  string
}

// SegmentWriter handles writing frames to video segments
type SegmentWriter struct {
	segmentDuration time.Duration
	currentSegment  int
	segmentStart    time.Time
	outputDir       string
	cameraID        string
	stdin           io.WriteCloser
}

type ChunkMetadata struct {
	CameraID     string    `json:"camera_id"`
	ChunkID      string    `json:"chunk_id"`
	SegmentPath  string    `json:"segment_path"`
	PlaylistPath string    `json:"playlist_path"`
	MinIOPath    string    `json:"minio_path"`
	StartTime    time.Time `json:"start_time"`
	Duration     float64   `json:"duration"`
	SegmentCount int       `json:"segment_count"`
	FileSize     int64     `json:"file_size"`
}

func NewService(cfg *config.Config, messageSvc *messaging.Service) *Service {
	return &Service{
		cfg:        cfg,
		messageSvc: messageSvc,
		recorders:  make(map[string]*CameraRecorder),
		stopCh:     make(chan struct{}),
	}
}

func (rs *Service) StartRecording(cameraID string) error {
	rs.mutex.Lock()
	defer rs.mutex.Unlock()

	if recorder, exists := rs.recorders[cameraID]; exists && recorder.IsRecording {
		return fmt.Errorf("camera %s is already being recorded", cameraID)
	}

	outputDir := filepath.Join(rs.cfg.VideoOutputDir, cameraID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	recorder := &CameraRecorder{
		CameraID:     cameraID,
		OutputDir:    outputDir,
		StartedAt:    time.Now(),
		stopCh:       make(chan struct{}),
		frameChannel: make(chan *ProcessedFrame, 100), // Buffer for frames
		segmentWriter: &SegmentWriter{
			segmentDuration: time.Duration(rs.cfg.VideoSegmentTime) * time.Second,
			outputDir:       outputDir,
			cameraID:        cameraID,
		},
	}

	recorder.IsRecording = true
	rs.recorders[cameraID] = recorder

	// Start background workers for this camera
	go rs.processFrames(recorder)
	go rs.uploadSegments(recorder)

	log.Info().Str("camera_id", cameraID).Str("output_dir", outputDir).Msg("Started frame-based recording")
	return nil
}

// ProcessFrame processes a single frame for recording
func (rs *Service) ProcessFrame(cameraID string, frame interface{}) error {
	rs.mutex.RLock()
	recorder, exists := rs.recorders[cameraID]
	rs.mutex.RUnlock()

	if !exists || !recorder.IsRecording {
		return fmt.Errorf("camera %s is not recording", cameraID)
	}

	// Convert the frame to our ProcessedFrame format
	// Note: This would need to be adapted based on your actual frame structure
	processedFrame := &ProcessedFrame{
		CameraID:  cameraID,
		Timestamp: time.Now(),
		// TODO: Extract actual frame data, dimensions, etc. from your frame structure
		// This depends on your models.ProcessedFrame structure
	}

	// Send frame to processing channel (non-blocking)
	select {
	case recorder.frameChannel <- processedFrame:
		return nil
	default:
		// If channel is full, drop the frame to prevent blocking
		log.Debug().Str("camera_id", cameraID).Msg("Dropped frame for recording (channel full)")
		return nil
	}
}

// processFrames handles frame-to-video conversion for a camera
func (rs *Service) processFrames(recorder *CameraRecorder) {
	defer func() {
		recorder.IsRecording = false
		if recorder.Process != nil && recorder.Process.Process != nil {
			recorder.Process.Process.Kill()
		}
	}()

	// Initialize FFmpeg process for writing frames
	if err := rs.startFrameBasedFFmpeg(recorder); err != nil {
		log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to start frame-based FFmpeg")
		return
	}

	for {
		select {
		case <-recorder.stopCh:
			return
		case frame := <-recorder.frameChannel:
			// Process frame into video segment
			if err := rs.writeFrameToSegment(recorder, frame); err != nil {
				log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to write frame to segment")
			}
		}
	}
}

func (rs *Service) startFrameBasedFFmpeg(recorder *CameraRecorder) error {
	// Create a named pipe or use stdin for frame input
	playlistPath := filepath.Join(recorder.OutputDir, "index.m3u8")
	segmentPattern := filepath.Join(recorder.OutputDir, "segment_%03d.ts")

	args := []string{
		"-f", "rawvideo",
		"-pix_fmt", "bgr24", // OpenCV default format
		"-s", "640x480", // TODO: Use actual frame dimensions
		"-r", "12", // Input framerate
		"-i", "-", // Read from stdin
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-g", "24",
		"-sc_threshold", "0",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", rs.cfg.VideoSegmentTime),
		"-hls_list_size", fmt.Sprintf("%d", rs.cfg.VideoMaxSegments),
		"-hls_flags", "delete_segments+independent_segments",
		"-hls_segment_filename", segmentPattern,
		"-hls_allow_cache", "0",
		"-loglevel", "error",
		playlistPath,
	}

	recorder.Process = exec.Command("ffmpeg", args...)
	recorder.Process.Dir = recorder.OutputDir

	// Set up stdin pipe for frame data
	stdin, err := recorder.Process.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	recorder.segmentWriter.stdin = stdin

	return recorder.Process.Start()
}

func (rs *Service) writeFrameToSegment(recorder *CameraRecorder, frame *ProcessedFrame) error {
	// TODO: Write actual frame data to FFmpeg stdin
	// This would involve converting your frame format to raw video data
	// For now, we'll just track the frame

	log.Debug().
		Str("camera_id", recorder.CameraID).
		Int64("frame_id", frame.FrameID).
		Msg("Processing frame for recording")

	return nil
}

func (rs *Service) uploadSegments(recorder *CameraRecorder) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-recorder.stopCh:
			return
		case <-rs.stopCh:
			return
		case <-ticker.C:
			if err := rs.processNewSegments(recorder); err != nil {
				log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to process segments")
			}
		}
	}
}

func (rs *Service) processNewSegments(recorder *CameraRecorder) error {
	pattern := filepath.Join(recorder.OutputDir, "segment_*.ts")
	segments, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find segments: %w", err)
	}

	for _, segmentFile := range segments {
		if rs.isSegmentProcessed(segmentFile) {
			continue
		}

		// Check if file is completely written (not being written by ffmpeg)
		if !rs.isFileReady(segmentFile) {
			continue
		}

		if err := rs.uploadSegmentToMinIO(recorder, segmentFile); err != nil {
			log.Error().Err(err).Str("segment", segmentFile).Msg("Failed to upload segment")
			continue
		}

		rs.markSegmentProcessed(segmentFile)
		recorder.lastSegment = segmentFile
	}

	// Upload playlist if it exists and has been updated
	playlistPath := filepath.Join(recorder.OutputDir, "index.m3u8")
	if stat, err := os.Stat(playlistPath); err == nil {
		// Only upload playlist if it's been modified recently
		if time.Since(stat.ModTime()) < 5*time.Second {
			rs.uploadPlaylistToMinIO(recorder, playlistPath)
		}
	}

	return nil
}

func (rs *Service) isFileReady(filePath string) bool {
	// Check if file size is stable (not being written)
	stat1, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	time.Sleep(100 * time.Millisecond)

	stat2, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	return stat1.Size() == stat2.Size() && stat1.ModTime().Equal(stat2.ModTime())
}

func (rs *Service) uploadSegmentToMinIO(recorder *CameraRecorder, segmentFile string) error {
	// TODO: Implement actual MinIO upload using MinIO Go SDK
	// For now, we'll just simulate the upload and send metadata

	segmentName := filepath.Base(segmentFile)
	minioPath := fmt.Sprintf("videos/%s/%s", recorder.CameraID, segmentName)

	fileInfo, err := os.Stat(segmentFile)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Extract segment number for better start time estimation
	startTime := recorder.StartedAt.Add(time.Duration(rs.extractSegmentNumber(segmentName)) * time.Duration(rs.cfg.VideoSegmentTime) * time.Second)

	metadata := ChunkMetadata{
		CameraID:     recorder.CameraID,
		ChunkID:      segmentName,
		SegmentPath:  segmentFile,
		MinIOPath:    minioPath,
		StartTime:    startTime,
		Duration:     float64(rs.cfg.VideoSegmentTime),
		SegmentCount: 1,
		FileSize:     fileInfo.Size(),
	}

	subject := fmt.Sprintf("video.segments.%s", recorder.CameraID)
	if err := rs.messageSvc.Publish(subject, metadata); err != nil {
		return fmt.Errorf("failed to publish segment metadata: %w", err)
	}

	log.Debug().
		Str("camera_id", recorder.CameraID).
		Str("segment", segmentName).
		Str("minio_path", minioPath).
		Int64("size_bytes", fileInfo.Size()).
		Msg("Processed segment for upload")

	return nil
}

func (rs *Service) extractSegmentNumber(segmentName string) int {
	// Extract number from segment_000.ts format
	var segNum int
	fmt.Sscanf(segmentName, "segment_%d.ts", &segNum)
	return segNum
}

func (rs *Service) uploadPlaylistToMinIO(recorder *CameraRecorder, playlistPath string) error {
	// TODO: Implement MinIO playlist upload
	minioPath := fmt.Sprintf("videos/%s/index.m3u8", recorder.CameraID)

	metadata := map[string]interface{}{
		"camera_id":     recorder.CameraID,
		"playlist_path": minioPath,
		"updated_at":    time.Now(),
		"type":          "live_playlist",
	}

	subject := fmt.Sprintf("video.playlists.%s", recorder.CameraID)
	if err := rs.messageSvc.Publish(subject, metadata); err != nil {
		log.Error().Err(err).Msg("Failed to publish playlist metadata")
		return err
	}

	return nil
}

func (rs *Service) isSegmentProcessed(segmentFile string) bool {
	_, exists := rs.processedSegs.Load(segmentFile)
	return exists
}

func (rs *Service) markSegmentProcessed(segmentFile string) {
	rs.processedSegs.Store(segmentFile, time.Now())
}

func (rs *Service) StopRecording(cameraID string) error {
	rs.mutex.Lock()
	defer rs.mutex.Unlock()

	recorder, exists := rs.recorders[cameraID]
	if !exists || !recorder.IsRecording {
		return fmt.Errorf("camera %s is not being recorded", cameraID)
	}

	close(recorder.stopCh)

	// Close frame channel
	close(recorder.frameChannel)

	// Close stdin pipe if it exists
	if recorder.segmentWriter != nil && recorder.segmentWriter.stdin != nil {
		recorder.segmentWriter.stdin.Close()
	}

	if recorder.Process != nil && recorder.Process.Process != nil {
		// Send SIGTERM for graceful shutdown
		if err := recorder.Process.Process.Signal(os.Interrupt); err != nil {
			log.Warn().Err(err).Str("camera_id", cameraID).Msg("Failed to send interrupt signal")
		}

		// Wait a bit for graceful shutdown
		done := make(chan error, 1)
		go func() {
			done <- recorder.Process.Wait()
		}()

		select {
		case <-time.After(5 * time.Second):
			// Force kill if not stopped gracefully
			recorder.Process.Process.Kill()
			log.Warn().Str("camera_id", cameraID).Msg("Force killed FFmpeg process")
		case err := <-done:
			if err != nil {
				log.Debug().Err(err).Str("camera_id", cameraID).Msg("FFmpeg process ended")
			}
		}
	}

	// Clean up processed segments for this camera
	rs.processedSegs.Range(func(key, value interface{}) bool {
		if segmentPath, ok := key.(string); ok {
			if filepath.Dir(segmentPath) == recorder.OutputDir {
				rs.processedSegs.Delete(key)
			}
		}
		return true
	})

	delete(rs.recorders, cameraID)
	log.Info().Str("camera_id", cameraID).Msg("Stopped frame-based recording")
	return nil
}

func (rs *Service) GetRecordingStatus(cameraID string) (bool, error) {
	rs.mutex.RLock()
	defer rs.mutex.RUnlock()

	recorder, exists := rs.recorders[cameraID]
	if !exists {
		return false, nil
	}

	return recorder.IsRecording, nil
}

func (rs *Service) GetAllRecordings() map[string]bool {
	rs.mutex.RLock()
	defer rs.mutex.RUnlock()

	status := make(map[string]bool)
	for cameraID, recorder := range rs.recorders {
		status[cameraID] = recorder.IsRecording
	}

	return status
}

func (rs *Service) Shutdown(ctx context.Context) error {
	close(rs.stopCh)

	rs.mutex.Lock()
	defer rs.mutex.Unlock()

	for cameraID := range rs.recorders {
		if err := rs.StopRecording(cameraID); err != nil {
			log.Error().Err(err).Str("camera_id", cameraID).Msg("Failed to stop recording during shutdown")
		}
	}

	return nil
}
