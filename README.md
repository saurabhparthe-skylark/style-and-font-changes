# Kepler Worker (Go Implementation)

A high-performance Go implementation of the Kepler video processing worker that handles RTSP streams via FFmpeg, AI processing via gRPC, and WebRTC publishing to MediaMTX using WHIP protocol.

## Architecture

This Go implementation follows a clean, modular architecture:

```
kepler-worker-go/
├── main.go                    # Entry point
├── go.mod                     # Dependencies
├── proto/                     # Protobuf definitions and generated code
│   ├── detection.proto        # AI service protocol
│   ├── detection.pb.go        # Generated protobuf messages
│   └── detection_grpc.pb.go   # Generated gRPC client
├── internal/                  # Internal packages
│   ├── config/                # Configuration management
│   │   └── config.go          # Environment-based config
│   ├── worker/                # Main worker implementation
│   │   ├── worker.go          # Worker core logic
│   │   └── http.go            # REST API endpoints
│   ├── stream/                # Stream processing with FFmpeg
│   │   ├── handler_simple.go  # FFmpeg-based RTSP stream handler
│   │   └── processor_simple.go # Frame processor with AI integration
│   └── webrtc/                # WebRTC publishing
│       └── publisher.go       # MediaMTX WHIP publisher
└── pkg/                       # Public packages
    └── logger/                # Structured logging
        └── logger.go          # JSON logger with context
```

## Key Features

### 1. **FFmpeg-Based Stream Processing**
- Direct RTSP stream handling via FFmpeg subprocess
- Automatic stream property detection (FPS, resolution)
- Robust error handling and reconnection
- Efficient frame buffering for real-time processing

### 2. **WebRTC Publishing via WHIP**
- Full WHIP (WebRTC-HTTP Ingestion Protocol) implementation
- Direct publishing to MediaMTX server
- Automatic session management and cleanup
- Support for HLS, WebRTC, and RTSP output formats

### 3. **Optimized Performance**
- Minimal memory copying and efficient frame management
- Background frame processing with configurable AI frame skipping (1/N)
- Thread-safe concurrent processing
- Adaptive frame rate control

### 4. **Production Ready**
- Comprehensive error handling and recovery
- Structured logging with contextual information
- Health monitoring and statistics
- Graceful shutdown and resource cleanup

## Dependencies

### Required System Dependencies
```bash
# Install FFmpeg (required for RTSP stream processing)
sudo apt update
sudo apt install ffmpeg

# Verify FFmpeg installation
ffmpeg -version
```

### Go Dependencies
- **Gin**: Fast HTTP router and middleware
- **gRPC**: Communication with AI server
- **Protobuf**: Serialization for AI communication
- **Pion WebRTC**: WebRTC implementation for WHIP protocol

## Quick Start

### 1. Build and Run

```bash
# Install dependencies and build
make deps
make build

# Run with default settings
./kepler-worker --port 5000 --worker-id worker-1

# Run with MediaMTX server
MEDIAMTX_WHIP=http://localhost:8889 ./kepler-worker
```

### 2. Start a Camera

```bash
# Start an RTSP camera
curl -X POST http://localhost:5000/cameras/cam1/start \
  -H "Content-Type: application/json" \
  -d '{
    "stream_url": "rtsp://camera.example.com/stream",
    "solutions": ["ppe_detection", "person_detection"]
  }'
```

### 3. Get Stream URLs

```bash
# Get all available stream formats
curl http://localhost:5000/webrtc/cam1/urls

# Response:
{
  "hls": "http://172.17.0.1:8888/camera_cam1/index.m3u8",
  "webrtc": "http://172.17.0.1:8889/camera_cam1/whep",
  "rtsp": "rtsp://172.17.0.1:8554/camera_cam1"
}
```

### 4. Test with Sample Stream

```bash
# Test with public RTSP stream
make test-stream

# This will:
# 1. Start the worker on port 5002
# 2. Connect to a sample RTSP stream
# 3. Start WebRTC publishing to MediaMTX
# 4. Show access URLs
```

## Configuration

### Environment Variables

```bash
# Worker Configuration
WORKER_PORT=5000                    # API server port
WORKER_ID=worker-1                  # Unique worker identifier
MAX_CAMERAS=10                      # Maximum concurrent cameras

# Stream Configuration
STREAM_BUFFER_SIZE=5                # Frame buffer size
STREAM_MAX_RETRIES=3                # Connection retry attempts
STREAM_RETRY_DELAY=2s               # Delay between retries

# AI Processing
AI_ENABLED=true                     # Enable AI processing
AI_ENDPOINT=localhost:50051         # gRPC AI server
AI_PROCESS_EVERY_N=3                # Process every Nth frame
AI_TIMEOUT=10s                      # AI request timeout

# WebRTC Configuration
WEBRTC_ENABLED=true                 # Enable WebRTC publishing
WEBRTC_TARGET_FPS=15                # Target streaming FPS
WEBRTC_RESOLUTION=1280x720          # Streaming resolution

# MediaMTX Server URLs
MEDIAMTX_WHIP=http://172.17.0.1:8889    # WHIP endpoint
MEDIAMTX_HLS=http://172.17.0.1:8888     # HLS endpoint  
MEDIAMTX_RTSP=rtsp://172.17.0.1:8554    # RTSP endpoint
```

## API Endpoints

