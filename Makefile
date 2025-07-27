# Kepler Worker Go - Simple Makefile

.PHONY: all build clean run test proto swagger deps

# Default target
all: clean deps proto swagger build

# Build the application
build:
	@echo "🔨 Building kepler-worker..."
	go build -o kepler-worker main.go

# Clean build artifacts
clean:
	@echo "🧹 Cleaning..."
	go clean
	rm -f kepler-worker
	rm -rf docs/

# Install dependencies
deps:
	@echo "📦 Installing dependencies..."
	go mod download
	go mod tidy

# Generate protobuf files
proto:
	@echo "🚀 Generating protobuf files..."
	@mkdir -p pkg/detection
	protoc --go_out=. --go-grpc_out=. \
		--go_opt=paths=source_relative \
		--go-grpc_opt=paths=source_relative \
		proto/detection.proto

# Generate swagger documentation
swagger:
	@echo "📚 Generating swagger docs..."
	@mkdir -p docs
	@command -v swag >/dev/null 2>&1 || { echo "Installing swag..."; go install github.com/swaggo/swag/cmd/swag@latest; }
	swag init -g internal/api/handlers/* -o docs/ --parseDependency --parseInternal

# Run the application
run: build
	@echo "🚀 Running kepler-worker..."
	./kepler-worker

# Run with custom port
dev: build
	@echo "🚀 Running kepler-worker on port 5001..."
	./kepler-worker --worker-id=dev-worker

# Run tests
test:
	@echo "🧪 Running tests..."
	go test -v ./...

# Format code
fmt:
	@echo "✨ Formatting code..."
	go fmt ./...

# Run linter
lint:
	@echo "🔍 Running linter..."
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Installing golangci-lint..."; go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; }
	golangci-lint run

# Test with sample RTSP stream
test-stream: build
	@echo "🎥 Testing with sample camera..."
	@echo "Starting camera with test RTSP URL..."
	@echo "Make sure MediaMTX is running on localhost:8889"
	@./kepler-worker --port 5002 &
	@sleep 2
	@curl -X POST http://localhost:5002/cameras/test/start \
		-H "Content-Type: application/json" \
		-d '{"stream_url": "rtsp://wowzaec2demo.streamlock.net/vod/mp4:BigBuckBunny_115k.mov", "solutions": ["person_detection"]}'
	@echo "\nCamera started! Check status at: http://localhost:5002/cameras/test/status"
	@echo "WebRTC URLs at: http://localhost:5002/webrtc/test/urls"

# Stop test worker
stop-test:
	@echo "🛑 Stopping test worker..."
	@pkill -f "kepler-worker.*5002" || true