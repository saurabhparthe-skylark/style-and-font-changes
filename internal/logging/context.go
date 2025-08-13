package logging

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ctxKey string

const (
	ctxRequestID ctxKey = "request_id"
	ctxStartTime ctxKey = "start_time"
	ctxUserID    ctxKey = "user_id"
)

func withGinContext(c *gin.Context, e *zerolog.Event) *zerolog.Event {
	if c == nil {
		return e
	}
	if v, ok := c.Get(string(ctxRequestID)); ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			e.Str("request_id", s)
		}
	}
	if v, ok := c.Get(string(ctxUserID)); ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			e.Str("user_id", s)
		}
	}
	if v, ok := c.Get(string(ctxStartTime)); ok {
		if t, ok2 := v.(time.Time); ok2 {
			e.Dur("duration", time.Since(t))
		}
	}
	return e
}

func Info(c *gin.Context) *zerolog.Event  { return withGinContext(c, log.Info()) }
func Debug(c *gin.Context) *zerolog.Event { return withGinContext(c, log.Debug()) }
func Warn(c *gin.Context) *zerolog.Event  { return withGinContext(c, log.Warn()) }
func Error(c *gin.Context) *zerolog.Event { return withGinContext(c, log.Error()) }