The worker exposes a clean, minimal REST API:

```
# Health and Info
GET  /health                    # Health check
GET  /                          # Worker information and capabilities

# Camera Management  
GET  /cameras                   # List all cameras
POST /cameras/{id}/start        # Start camera processing
POST /cameras/{id}/stop         # Stop camera processing  
GET  /cameras/{id}/status       # Get detailed camera status

# WebRTC Streaming
GET  /webrtc/{id}/urls          # Get stream URLs (HLS, WebRTC, RTSP)
GET  /webrtc/stats              # Get WebRTC statistics (all cameras)
GET  /webrtc/stats?id={id}      # Get WebRTC statistics (specific camera)

# System Monitoring
GET  /system/stats              # System performance metrics
```

### API Request/Response Examples

#### Start Camera
```bash
POST /cameras/cam1/start
Content-Type: application/json

{
  "stream_url": "rtsp://camera.example.com/stream",
  "solutions": ["person_detection", "ppe_detection"]
}

# Response:
{
  "camera_id": "cam1",
  "status": "started", 
  "stream_url": "rtsp://camera.example.com/stream",
  "message": "Camera started successfully"
}
```

#### Get Stream URLs
```bash
GET /webrtc/cam1/urls

# Response:
{
  "camera_id": "cam1",
  "hls": "http://172.17.0.1:8888/camera_cam1/index.m3u8",
  "webrtc": "http://172.17.0.1:8889/camera_cam1/whep", 
  "rtsp": "rtsp://172.17.0.1:8554/camera_cam1"
}
```

#### Camera Status
```bash
GET /cameras/cam1/status

# Response:
{
  "camera_id": "cam1",
  "active": true,
  "stream_info": {
    "fps": 25.0,
    "width": 1280,
    "height": 720,
    "connected": true
  },
  "processing_info": {
    "frames_processed": 1500,
    "frames_sent_ai": 500,
    "avg_process_time": "45ms"
  },
  "webrtc_info": {
    "session_id": "sess_12345",
    "active": true,
    "uptime": "5m30s"
  }
}
```

## Example Usage

### Python Client Example

```python
import requests
import time

# Start worker
worker_url = "http://localhost:5000"

# Add camera
camera_config = {
    "stream_url": "rtsp://camera.local/stream",
    "solutions": ["person_detection", "ppe_detection"]
}

response = requests.post(f"{worker_url}/cameras/office_cam/start", 
                        json=camera_config)
print(f"Camera started: {response.status_code}")

# Get stream URLs
urls = requests.get(f"{worker_url}/webrtc/office_cam/urls").json()
print(f"HLS Stream: {urls['hls']}")
print(f"WebRTC Stream: {urls['webrtc']}")

# Monitor status
status = requests.get(f"{worker_url}/cameras/office_cam/status").json()
print(f"Camera status: {status}")
```

### JavaScript/Browser WebRTC Client

```javascript
async function watchStream(cameraId) {
    // Get WebRTC endpoint
    const response = await fetch(`/webrtc/${cameraId}/urls`);
    const urls = await response.json();
    
    // Connect to WebRTC stream
    const pc = new RTCPeerConnection({
        iceServers: [{urls: 'stun:stun.l.google.com:19302'}]
    });
    
    pc.ontrack = (event) => {
        const video = document.getElementById('video');
        video.srcObject = event.streams[0];
    };
    
    // Use WHEP protocol to connect
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    
    const whepResponse = await fetch(urls.webrtc, {
        method: 'POST',
        headers: {'Content-Type': 'application/sdp'},
        body: pc.localDescription.sdp
    });
    
    const answer = await whepResponse.text();
    await pc.setRemoteDescription({type: 'answer', sdp: answer});
}
```

## Performance Characteristics

- **Memory Usage**: ~50-100MB per camera stream
- **CPU Usage**: ~15-25% per 1080p@25fps stream (with AI processing)
- **Network**: Supports 10+ concurrent 1080p streams on modern hardware
- **Latency**: <200ms end-to-end (RTSP → WebRTC)
- **Reconnection**: Automatic reconnection within 5-10 seconds

## Troubleshooting

### Common Issues

1. **FFmpeg not found**
   ```bash
   # Install FFmpeg
   sudo apt install ffmpeg
   ```

2. **RTSP connection failures**
   ```bash
   # Check stream URL
   ffmpeg -i rtsp://your-camera/stream -t 5 -f null -
   ```

3. **WebRTC publishing issues**
   ```bash
   # Verify MediaMTX is running
   curl http://localhost:8889/
   ```

4. **Permission denied errors**
   ```bash
   # Ensure proper permissions
   sudo usermod -a -G video $USER
   ```

## Development

### Build Commands

```bash
make build          # Build binary
make test           # Run tests  
make lint           # Run linter
make proto          # Generate protobuf
make swagger        # Generate API docs
make test-stream    # Test with sample stream
```

### Code Structure

The codebase follows Go best practices:
- Clear separation of concerns
- Interface-based design for testability
- Comprehensive error handling
- Efficient resource management
- Thread-safe concurrent operations

## Status

✅ **Production Ready**: 
- FFmpeg-based RTSP stream processing
- WHIP protocol WebRTC publishing
- Comprehensive error handling and recovery
- Performance optimized for high-throughput scenarios
- Full API and monitoring capabilities 