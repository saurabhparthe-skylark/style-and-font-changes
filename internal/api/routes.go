package api

func (s *Server) setupRoutes() {
	s.router.GET("/", s.healthHandler.WorkerInfo)
	s.router.GET("/health", s.healthHandler.HealthCheck)

	cameras := s.router.Group("/cameras")
	{
		cameras.GET("", s.cameraHandler.ListCameras)
		cameras.POST("/:id/start", s.cameraHandler.StartCamera)
		cameras.POST("/:id/stop", s.cameraHandler.StopCamera)
		cameras.GET("/:id/status", s.cameraHandler.GetCameraStatus)
	}

	webrtc := s.router.Group("/webrtc")
	{
		webrtc.GET("/:id/urls", s.webrtcHandler.GetStreamURLs)
		webrtc.GET("/stats", s.webrtcHandler.GetStats)
	}

	system := s.router.Group("/system")
	{
		system.GET("/stats", s.systemHandler.GetStats)
	}
}
