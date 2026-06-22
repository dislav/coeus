# Coeus

Exam-image processing API. Upload photos of exam questions, and Coeus extracts,
verifies, and indexes them for expert review using vision LLM, text LLM, and
embedding models.

## Pipeline

```
upload → enhance → extract → dedup → verify → embed → save
         (govips)  (Kimi     (exact  (DeepSeek (OpenAI)
                    vision)   + pgv)  text)
```

1. **Enhance** — deterministic Go image processing (auto-rotate, contrast,
   gamma, sharpen) via libvips
2. **Extract** — Kimi/Moonshot vision LLM reads questions, choices, answers,
   confidence, and subject tags from the image
3. **Dedup** — exact hash match + pgvector cosine similarity against existing
   questions in the session
4. **Verify** — DeepSeek text LLM validates answers, adjusts confidence, flags
   disagreements
5. **Embed** — OpenAI `text-embedding-3-small` generates 1536-dim vectors for
   semantic dedup across sessions
6. **Save** — questions persisted with status `moderation`, subject tags, and
   `ai-generated` tag

## Prerequisites

| Dependency | Version | Install |
|---|---|---|
| Go | 1.26+ | [go.dev/dl](https://go.dev/dl/) |
| PostgreSQL | 15+ with `pgvector` | `brew install postgresql@16` |
| libvips | 8.16+ | macOS: `brew install vips pkg-config` / Linux: `sudo apt install libvips-dev` |
| Docker | any (for Testcontainers tests) | [docker.com](https://docker.com) |

Verify libvips:
```bash
pkg-config --modversion vips
```

## Quick Start

### 1. Set up PostgreSQL

```bash
# Create database with pgvector extension
createdb coeus
psql coeus -c "CREATE EXTENSION IF NOT EXISTS vector;"
```

### 2. Set environment variables

```bash
export COEUS_POSTGRES_DSN="postgres://user:pass@localhost:5432/coeus?sslmode=disable"
export COEUS_JWT_SECRET="change-me-in-production"

# AI provider keys (required — app fails fast without them)
export COEUS_AI_VISION_API_KEY="sk-..."               # Moonshot/Kimi
export COEUS_AI_VISION_BASE_URL="https://api.moonshot.cn/v1"
export COEUS_AI_REVIEWER_API_KEY="sk-..."             # DeepSeek
export COEUS_AI_REVIEWER_BASE_URL="https://api.deepseek.com/v1"
export COEUS_AI_EMBEDDER_API_KEY="sk-..."             # OpenAI
```

### 3. Run

```bash
go run ./cmd/coeus
```

Migrations run automatically on startup. The server listens on `:8080` by
default.

## Configuration

Configuration is embedded in the binary (`internal/config/config.yaml`) and
overridden by environment variables. Only secrets need env vars — everything
else has sensible defaults.

### Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `COEUS_POSTGRES_DSN` | yes | — | PostgreSQL connection string |
| `COEUS_JWT_SECRET` | yes | — | JWT signing secret |
| `COEUS_AI_VISION_API_KEY` | yes | — | Moonshot/Kimi API key |
| `COEUS_AI_VISION_BASE_URL` | no | `https://api.moonshot.cn/v1` | Vision model base URL |
| `COEUS_AI_REVIEWER_API_KEY` | yes | — | DeepSeek API key |
| `COEUS_AI_REVIEWER_BASE_URL` | no | `https://api.deepseek.com/v1` | Reviewer model base URL |
| `COEUS_AI_EMBEDDER_API_KEY` | yes | — | OpenAI API key |
| `COEUS_AI_EMBEDDER_BASE_URL` | no | OpenAI default | Embeddings base URL |
| `COEUS_SERVER_ADDR` | no | `:8080` | HTTP listen address |
| `COEUS_WORKERS_COUNT` | no | `4` | Pipeline worker count |

### Defaults (config.yaml)

<details>
<summary>Full embedded config</summary>

```yaml
server:
  addr: ":8080"
  read_timeout: 15s
  write_timeout: 120s
  shutdown_timeout: 30s

postgres:
  max_conns: 20
  min_conns: 4

jwt:
  access_ttl: 1h
  refresh_ttl: 168h

ai:
  vision:
    model: "kimi-k2.7"
    timeout: 90s
  reviewer:
    model: "deepseek-v4-pro"
    timeout: 60s
  embedder:
    model: "text-embedding-3-small"
    dim: 1536

pipeline:
  extract_max_attempts: 3
  semantic_threshold: 0.92
  reaper_interval: 60s
  stale_threshold: 10m
  max_queue_attempts: 3

workers:
  count: 4

upload:
  max_bytes: 10485760  # 10 MB
  allowed_mimes:
    - "image/jpeg"
    - "image/png"
    - "image/webp"
```

</details>

## API

All endpoints except `/healthz`, `/readyz`, and auth are behind JWT auth.

### Health

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Liveness probe |
| GET | `/readyz` | Readiness probe (pings DB) |

### Auth

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/v1/auth/register` | — | Register `{email, password}` |
| POST | `/api/v1/auth/login` | — | Login → `{access_token, refresh_token}` |
| POST | `/api/v1/auth/refresh` | Bearer | Refresh access token |

### Sessions

Sessions are time-boxed windows for uploading images. The upload buffer extends
past the session duration to allow in-flight jobs to finish.

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/sessions` | Create timed session |
| GET | `/api/v1/sessions` | List user's sessions |
| GET | `/api/v1/sessions/:id` | Get session detail |
| POST | `/api/v1/sessions/:id/close` | Manually close session |

### Images

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/sessions/:id/images` | Upload exam image (JPEG/PNG/WebP, max 10 MB) |
| GET | `/api/v1/sessions/:id/images` | List images with pipeline job status |

### Example: End-to-end flow

```bash
# 1. Register
curl -s localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"secret123"}'

# 2. Login
TOKEN=$(curl -s localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"secret123"}' \
  | jq -r .access_token)

# 3. Create session
SID=$(curl -s localhost:8080/api/v1/sessions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"duration_seconds":3600,"buffer_seconds":300}' \
  | jq -r .id)

# 4. Upload exam image
curl -s localhost:8080/api/v1/sessions/$SID/images \
  -H "Authorization: Bearer $TOKEN" \
  -F "image=@exam-photo.jpg"

# 5. Check job status
curl -s localhost:8080/api/v1/sessions/$SID/images \
  -H "Authorization: Bearer $TOKEN" | jq
```

## Project Structure

```
cmd/coeus/              Entry point
internal/
  ai/
    oai/                Shared OpenAI-compatible client factory
    enhancer/           govips image enhancement (no AI)
    extractor/          Kimi vision extraction (multimodal)
    verifier/           DeepSeek text verification
    embedder/           OpenAI embeddings
  app/                  Composition root (wire.go)
  auth/                 JWT management
  config/               Embedded YAML + env overrides
  domain/               Domain types (Image, Session, Question, Job, etc.)
  httpapi/              Gin router, middleware, handlers, DTOs
  pipeline/             10-step orchestration + worker pool (LISTEN/NOTIFY)
  storage/
    ports.go            Repository + JobQueue interfaces
    postgres/           PostgreSQL implementations + migrations
docs/
  superpowers/
    specs/              Design specifications
    plans/              Implementation plans
skills/                 AI prompt definitions for extraction & verification
```

## Development

### Local Database

Spin up PostgreSQL with pgvector for development:

```bash
docker run --rm -d --name coeus-db \
  -p 5432:5432 \
  -e POSTGRES_DB=coeus \
  -e POSTGRES_PASSWORD=dev \
  pgvector/pgvector:pg16

export COEUS_POSTGRES_DSN="postgres://postgres:dev@localhost:5432/coeus?sslmode=disable"
```

Migrations run automatically on app startup — no manual setup needed.

### Environment File

Create a `.env` file (not committed) for local development:

```bash
cat > .env <<'EOF'
COEUS_POSTGRES_DSN=postgres://postgres:dev@localhost:5432/coeus?sslmode=disable
COEUS_JWT_SECRET=dev-secret-not-for-production
COEUS_AI_VISION_API_KEY=sk-...
COEUS_AI_VISION_BASE_URL=https://api.moonshot.cn/v1
COEUS_AI_REVIEWER_API_KEY=sk-...
COEUS_AI_REVIEWER_BASE_URL=https://api.deepseek.com/v1
COEUS_AI_EMBEDDER_API_KEY=sk-...
EOF
```

Load it with [`direnv`](https://direnv.net/) (`direnv allow`) or source manually:

```bash
set -a; source .env; set +a
```

### Running the Server

**One-shot** (recompiles from scratch each time):

```bash
go run ./cmd/coeus
```

**Hot-reload** with [air](https://github.com/air-verse/air) — recompiles only
on file changes, restarts instantly:

```bash
go install github.com/air-verse/air@latest
air
```

First compile takes ~5s (CGO + libvips). Subsequent restarts are near-instant.

### Testing

```bash
# Unit tests (no external dependencies)
go test -short ./...

# Integration tests (requires Docker for Testcontainers)
go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s

# Single package
go test ./internal/ai/extractor/ -v

# Build + vet
go build ./...
go vet ./...
```

### Debugging

Run with Delve:

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
dlv debug ./cmd/coeus
```

Connect from VS Code or IntelliJ at `localhost:2345`.

## Production

### Docker (recommended)

Build and run the multi-stage image (see `Dockerfile`):

```bash
docker build -t coeus .

docker run --rm -d \
  --name coeus \
  -p 8080:8080 \
  -e COEUS_POSTGRES_DSN="postgres://user:pass@db-host:5432/coeus?sslmode=disable" \
  -e COEUS_JWT_SECRET="$(openssl rand -hex 32)" \
  -e COEUS_AI_VISION_API_KEY="sk-..." \
  -e COEUS_AI_REVIEWER_API_KEY="sk-..." \
  -e COEUS_AI_EMBEDDER_API_KEY="sk-..." \
  coeus
```

The image includes a health check against `/healthz` (10s interval, 3 retries).

Verify:

```bash
curl localhost:8080/healthz   # {"status":"ok"}
curl localhost:8080/readyz    # {"status":"ready"}  (pings DB)
```

### Binary (without Docker)

Build a stripped binary:

```bash
CGO_ENABLED=1 go build -ldflags='-s -w' -trimpath -o coeus ./cmd/coeus
```

The binary requires `libvips` shared libraries at runtime. On a Linux server:

```bash
sudo apt install libvips42
./coeus
```

Run under a process manager (systemd, supervisor) with env vars sourced from a
secrets manager or a restricted-permission environment file:

```ini
# /etc/systemd/system/coeus.service
[Unit]
Description=Coeus
After=network.target postgresql.service

[Service]
ExecStart=/usr/local/bin/coeus
EnvironmentFile=/etc/coeus/env
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Production Checklist

- [ ] JWT secret is a random 32+ byte string (not a short password)
- [ ] PostgreSQL has pgvector extension enabled
- [ ] `COEUS_POSTGRES_DSN` points to a persistent database (not a dev container)
- [ ] AI API keys are provisioned with appropriate rate limits
- [ ] `COEUS_WORKERS_COUNT` tuned to match CPU cores and DB connection pool
  (`postgres.max_conns` defaults to 20; each worker may hold a connection)
- [ ] Reverse proxy (nginx, Caddy) in front for TLS termination
- [ ] `upload.max_bytes` (10 MB default) fits your use case

## Graceful Shutdown

On `SIGINT`/`SIGTERM`, the server:

1. Stops accepting new HTTP requests
2. Drains in-flight HTTP requests (30s timeout)
3. Stops the worker pool (waits for in-flight pipeline jobs to finish)
4. Shuts down libvips
5. Closes the PostgreSQL connection pool

## Tech Stack

- **Go 1.26** — Gin web framework, `log/slog` structured logging
- **PostgreSQL** — pgvector for semantic similarity, `LISTEN`/`NOTIFY` for job
  queue, pgcrypto for UUID generation
- **libvips** (via govips) — high-performance image processing (CGO)
- **openai-go** — official OpenAI SDK, used for all three LLM clients
  (Kimi and DeepSeek are OpenAI-compatible)
- **pgx/v5** — PostgreSQL driver with connection pooling
- **Testcontainers** — integration test spin-up of real PostgreSQL instances
