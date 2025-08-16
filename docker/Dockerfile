FROM gocv/opencv:4.12.0-ubuntu-24.04 AS builder

# Install latest Go manually
RUN apt-get update && apt-get install -y curl git \
    && curl -LO https://go.dev/dl/go1.24.0.linux-amd64.tar.gz \
    && rm -rf /usr/local/go && tar -C /usr/local -xzf go1.24.0.linux-amd64.tar.gz \
    && rm go1.24.0.linux-amd64.tar.gz

ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Install swag CLI and generate docs
RUN go install github.com/swaggo/swag/cmd/swag@latest
RUN swag init -g internal/api/server.go -o docs/ --parseDependency --parseInternal

# Build binary
RUN CGO_ENABLED=1 go build \
    -ldflags "-X main.Version=${VERSION:-1.0.0} \
                -X main.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ') \
                -X main.GitCommit=$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')" \
    -o /app/kepler-worker \
    ./cmd/worker

FROM gocv/opencv:4.12.0-ubuntu-24.04

WORKDIR /app

# Copy just the binary and docs (no Go toolchain, no cache)
COPY --from=builder /app/kepler-worker /app/kepler-worker
COPY --from=builder /app/docs /app/docs

CMD ["./kepler-worker"]
