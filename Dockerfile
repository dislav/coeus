# syntax=docker/dockerfile:1.7

# ── Build stage ──────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder

# CGO + libvips build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libc6-dev pkg-config libvips-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Cache module downloads separately from source
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Build
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -ldflags='-s -w' -trimpath -o /coeus ./cmd/coeus

# ── Runtime stage ────────────────────────────────────────────
FROM debian:bookworm-slim

# Runtime: libvips shared libs + CA certs (HTTPS to AI APIs) + wget (healthcheck).
# libvips42 transitively installs libheif1, which on bookworm hard-Depends on
# libde265-0 — so HEIC (H.265) decode works out of the box. (The separate
# libheif-plugin-libde265 package only exists on trixie/sid's libheif 1.17+.)
RUN apt-get update && apt-get install -y --no-install-recommends \
    libvips42 ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*

# Non-root user
RUN useradd -r -u 1000 coeus && mkdir /data && chown coeus /data
USER coeus

COPY --from=builder /coeus /coeus

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/coeus"]
