SHELL := /usr/bin/fish

.PHONY: build clean proto swagger run dev test fmt lint help build-static build-static-alpine build-static-pkgconfig build-minimal

# Application configuration
APP_NAME=kepler-worker
BINARY_NAME=$(APP_NAME)
VERSION?=1.0.0
BUILD_TIME?=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT?=$(shell git rev-parse --short HEAD)

# Directories
BIN_DIR=bin
PROTO_DIR=proto
CMD_DIR=./cmd/worker

# Build flags
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

# Static build flags for minimal external dependencies
STATIC_LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT) -linkmode external -extldflags '-static'"

# CGO flags for static builds - includes all required dependencies
CGO_STATIC_FLAGS=CGO_ENABLED=1 \
	CGO_CFLAGS="-I/usr/include/opencv4" \
	CGO_LDFLAGS="-L/usr/lib/x86_64-linux-gnu \
		-lopencv_core -lopencv_imgproc -lopencv_imgcodecs -lopencv_videoio -lopencv_highgui \
		-lopencv_features2d -lopencv_calib3d -lopencv_objdetect -lopencv_video -lopencv_dnn \
		-llapack -lblas -lgfortran -lquadmath \
		-lprotobuf -lGL -lGLU -lX11 -lXext \
		-lpthread -ldl -lm -lz -ljpeg -lpng -ltiff -lwebp \
		-static-libgcc -static-libstdc++"

## Build the application binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_DIR)

## Build static binary (requires OpenCV dev libraries)
build-static:
	@echo "Building static $(BINARY_NAME)..."
	@echo "Note: This requires OpenCV development libraries installed on the build system"
	@mkdir -p $(BIN_DIR)
	$(CGO_STATIC_FLAGS) go build -a -installsuffix cgo $(STATIC_LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-static $(CMD_DIR)

## Build with minimal dynamic linking (recommended for deployment)
build-minimal:
	@echo "Building $(BINARY_NAME) with minimal dynamic linking..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -a $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-minimal $(CMD_DIR)

## Build using Alpine-based static linking approach
build-static-alpine:
	@echo "Building static binary using Alpine approach..."
	@echo "This target should be run in an Alpine Linux container with OpenCV static libraries"
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I/usr/include/opencv4" \
	CGO_LDFLAGS="-L/usr/lib -lopencv_core -lopencv_imgproc -lopencv_imgcodecs -lopencv_videoio -lopencv_highgui -static" \
	go build -a -ldflags "-linkmode external -extldflags '-static' -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)" \
	-o $(BIN_DIR)/$(BINARY_NAME)-static-alpine $(CMD_DIR)

## Build static with pkg-config detection (recommended)
build-static-pkgconfig:
	@echo "Building static binary using pkg-config for OpenCV detection..."
	@echo "Installing required dependencies first..."
	sudo apt update && sudo apt install -y \
		libopencv-dev liblapack-dev libblas-dev libprotobuf-dev \
		libgl1-mesa-dev libglu1-mesa-dev libx11-dev libxext-dev \
		libjpeg-dev libpng-dev libtiff-dev libwebp-dev
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 \
	CGO_CFLAGS="$$(pkg-config --cflags opencv4)" \
	CGO_LDFLAGS="$$(pkg-config --libs --static opencv4) -llapack -lblas -lgfortran -lquadmath -lprotobuf -lGL -lGLU -lX11 -lXext -lpthread -ldl -lm -lz" \
	go build -a -ldflags "-linkmode external -extldflags '-static' -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)" \
	-o $(BIN_DIR)/$(BINARY_NAME)-static-pkg $(CMD_DIR)

## Build for multiple architectures
build-multi:
	@echo "Building for multiple architectures..."
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_DIR)

## Generate protobuf files
proto:
	@echo "Generating protobuf files..."
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/detection.proto

## Generate swagger documentation
swagger:
	@echo "Generating swagger documentation..."
	swag init -g internal/api/server.go -o docs/ --parseDependency --parseInternal

## Run the application
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BIN_DIR)/$(BINARY_NAME)

## Run with hot reload for development
dev:
	@echo "Starting development server with hot reload..."
	air

## Format Go code
fmt:
	@echo "Formatting Go code..."
	go fmt ./...

## Run linter
lint:
	@echo "Running linter..."
	golangci-lint run

## Run tests
test:
	@echo "Running tests..."
	go test -v ./...

## Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

## Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BIN_DIR)
	rm -rf tmp
	rm -rf docs
	rm -f coverage.out coverage.html

## Install development tools
tools:
	@echo "Installing development tools..."
	go install github.com/swaggo/swag/cmd/swag@latest
	go install github.com/cosmtrek/air@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

## Show help
help:
	@echo "Available targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
	@echo ""
	@echo "Static Build Notes:"
	@echo "  build-static            - Attempts static build (requires OpenCV dev libs)"
	@echo "  build-static-pkgconfig  - Auto-detects and installs deps (recommended)"
	@echo "  build-minimal           - Minimal dynamic linking (good for containers)"
	@echo "  build-static-alpine     - For use in Alpine container with static OpenCV"