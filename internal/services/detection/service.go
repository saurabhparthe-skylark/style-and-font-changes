package detection

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "kepler-worker-go/proto"
)

type Service struct {
	client    pb.DetectionServiceClient
	conn      *grpc.ClientConn
	grpcURL   string
	isHealthy bool
}

func NewService(grpcURL string) (*Service, error) {
	log.Info().Str("url", grpcURL).Msg("Initializing AI detection service")

	service := &Service{
		grpcURL:   grpcURL,
		isHealthy: false,
	}

	// Try to connect, but don't fail if it's not available
	err := service.connect()
	if err != nil {
		log.Warn().Err(err).Msg("AI detection service not available, will retry later")
	}

	return service, nil
}

func (s *Service) connect() error {
	if s.conn != nil {
		s.conn.Close()
	}

	conn, err := grpc.NewClient(s.grpcURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to connect to detection service: %w", err)
	}

	client := pb.NewDetectionServiceClient(conn)

	// Test connection with health check
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = client.HealthCheck(ctx, &pb.Empty{})
	if err != nil {
		conn.Close()
		return fmt.Errorf("detection service health check failed: %w", err)
	}

	s.client = client
	s.conn = conn
	s.isHealthy = true

	log.Info().Msg("Successfully connected to AI detection service")
	return nil
}

func (s *Service) ensureConnection() error {
	if s.isHealthy && s.conn != nil {
		return nil
	}

	return s.connect()
}

func (s *Service) ProcessFrame(ctx context.Context, req *pb.FrameRequest) (*pb.DetectionResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	resp, err := s.client.InferDetection(ctx, req)
	log.Debug().Msgf("Detection response: %v", resp)
	if err != nil {
		s.isHealthy = false
		return nil, err
	}

	return resp, nil
}

func (s *Service) SaveFalseDetection(ctx context.Context, req *pb.ROIRequest) (*pb.StatusResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.SaveFalseDetection(ctx, req)
}

func (s *Service) RemoveFalseDetection(ctx context.Context, req *pb.RemoveROIRequest) (*pb.StatusResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.RemoveFalseDetection(ctx, req)
}

func (s *Service) GetActivePipelines(ctx context.Context) (*pb.PipelinesResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.GetActivePipelines(ctx, &pb.Empty{})
}

func (s *Service) RemovePipelines(ctx context.Context, req *pb.CameraIdRequest) (*pb.StatusResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.RemovePipelines(ctx, req)
}

func (s *Service) SetROI(ctx context.Context, req *pb.SolutionROIRequest) (*pb.StatusResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.SetROI(ctx, req)
}

func (s *Service) AddFace(ctx context.Context, req *pb.AddFaceRequest) (*pb.StatusResponse, error) {
	if err := s.ensureConnection(); err != nil {
		return nil, fmt.Errorf("detection service unavailable: %w", err)
	}

	return s.client.AddFace(ctx, req)
}

func (s *Service) HealthCheck(ctx context.Context) error {
	if err := s.ensureConnection(); err != nil {
		return err
	}

	_, err := s.client.HealthCheck(ctx, &pb.Empty{})
	if err != nil {
		s.isHealthy = false
	}
	return err
}

func (s *Service) IsHealthy() bool {
	return s.isHealthy
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s.conn != nil {
		log.Info().Msg("Shutting down detection service connection")
		return s.conn.Close()
	}
	return nil
}
