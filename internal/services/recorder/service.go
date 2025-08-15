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
	"kepler-worker-go/internal/models"
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
	lastSegment   string
	frameChannel  chan *models.ProcessedFrame
	segmentWriter *SegmentWriter
	frameCount    int64
	lastFrameTime time.Time
	chunkDuration time.Duration // Duration for each video chunk
}

// SegmentWriter handles writing frames to video chunks
type SegmentWriter struct {
	chunkDuration time.Duration
	currentChunk  int
	chunkStart    time.Time
	outputDir     string
	cameraID      string
	stdin         io.WriteCloser
	frameWidth    int
	frameHeight   int
	rotating      bool // Flag to indicate if rotation is in progress
}

type ChunkMetadata struct {
	CameraID   string    `json:"camera_id"`
	ChunkID    string    `json:"chunk_id"`
	ChunkPath  string    `json:"chunk_path"`
	StartTime  time.Time `json:"start_time"`
	Duration   float64   `json:"duration"`
	FileSize   int64     `json:"file_size"`
	FrameCount int64     `json:"frame_count"`
}

func NewService(cfg *config.Config, messageSvc *messaging.Service) *Service {
	service := &Service{
		cfg:        cfg,
		messageSvc: messageSvc,
		recorders:  make(map[string]*CameraRecorder),
		stopCh:     make(chan struct{}),
	}

	log.Info().Msg("Recorder service initialized (filesystem only)")
	return service
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

	// Use chunk duration from config (default: 60 seconds)
	chunkDuration := time.Duration(rs.cfg.VideoChunkTime) * time.Second

	recorder := &CameraRecorder{
		CameraID:      cameraID,
		OutputDir:     outputDir,
		StartedAt:     time.Now(),
		stopCh:        make(chan struct{}),
		frameChannel:  make(chan *models.ProcessedFrame, 200), // Larger buffer for chunks
		lastFrameTime: time.Now(),
		chunkDuration: chunkDuration,
		segmentWriter: &SegmentWriter{
			chunkDuration: chunkDuration,
			outputDir:     outputDir,
			cameraID:      cameraID,
			frameWidth:    rs.cfg.OutputWidth,
			frameHeight:   rs.cfg.OutputHeight,
		},
	}

	recorder.IsRecording = true
	rs.recorders[cameraID] = recorder

	// Start background workers for this camera
	go rs.processFrames(recorder)
	go rs.manageChunks(recorder) // Changed from uploadSegments to manageChunks

	log.Info().
		Str("camera_id", cameraID).
		Str("output_dir", outputDir).
		Int("width", rs.cfg.OutputWidth).
		Int("height", rs.cfg.OutputHeight).
		Dur("chunk_duration", chunkDuration).
		Msg("Started chunk-based recording")
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
	var processedFrame *models.ProcessedFrame
	switch f := frame.(type) {
	case *models.ProcessedFrame:
		processedFrame = f
	default:
		return fmt.Errorf("unsupported frame type for camera %s", cameraID)
	}

	// Update recorder statistics
	recorder.frameCount++
	recorder.lastFrameTime = time.Now()

	// Send frame to processing channel (non-blocking)
	select {
	case recorder.frameChannel <- processedFrame:
		return nil
	default:
		// If channel is full, drop older frames to prevent blocking
		// Drain some frames first
		select {
		case <-recorder.frameChannel:
			// Dropped one frame, now try to add the new one
			select {
			case recorder.frameChannel <- processedFrame:
				return nil
			default:
				log.Debug().Str("camera_id", cameraID).Msg("Dropped frame for recording (channel still full)")
			}
		default:
		}
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
		if recorder.segmentWriter != nil && recorder.segmentWriter.stdin != nil {
			recorder.segmentWriter.stdin.Close()
		}
	}()

	// Initialize FFmpeg process for writing frames
	if err := rs.startChunkBasedFFmpeg(recorder); err != nil {
		log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to start chunk-based FFmpeg")
		return
	}

	frameCounter := 0
	for {
		select {
		case <-recorder.stopCh:
			return
		case frame := <-recorder.frameChannel:
			frameCounter++
			// Only process every 3rd frame to reduce processing load
			if frameCounter%3 != 0 {
				continue
			}

			// Process frame into video chunk
			if err := rs.writeFrameToChunk(recorder, frame); err != nil {
				log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to write frame to chunk")
				// Don't return here, continue processing other frames
			}
		}
	}
}

