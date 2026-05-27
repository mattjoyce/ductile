# Build stage
FROM golang:1.26-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata gcc musl-dev sqlite-dev

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Derive version from git state (same script used locally and in CI)
RUN VERSION=$(./scripts/version.sh) && \
    COMMIT=$(git rev-parse --short HEAD) && \
    BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) && \
    CGO_ENABLED=1 GOOS=linux go build -a \
      -ldflags="-w -s -X main.version=${VERSION} -X main.gitCommit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o ductile ./cmd/ductile

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (bash for plugins, jq for JSON parsing, python3 for python plugins)
RUN apk add --no-cache ca-certificates tzdata bash jq curl python3 py3-pip sqlite-libs && \
    curl -LsSf https://astral.sh/uv/install.sh | UV_INSTALL_DIR=/usr/local/bin sh

# Create app user
RUN addgroup -S ductile && adduser -S ductile -G ductile

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/ductile .

# Copy plugins directory
COPY --chown=ductile:ductile plugins/ ./plugins/

# Copy pipelines directory if it exists (wildcard avoids failure when absent)
COPY --chown=ductile:ductile pipeline[s] ./pipelines

# Create data directory for state persistence
RUN mkdir -p /app/data && chown -R ductile:ductile /app/data

# Switch to non-root user
USER ductile

# Expose API port
EXPOSE 8080

# Health check — hit the real liveness endpoint rather than test for
# a pid file at /app/ductile.pid. The container did not write that
# pid file in this mode, so the file-existence check always failed
# and Docker reported the container unhealthy even when the gateway
# was serving requests. /healthz is the gateway's actual liveness
# signal and curl is already installed for runtime use above.
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD curl -fsS http://localhost:8080/healthz || exit 1

# Default command
CMD ["./ductile", "system", "start"]
