.PHONY: build clean proto swagger run dev test fmt lint help docker-build docker-build-static docker-push docker-run

# Application configuration
APP_NAME=kepler-worker
BINARY_NAME=$(APP_NAME)
VERSION?=1.0.0
BUILD_TIME?=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT?=$(shell git rev-parse --short HEAD)
DOCKER_REGISTRY?=skylarklabs

# Directories
BIN_DIR=bin
PROTO_DIR=proto
CMD_DIR=./cmd/worker

# Build flags
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

## Build the application binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_DIR)

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

## Build Docker image using docker-compose
docker-build:
	cd docker && docker compose build --build-arg VERSION=$(VERSION)
	docker tag $(DOCKER_REGISTRY)/kepler-worker-go:$(VERSION) $(DOCKER_REGISTRY)/kepler-worker-go:latest

docker-run:
	cd docker && docker compose up -d

## Push Docker image to Docker Hub
docker-push:
	docker push $(DOCKER_REGISTRY)/kepler-worker-go:$(VERSION)
	docker push $(DOCKER_REGISTRY)/kepler-worker-go:latest

## Build static Docker image and extract binary
docker-build-static:
	@mkdir -p $(BIN_DIR)
	docker build -f docker/Dockerfile.static -t kepler-worker-static:$(VERSION) --build-arg VERSION=$(VERSION) .
	docker run --rm -v $(PWD)/$(BIN_DIR):/output kepler-worker-static:$(VERSION) cp /kepler-worker-static /output/kepler-worker-static

