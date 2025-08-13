package logging

import (
	"fmt"
	"io"
	"strconv"

	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"

	"github.com/logdyhq/logdy-core/logdy"
)

type logdyWriter struct {
	logger logdy.Logdy
}

func (w *logdyWriter) Write(p []byte) (n int, err error) {
	// Forward raw line to Logdy UI
	w.logger.LogString(string(p))
	return len(p), nil
}

// StartLogdy starts embedded Logdy web UI and returns a writer to tee logs, plus the UI URL
func StartLogdy(cfg *config.Config) (io.Writer, string, error) {
	portStr := strconv.Itoa(cfg.LogdyPort)
	ld := logdy.InitializeLogdy(logdy.Config{
		ServerIp:   cfg.LogdyHost,
		ServerPort: portStr,
	}, nil)

	url := fmt.Sprintf("http://%s:%s", cfg.LogdyHost, portStr)
	log.Info().Str("url", url).Msg("Logdy UI available")
	return &logdyWriter{logger: ld}, url, nil
}