func (rs *Service) startChunkBasedFFmpeg(recorder *CameraRecorder) error {
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	chunkPath := filepath.Join(recorder.OutputDir, fmt.Sprintf("chunk_%s.mp4", timestamp))

	// Use actual frame dimensions from config
	frameSize := fmt.Sprintf("%dx%d", recorder.segmentWriter.frameWidth, recorder.segmentWriter.frameHeight)

	args := []string{
		"-f", "rawvideo",
		"-pix_fmt", "bgr24", // OpenCV default format
		"-s", frameSize,
		"-r", "10", // Reduced frame rate for chunks
		"-i", "-", // Read from stdin
		"-c:v", "libx264",
		"-preset", "medium", // Better quality than veryfast
		"-crf", "23", // Good quality
		"-movflags", "+faststart", // Optimize for streaming
		"-f", "mp4",
		"-loglevel", "warning",
		chunkPath,
	}

	recorder.Process = exec.Command("ffmpeg", args...)
	recorder.Process.Dir = recorder.OutputDir

	// Set up stdin pipe for frame data
	stdin, err := recorder.Process.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	recorder.segmentWriter.stdin = stdin

	// Start the process
	if err := recorder.Process.Start(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	log.Info().
		Str("camera_id", recorder.CameraID).
		Str("frame_size", frameSize).
		Str("chunk_path", chunkPath).
		Msg("FFmpeg process started for chunk recording")

	return nil
}

func (rs *Service) writeFrameToChunk(recorder *CameraRecorder, frame *models.ProcessedFrame) error {
	// Check if stdin is available and not closed
	if recorder.segmentWriter == nil || recorder.segmentWriter.stdin == nil {
		return fmt.Errorf("FFmpeg stdin not available")
	}

	// Don't write if rotation is in progress
	if recorder.segmentWriter.rotating {
		return nil // Silently skip frames during rotation
	}

	// Convert frame data to proper size if needed
	frameData := frame.Data
	expectedSize := recorder.segmentWriter.frameWidth * recorder.segmentWriter.frameHeight * 3 // BGR24

	if len(frameData) != expectedSize {
		log.Debug().
			Str("camera_id", recorder.CameraID).
			Int("actual_size", len(frameData)).
			Int("expected_size", expectedSize).
			Msg("Frame size mismatch - skipping frame")
		return nil // Skip malformed frames
	}

	// Check if the process is still running and stdin is still writable
	if recorder.Process != nil && recorder.Process.Process != nil {
		if recorder.Process.ProcessState != nil && recorder.Process.ProcessState.Exited() {
			return fmt.Errorf("FFmpeg process has exited")
		}
	}

	// Write frame data to FFmpeg stdin with error handling
	_, err := recorder.segmentWriter.stdin.Write(frameData)
	if err != nil {
		// Don't log this as an error if it's just a closed pipe during rotation
		if err.Error() == "write |1: file already closed" {
			return nil // Silently ignore closed pipe during rotation
		}
		return fmt.Errorf("failed to write frame data to FFmpeg: %w", err)
	}

	return nil
}

// manageChunks handles chunk rotation and metadata publishing
func (rs *Service) manageChunks(recorder *CameraRecorder) {
	ticker := time.NewTicker(recorder.chunkDuration) // Rotate chunks based on duration
	defer ticker.Stop()

	for {
		select {
		case <-recorder.stopCh:
			return
		case <-rs.stopCh:
			return
		case <-ticker.C:
			// Rotate to new chunk
			if err := rs.rotateChunk(recorder); err != nil {
				log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to rotate chunk")
			}
		}
	}
}

func (rs *Service) rotateChunk(recorder *CameraRecorder) error {
	log.Info().Str("camera_id", recorder.CameraID).Msg("Rotating to new chunk")

	// Create a temporary flag to prevent new writes during rotation
	recorder.segmentWriter.rotating = true

	// Give a brief moment for any in-flight writes to complete
	time.Sleep(100 * time.Millisecond)

	// Stop current FFmpeg process gracefully
	if recorder.segmentWriter.stdin != nil {
		recorder.segmentWriter.stdin.Close()
		recorder.segmentWriter.stdin = nil // Set to nil to prevent further writes
	}

	if recorder.Process != nil && recorder.Process.Process != nil {
		// Send SIGTERM and wait for completion
		if err := recorder.Process.Process.Signal(os.Interrupt); err != nil {
			log.Warn().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to send interrupt to FFmpeg")
		}

		done := make(chan error, 1)
		go func() {
			done <- recorder.Process.Wait()
		}()

		select {
		case <-time.After(5 * time.Second):
			recorder.Process.Process.Kill()
			log.Warn().Str("camera_id", recorder.CameraID).Msg("Force killed FFmpeg during rotation")
		case <-done:
			log.Debug().Str("camera_id", recorder.CameraID).Msg("FFmpeg process ended gracefully")
		}
	}

	// Start new FFmpeg process for next chunk
	if err := rs.startChunkBasedFFmpeg(recorder); err != nil {
		return fmt.Errorf("failed to start new chunk FFmpeg: %w", err)
	}

	// Clear the rotation flag
	recorder.segmentWriter.rotating = false

	// Publish metadata about completed chunk (if any)
	if err := rs.publishChunkMetadata(recorder); err != nil {
		log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to publish chunk metadata")
	}

	// Clean up old chunks to avoid disk space issues
	if err := rs.cleanupOldChunks(recorder); err != nil {
		log.Error().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to cleanup old chunks")
	}

	return nil
}

func (rs *Service) publishChunkMetadata(recorder *CameraRecorder) error {
	// Find the most recent chunk file
	pattern := filepath.Join(recorder.OutputDir, "chunk_*.mp4")
	chunks, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find chunks: %w", err)
	}

	if len(chunks) == 0 {
		return nil // No chunks yet
	}

	// Get the most recent chunk (by modification time)
	var latestChunk string
	var latestTime time.Time
	for _, chunk := range chunks {
		if stat, err := os.Stat(chunk); err == nil {
			if stat.ModTime().After(latestTime) {
				latestTime = stat.ModTime()
				latestChunk = chunk
			}
		}
	}

	if latestChunk == "" {
		return nil
	}

	// Get file info
	fileInfo, err := os.Stat(latestChunk)
	if err != nil {
		return fmt.Errorf("failed to get chunk file info: %w", err)
	}

	// Create metadata
	chunkName := filepath.Base(latestChunk)
	metadata := ChunkMetadata{
		CameraID:   recorder.CameraID,
		ChunkID:    chunkName,
		ChunkPath:  latestChunk,
		StartTime:  latestTime.Add(-recorder.chunkDuration), // Approximate start time
		Duration:   recorder.chunkDuration.Seconds(),
		FileSize:   fileInfo.Size(),
		FrameCount: recorder.frameCount, // Approximate
	}

	// Publish metadata via NATS
	subject := fmt.Sprintf("video.chunks.%s", recorder.CameraID)
	if err := rs.messageSvc.Publish(subject, metadata); err != nil {
		log.Error().Err(err).Msg("Failed to publish chunk metadata")
	} else {
		log.Info().
			Str("camera_id", recorder.CameraID).
			Str("chunk", chunkName).
			Int64("size_bytes", fileInfo.Size()).
			Dur("duration", recorder.chunkDuration).
			Msg("Published chunk metadata")
	}

	return nil
}

