package api

func (s *Server) setupRoutes() {
	s.router.GET("/", s.healthHandler.WorkerInfo)
	s.router.GET("/health", s.healthHandler.HealthCheck)

	cameras := s.router.Group("/cameras")
	{
		cameras.GET("", s.cameraHandler.ListCameras)
		cameras.POST("", s.cameraHandler.AddCamera)
		cameras.DELETE("/:id", s.cameraHandler.RemoveCamera)
		cameras.GET("/:id/status", s.cameraHandler.GetCameraStatus)
		cameras.GET("/:id/frame", s.cameraHandler.GetLatestFrame)
	}

	webrtc := s.router.Group("/webrtc")
	{
		webrtc.POST("/control", s.webrtcHandler.Control)
		webrtc.GET("/status", s.webrtcHandler.Status)
		webrtc.GET("/:id/urls", s.webrtcHandler.GetStreamURLs)
	}

	system := s.router.Group("/system")
	{
		system.GET("/stats", s.systemHandler.GetStats)
		system.GET("/debug", s.systemHandler.GetDebugInfo)
	}
}
