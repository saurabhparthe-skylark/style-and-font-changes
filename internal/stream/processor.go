package stream

import (
	"context"
	"fmt"
	"sync"
	"time"

	"kepler-worker/internal/config"
	"kepler-worker/pkg/logger"
)

type ProcessingStats struct {
	FramesProcessed    int64
	FramesSentToAI     int64
	AverageProcessTime time.Duration
	LastProcessTime    time.Time
	DetectionCount     int64
}

type Detection struct {
	BBox       []float32 `json:"bbox"`
	Confidence float32   `json:"confidence"`
	ClassID    int32     `json:"class_id"`
	ClassName  string    `json:"class_name"`
	TrackID    int32     `json:"track_id"`
}

type FrameProcessor struct {
	cameraID string
	config   *config.Config
	logger   *logger.Logger

	frameCount     int64
	lastAIFrame    int64
	processingLock sync.RWMutex

	latestFrame      *Frame
	latestFrameTime  time.Time
	latestDetections []*Detection
	frameMutex       sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc

	stats       ProcessingStats
	statsBuffer []time.Duration
}

func NewFrameProcessor(cameraID string, cfg *config.Config, logger *logger.Logger) (*FrameProcessor, error) {
	ctx, cancel := context.WithCancel(context.Background())

	processor := &FrameProcessor{
		cameraID:    cameraID,
		config:      cfg,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		statsBuffer: make([]time.Duration, 0, 100),
	}

	return processor, nil
}

func (fp *FrameProcessor) ProcessFrame(frame *Frame, frameCount int) error {
	if frame == nil {
		return fmt.Errorf("invalid frame")
	}

	fp.processingLock.Lock()
	fp.frameCount = int64(frameCount)
	fp.processingLock.Unlock()

	startTime := time.Now()

	fp.updateLatestFrame(frame)

	shouldProcessAI := fp.shouldProcessWithAI(frameCount)

	if shouldProcessAI && fp.config.AI.Enabled {
		go fp.processFrameWithAI(frame, int64(frameCount))

		fp.processingLock.Lock()
		fp.stats.FramesSentToAI++
		fp.processingLock.Unlock()
	}

	processingTime := time.Since(startTime)
	fp.updateStats(processingTime)

	return nil
}

func (fp *FrameProcessor) GetLatestFrame() (*Frame, []*Detection, time.Time) {
	fp.frameMutex.RLock()
	defer fp.frameMutex.RUnlock()

	var frameCopy *Frame
	if fp.latestFrame != nil {
		frameCopy = &Frame{
			Data:      make([]byte, len(fp.latestFrame.Data)),
			Width:     fp.latestFrame.Width,
			Height:    fp.latestFrame.Height,
			Timestamp: fp.latestFrame.Timestamp,
		}
		copy(frameCopy.Data, fp.latestFrame.Data)
	}

	var detectionsCopy []*Detection
	if fp.latestDetections != nil {
		detectionsCopy = make([]*Detection, len(fp.latestDetections))
		copy(detectionsCopy, fp.latestDetections)
	}

	return frameCopy, detectionsCopy, fp.latestFrameTime
}

func (fp *FrameProcessor) GetStreamingFrame() (*Frame, bool) {
	fp.frameMutex.RLock()
	defer fp.frameMutex.RUnlock()

	if fp.latestFrame == nil {
		return nil, false
	}

	frameCopy := &Frame{
		Data:      make([]byte, len(fp.latestFrame.Data)),
		Width:     fp.latestFrame.Width,
		Height:    fp.latestFrame.Height,
		Timestamp: fp.latestFrame.Timestamp,
	}
	copy(frameCopy.Data, fp.latestFrame.Data)

	isDuplicate := time.Since(fp.latestFrameTime) > 100*time.Millisecond
	return frameCopy, isDuplicate
}

func (fp *FrameProcessor) GetStats() ProcessingStats {
	fp.processingLock.RLock()
	defer fp.processingLock.RUnlock()
	return fp.stats
}

func (fp *FrameProcessor) Close() error {
	fp.cancel()
	fp.logger.Info("Frame processor closed", "camera_id", fp.cameraID)
	return nil
}

func (fp *FrameProcessor) shouldProcessWithAI(frameCount int) bool {
	return frameCount%fp.config.AI.ProcessEveryN == 0
}

func (fp *FrameProcessor) updateLatestFrame(frame *Frame) {
	fp.frameMutex.Lock()
	defer fp.frameMutex.Unlock()

	if fp.latestFrame != nil && len(fp.latestFrame.Data) > 0 {
		fp.latestFrame.Data = nil
	}

	fp.latestFrame = &Frame{
		Data:      make([]byte, len(frame.Data)),
		Width:     frame.Width,
		Height:    frame.Height,
		Timestamp: frame.Timestamp,
	}
	copy(fp.latestFrame.Data, frame.Data)
	fp.latestFrameTime = time.Now()
}

func (fp *FrameProcessor) processFrameWithAI(frame *Frame, frameID int64) {
	startTime := time.Now()

	time.Sleep(50 * time.Millisecond)

	detections := []*Detection{
		{
			BBox:       []float32{100, 100, 200, 200},
			Confidence: 0.85,
			ClassID:    1,
			ClassName:  "person",
			TrackID:    1,
		},
		{
			BBox:       []float32{300, 150, 400, 250},
			Confidence: 0.92,
			ClassID:    2,
			ClassName:  "helmet",
			TrackID:    2,
		},
	}

	fp.frameMutex.Lock()
	fp.latestDetections = detections
	fp.frameMutex.Unlock()

	processingTime := time.Since(startTime)
	fp.logger.Debug("AI processing completed",
		"camera_id", fp.cameraID,
		"frame_id", frameID,
		"detections", len(detections),
		"processing_time", processingTime,
	)

	fp.processingLock.Lock()
	fp.stats.DetectionCount += int64(len(detections))
	fp.processingLock.Unlock()
}

func (fp *FrameProcessor) updateStats(processingTime time.Duration) {
	fp.processingLock.Lock()
	defer fp.processingLock.Unlock()

	fp.stats.FramesProcessed++
	fp.stats.LastProcessTime = time.Now()

	fp.statsBuffer = append(fp.statsBuffer, processingTime)
	if len(fp.statsBuffer) > 100 {
		fp.statsBuffer = fp.statsBuffer[1:]
	}

	if len(fp.statsBuffer) > 0 {
		var total time.Duration
		for _, t := range fp.statsBuffer {
			total += t
		}
		fp.stats.AverageProcessTime = total / time.Duration(len(fp.statsBuffer))
	}
}