// cleanupOldChunks removes old chunks to prevent disk space issues
func (rs *Service) cleanupOldChunks(recorder *CameraRecorder) error {
	pattern := filepath.Join(recorder.OutputDir, "chunk_*.mp4")
	chunks, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to find chunks: %w", err)
	}

	// Only clean up if we have more than the configured max chunks
	if len(chunks) <= rs.cfg.VideoMaxChunks {
		return nil
	}

	// Sort chunks by modification time (oldest first)
	type chunkInfo struct {
		path    string
		modTime time.Time
	}

	var chunkList []chunkInfo
	for _, chunk := range chunks {
		if stat, err := os.Stat(chunk); err == nil {
			chunkList = append(chunkList, chunkInfo{
				path:    chunk,
				modTime: stat.ModTime(),
			})
		}
	}

	// Sort by modification time (oldest first)
	for i := 0; i < len(chunkList)-1; i++ {
		for j := i + 1; j < len(chunkList); j++ {
			if chunkList[i].modTime.After(chunkList[j].modTime) {
				chunkList[i], chunkList[j] = chunkList[j], chunkList[i]
			}
		}
	}

	// Remove oldest chunks (keep only VideoMaxChunks)
	chunksToRemove := len(chunkList) - rs.cfg.VideoMaxChunks
	removedCount := 0

	for i := 0; i < chunksToRemove && i < len(chunkList); i++ {
		if err := os.Remove(chunkList[i].path); err != nil {
			log.Warn().Err(err).Str("chunk_path", chunkList[i].path).Msg("Failed to remove old chunk")
		} else {
			removedCount++
		}
	}

	if removedCount > 0 {
		log.Info().
			Str("camera_id", recorder.CameraID).
			Int("removed_chunks", removedCount).
			Int("total_chunks", len(chunkList)).
			Int("max_chunks", rs.cfg.VideoMaxChunks).
			Msg("Cleaned up old video chunks")
	}

	return nil
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

