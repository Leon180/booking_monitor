# Dockerfile for Booking Monitor
#
# All base images are pinned to a concrete minor tag (H11) and the
# runner stage drops root (H12). Update the pins deliberately; do NOT
# switch back to `:alpine` or `:latest`, which reintroduce supply-chain
# drift and reproducibility gaps.

# Stage 1: Builder
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache make git

# BUILD_TARGET selects which `cmd/<binary>/` to compile. Default
# `./cmd/booking-cli` preserves backward compatibility for the main
# `docker-compose.yml` `build: .` shorthand. The D12 comparison
# harness (`docker-compose.comparison.yml`) overrides this per stage:
#   stage1 → ./cmd/booking-cli-stage1
#   stage2 → ./cmd/booking-cli-stage2
#   stage3 → ./cmd/booking-cli-stage3
#   stage4 → ./cmd/booking-cli           (the current production binary IS Stage 4)
# Binary name remains `booking-cli` regardless so the runner stage
# stays uniform — only the source `cmd/` differs.
ARG BUILD_TARGET=./cmd/booking-cli

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Tidy modules
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/booking-cli "${BUILD_TARGET}"

# Stage 2: Runner
FROM alpine:3.20

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Create a non-root user for the runtime stage (H12). UID/GID 10001 is
# high enough to avoid collision with any Alpine system user.
RUN addgroup -g 10001 -S booking \
 && adduser  -u 10001 -S booking -G booking

# Copy binary from builder
COPY --from=builder /app/bin/booking-cli /app/booking-cli

# Copy config (read-only at runtime)
COPY config/ config/

# Drop root before the process starts. The binary and config must be
# readable by the booking user; chown explicitly so the copy steps
# above (which run as root) still produce files this UID can read.
RUN chown -R booking:booking /app

USER booking:booking

# Expose port
EXPOSE 8080

# Command to run (can be overridden in docker-compose)
CMD ["/app/booking-cli", "server"]
