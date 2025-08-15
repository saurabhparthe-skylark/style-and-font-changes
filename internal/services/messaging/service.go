package messaging

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"kepler-worker-go/internal/config"
)

type Service struct {
	conn *nats.Conn
	cfg  *config.Config
}

func NewService(cfg *config.Config) (*Service, error) {
	opts := []nats.Option{
		nats.Name("kepler-worker"),
		nats.Timeout(cfg.NatsConnectTimeout),
		nats.ReconnectWait(cfg.NatsReconnectWait),
		nats.MaxReconnects(cfg.NatsMaxReconnects),
	}

	conn, err := nats.Connect(cfg.NatsURL, opts...)
	if err != nil {
		return nil, err
	}

	log.Info().Str("url", cfg.NatsURL).Msg("NATS connection established")

	return &Service{
		conn: conn,
		cfg:  cfg,
	}, nil
}

func (s *Service) Publish(subject string, data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	return s.conn.Publish(subject, payload)
}

func (s *Service) Subscribe(subject string, handler func([]byte)) (*nats.Subscription, error) {
	return s.conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

func (s *Service) QueueSubscribe(subject, queue string, handler func([]byte)) (*nats.Subscription, error) {
	return s.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		handler(msg.Data)
	})
}

func (s *Service) IsConnected() bool {
	return s.conn != nil && s.conn.IsConnected()
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s.conn != nil {
		// Try graceful drain with timeout, fallback to immediate close
		if err := s.conn.Drain(); err != nil {
			log.Warn().Err(err).Msg("Failed to drain NATS connection gracefully, closing immediately")
			s.conn.Close()
		}
	}
	return nil
}