// RestartRecording restarts a failed recording
func (rs *Service) RestartRecording(cameraID string) error {
	rs.mutex.Lock()
	defer rs.mutex.Unlock()

	recorder, exists := rs.recorders[cameraID]
	if !exists {
		return fmt.Errorf("camera %s is not being recorded", cameraID)
	}

	if recorder.IsRecording {
		log.Info().Str("camera_id", cameraID).Msg("Stopping existing recording before restart")
		rs.stopRecordingInternal(recorder)
	}

	// Create a new recorder with the same settings
	newRecorder := &CameraRecorder{
		CameraID:      cameraID,
		OutputDir:     recorder.OutputDir,
		StartedAt:     time.Now(),
		stopCh:        make(chan struct{}),
		frameChannel:  make(chan *models.ProcessedFrame, 100),
		lastFrameTime: time.Now(),
		chunkDuration: recorder.chunkDuration, // Keep the same chunk duration
		segmentWriter: &SegmentWriter{
			chunkDuration: recorder.chunkDuration,
			outputDir:     recorder.OutputDir,
			cameraID:      cameraID,
			frameWidth:    rs.cfg.OutputWidth,
			frameHeight:   rs.cfg.OutputHeight,
		},
	}

	newRecorder.IsRecording = true
	rs.recorders[cameraID] = newRecorder

	// Start background workers for this camera
	go rs.processFrames(newRecorder)
	go rs.manageChunks(newRecorder) // Changed from uploadSegments to manageChunks

	log.Info().Str("camera_id", cameraID).Msg("Successfully restarted recording")
	return nil
}

// stopRecordingInternal stops recording without mutex lock (internal use)
func (rs *Service) stopRecordingInternal(recorder *CameraRecorder) {
	if recorder == nil {
		return
	}

	recorder.IsRecording = false

	// Close channels
	if recorder.stopCh != nil {
		close(recorder.stopCh)
	}
	if recorder.frameChannel != nil {
		close(recorder.frameChannel)
	}

	// Close stdin pipe
	if recorder.segmentWriter != nil && recorder.segmentWriter.stdin != nil {
		recorder.segmentWriter.stdin.Close()
	}

	// Stop FFmpeg process
	if recorder.Process != nil && recorder.Process.Process != nil {
		// Send SIGTERM for graceful shutdown
		if err := recorder.Process.Process.Signal(os.Interrupt); err != nil {
			log.Warn().Err(err).Str("camera_id", recorder.CameraID).Msg("Failed to send interrupt signal")
		}

		// Wait a bit for graceful shutdown
		done := make(chan error, 1)
		go func() {
			done <- recorder.Process.Wait()
		}()

		select {
		case <-time.After(3 * time.Second):
			// Force kill if not stopped gracefully
			recorder.Process.Process.Kill()
			log.Warn().Str("camera_id", recorder.CameraID).Msg("Force killed FFmpeg process during internal stop")
		case err := <-done:
			if err != nil {
				log.Debug().Err(err).Str("camera_id", recorder.CameraID).Msg("FFmpeg process ended during internal stop")
			}
		}
	}
}

// HealthCheck performs a health check on all active recordings
func (rs *Service) HealthCheck() map[string]string {
	rs.mutex.RLock()
	defer rs.mutex.RUnlock()

	health := make(map[string]string)
	for cameraID, recorder := range rs.recorders {
		if !recorder.IsRecording {
			health[cameraID] = "stopped"
			continue
		}

		// Check if recording is stale (no frames for a while)
		if time.Since(recorder.lastFrameTime) > 30*time.Second {
			health[cameraID] = "stale"
			continue
		}

		// Check if FFmpeg process is still running
		if recorder.Process == nil || recorder.Process.Process == nil {
			health[cameraID] = "no_process"
			continue
		}

		// Check process state
		if recorder.Process.ProcessState != nil && recorder.Process.ProcessState.Exited() {
			health[cameraID] = "process_exited"
			continue
		}

		health[cameraID] = "healthy"
	}

	return health
}
