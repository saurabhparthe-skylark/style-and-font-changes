package logger

import (
	"log/slog"
	"os"
)

// Logger is a structured logger wrapper
type Logger struct {
	*slog.Logger
	workerID string
}

// New creates a new logger for the worker
func New(workerID string) *Logger {
	// Create structured logger with JSON output
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	logger := slog.New(handler)

	// Add worker ID to all log entries
	logger = logger.With("worker_id", workerID)

	return &Logger{
		Logger:   logger,
		workerID: workerID,
	}
}

// WithCamera returns a logger with camera context
func (l *Logger) WithCamera(cameraID string) *Logger {
	return &Logger{
		Logger:   l.Logger.With("camera_id", cameraID),
		workerID: l.workerID,
	}
}

// WithComponent returns a logger with component context
func (l *Logger) WithComponent(component string) *Logger {
	return &Logger{
		Logger:   l.Logger.With("component", component),
		workerID: l.workerID,
	}
}
