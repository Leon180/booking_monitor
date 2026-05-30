# syntax=docker/dockerfile:1.7
#
# Dockerfile for Booking Monitor — production-hardened.
#
# Hardening applied (PR 2):
#   * Base images pinned to immutable SHA digests (NOT moving tags) —
#     prevents upstream tag re-tagging or supply-chain compromise from
#     silently flowing into our builds. Update via Renovate / Dependabot.
#     Manual update:  crane digest <image>:<tag>
#   * Runner stage = `gcr.io/distroless/static-debian12:nonroot`
#     - No shell, no apk/apt, no busybox. RCE → attacker has no staging
#       ground (can't wget/curl/install anything).
#     - CA certs + /etc/passwd (with `nonroot` user uid 65532) included.
#     - No `RUN` directives possible in runner stage — distroless has
#       no shell to execute them. All ownership done via COPY --chown.
#   * OCI image labels (org.opencontainers.image.*) — provenance baked
#     into the manifest. Visible via `docker inspect`. Not a security
#     boundary (labels are self-claim), but essential for human debug
#     + companion to PR 3's cosign signature.
#   * Build args (COMMIT_SHA / BUILD_DATE / VERSION) injected via
#     -ldflags into the binary AND surfaced as OCI labels. Reproducible
#     build: same source + same base digest = bit-identical image.
#   * BuildKit cache mounts on `go mod download` + go build cache.
#     CI build time ~30s warm vs ~2-3min cold.
#   * NO `go mod tidy` in the builder stage — tidy is a dev-time op
#     that modifies go.mod. Reproducible build needs go.mod committed
#     as-is. Run tidy locally before commit; never in CI.

# Pin both base images to immutable SHA digests. Update deliberately.
ARG GO_BASE=golang:1.25-alpine@sha256:8d22e29d960bc50cd025d93d5b7c7d220b1ee9aa7a239b3c8f55a57e987e8d45
ARG RUNNER_BASE=gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639

# ============================================================================
# Stage 1: Builder
# ============================================================================
# hadolint ignore=DL3006
# Rationale: GO_BASE includes a sha256 digest pin (strongest possible "tag");
# hadolint doesn't resolve ARG values so it can't see the pin and warns
# DL3006 (untagged image). False positive — suppress inline.
FROM ${GO_BASE} AS builder

WORKDIR /app

# Only `git` is needed for go module resolution (some private modules use
# git+ssh). `make` was previously installed; Dockerfile doesn't use it.
RUN apk add --no-cache git

# BUILD_TARGET selects which `cmd/<binary>/` to compile. Default is the
# production server. The D12 comparison harness
# (`docker-compose.comparison.yml`) overrides per stage:
#   stage1 → ./cmd/booking-cli-stage1
#   stage2 → ./cmd/booking-cli-stage2
#   stage3 → ./cmd/booking-cli-stage3
#   stage4 → ./cmd/booking-cli           (the current production binary IS Stage 4)
# Binary name remains `booking-cli` regardless so the runner stage stays
# uniform — only the source `cmd/` differs.
ARG BUILD_TARGET=./cmd/booking-cli

# Build identity (defaults so `docker build` without args still works for
# local iteration; CI passes real values via --build-arg).
ARG COMMIT_SHA=unknown
ARG BUILD_DATE=unknown
ARG VERSION=dev

# Resolve modules with cache mount (~30s saved on warm CI builds).
# hadolint ignore: DL3022 (mount source not pinned — BuildKit feature)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy source AFTER `go mod download` so module download layer cache
# survives any source-only change.
COPY . .

# Build the static binary. `-trimpath` strips local file paths from
# stack traces (reproducible build requirement). `-w -s` strips DWARF
# debug info + symbol table (smaller binary; acceptable for production
# since pprof works without them).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-w -s \
                -X main.commit=${COMMIT_SHA} \
                -X main.version=${VERSION} \
                -X main.buildDate=${BUILD_DATE}" \
      -o /app/bin/booking-cli "${BUILD_TARGET}"

# ============================================================================
# Stage 2: Runner (distroless)
# ============================================================================
# hadolint ignore=DL3006
# Same rationale as builder stage — RUNNER_BASE includes a sha256 digest.
FROM ${RUNNER_BASE} AS runner

# ARGs must be re-declared in this stage to be available for LABEL substitution.
ARG COMMIT_SHA=unknown
ARG BUILD_DATE=unknown
ARG VERSION=dev

# OCI image labels — provenance metadata. Use the standard
# `org.opencontainers.image.*` namespace so registries (GHCR, Artifact
# Registry) display them and tooling like syft can use them.
#
# CAUTION: Labels are NOT a security boundary. They are self-claims
# written by whoever built the image. PR 3's cosign signature is what
# binds the image to a specific GH Actions build run cryptographically.
LABEL org.opencontainers.image.source="https://github.com/Leon180/booking_monitor" \
      org.opencontainers.image.revision="${COMMIT_SHA}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.title="booking-monitor" \
      org.opencontainers.image.description="High-concurrency ticket booking flash-sale simulator (Go 1.25)" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.url="https://github.com/Leon180/booking_monitor" \
      org.opencontainers.image.vendor="Leon180"

WORKDIR /app

# Distroless has NO shell — every operation must use COPY (no RUN works).
# `--chown=65532:65532` sets ownership at copy time without needing
# `chown` (which doesn't exist in distroless).
#
# UID 65532 is the distroless `nonroot` user (defined in /etc/passwd
# of the nonroot variant). Explicit USER directive below for clarity.
COPY --from=builder --chown=65532:65532 /app/bin/booking-cli /app/booking-cli

# Read-only runtime assets. Config + admin dashboard HTML.
COPY --chown=65532:65532 config/ /app/config/
COPY --chown=65532:65532 web/    /app/web/

USER 65532:65532

# Documents the listening port (8080). Compose / k8s use this for
# discovery hints — does NOT actually open the port (that's the
# orchestrator's job).
EXPOSE 8080

# Distroless requires exec-form ENTRYPOINT (no shell to parse string form).
# Split ENTRYPOINT + CMD so `docker run <image> recon` runs the recon
# subcommand without overriding the binary path.
ENTRYPOINT ["/app/booking-cli"]
CMD ["server"]
