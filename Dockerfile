# Dockerfile for Booking Monitor

# Stage 1: Builder
FROM golang:alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache make git

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Tidy modules
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/booking-cli ./cmd/booking-cli

# Stage 2: Runner
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /app/bin/booking-cli /app/booking-cli

# Expose port
EXPOSE 8080

# Command to run (can be overridden in docker-compose)
CMD ["/app/booking-cli", "server"]
