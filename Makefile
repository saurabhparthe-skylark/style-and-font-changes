# Kepler Worker Go - Simple Makefile

.PHONY: all build clean run test proto swagger deps

# Default target
all: clean deps proto swagger build

# Build the application
build:
	@echo "🔨 Building kepler-worker..."
	go build -o bin/kepler-worker main.go

# Clean build artifacts
clean:
	@echo "🧹 Cleaning..."
	go clean
	rm -f bin/kepler-worker
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
	./bin/kepler-worker

# Run with custom port
dev: build
	@echo "🚀 Running kepler-worker on port 5001..."
	./bin/kepler-worker --worker-id=dev-worker

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