package publisher

import (
	"context"
	"net/http"

	"kepler-worker-go/internal/config"
	"kepler-worker-go/internal/models"
	"kepler-worker-go/internal/services/publisher/mjpeg"
	"kepler-worker-go/internal/services/publisher/webrtc"
)

type Service struct {
	cfg             *config.Config
	mjpegPublisher  *mjpeg.Publisher
	webrtcPublisher *webrtc.Publisher
}

func NewService(cfg *config.Config) (*Service, error) {
	mjpegPub, err := mjpeg.NewPublisher(cfg)
	if err != nil {
		return nil, err
	}

	webrtcPub, err := webrtc.NewPublisher(cfg)
	if err != nil {
		return nil, err
	}

	return &Service{
		cfg:             cfg,
		mjpegPublisher:  mjpegPub,
		webrtcPublisher: webrtcPub,
	}, nil
}

func (s *Service) PublishFrame(frame *models.ProcessedFrame) error {
	if err := s.mjpegPublisher.PublishFrame(frame); err != nil {
		return err
	}
	return s.webrtcPublisher.PublishFrame(frame)
}

func (s *Service) StreamMJPEGHTTP(w http.ResponseWriter, r *http.Request, cameraID string) {
	s.mjpegPublisher.StreamMJPEGHTTP(w, r, cameraID)
}

func (s *Service) StopStream(cameraID string) error {
	return s.webrtcPublisher.StopStream(cameraID)
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mjpegPublisher.Shutdown()
	return s.webrtcPublisher.Shutdown(ctx)
}

func (s *Service) GetStreamURL(cameraID string) string {
	return s.webrtcPublisher.GetStreamURL(cameraID)
}

func (s *Service) GetWebRTCURL(cameraID string) string {
	return s.webrtcPublisher.GetWebRTCURL(cameraID)
}

func (s *Service) GetWebRTCPublishURL(cameraID string) string {
	return s.webrtcPublisher.GetWebRTCPublishURL(cameraID)
}

func (s *Service) GetHLSURL(cameraID string) string {
	return s.webrtcPublisher.GetHLSURL(cameraID)
}

func (s *Service) GetRTSPURL(cameraID string) string {
	return s.webrtcPublisher.GetRTSPURL(cameraID)
}
