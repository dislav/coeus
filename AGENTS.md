# AGENTS.md

Guidance for OpenCode agents working in this repo. Coeus is a Go service that
ingests exam images through a REST API and runs an async AI pipeline
(extract → dedup → verify) to parse multiple-choice questions. It is a
**single binary**: the HTTP server and the worker pool run in one process.

## Commands

There is no `Makefile` and no CI — use `go` directly.

- Build: `go build ./...`
- Vet: `go vet ./...`  (no linter is configured)
- Unit tests, no external deps: `go test -short ./...`
- Integration tests, **Docker must be running**: `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s`
- Single package: `go test ./internal/ai/extractor/ -v`
- Run the server: `go run ./cmd/coeus`

Requires Go 1.26.3+ (see `go.mod`).

## CGO + libvips are required (not optional)

Image processing uses `govips`, so every build needs **`CGO_ENABLED=1`** and the
libvips development headers. A plain `go build` fails without them.

- macOS: `brew install vips pkg-config`
- Debian/Ubuntu: `apt-get install gcc libc6-dev pkg-config libvips-dev`

The `Dockerfile` sets all of this up — prefer `docker build -t coeus .` when the
local toolchain isn't configured. The runtime binary also needs the libvips
shared library present (`libvips42` on Linux).

## Config is embedded — there is no config file to edit

`internal/config/config.yaml` is compiled in via `//go:embed`. Do not look for
or create a runtime config file. Non-secret defaults live there; everything
sensitive or environment-specific comes from `COEUS_`-prefixed env vars:

- Required: `COEUS_POSTGRES_DSN`, `COEUS_JWT_SECRET`, `COEUS_AI_VISION_API_KEY`, `COEUS_AI_REVIEWER_API_KEY`
- Optional: `COEUS_AI_EMBEDDER_API_KEY` (if unset, semantic dedup is skipped — exact-hash dedup still runs) and `COEUS_*_BASE_URL` overrides.

`config.Validate()` runs at startup and exits if a required value is missing.
See `applyEnvOverrides` in `internal/config/config.go` for the full env list.

## Migrations run automatically on boot

`internal/storage/postgres/migrations/NNNN_*.sql` is embedded and applied on
every startup by `postgres.RunMigrations` (called from `app.Build`). There is
**no separate `migrate` step**. To change schema, add a new numbered file.
PostgreSQL needs the `vector` extension (pgvector), created by `0001_extensions.sql`.

## Architecture map

- `cmd/coeus/main.go` — entrypoint: load config → `app.Build` → start worker pool → serve HTTP → graceful shutdown on SIGINT/SIGTERM.
- `internal/app/wire.go` — **manual dependency wiring**. Read this first; migrations and libvips startup both happen here.
- `internal/config` — embedded YAML + env overrides.
- `internal/httpapi` — Gin HTTP layer (`/healthz`, `/readyz`, `/api/v1/...`), JWT middleware; handlers in `handlers/`.
- `internal/pipeline` — async job processor: `pipeline.go` (per-job extract→dedup→verify), `worker.go` (worker pool + reaper), `ports.go` (AI/storage interfaces).
- `internal/ai` — AI clients by role: `extractor` (Kimi/Moonshot vision), `verifier` (DeepSeek), `embedder` (OpenAI-compatible, optional), `enhancer` (local govips), `oai` (shared OpenAI client).
- `internal/storage` — repo interfaces in `ports.go`; PostgreSQL impls + job queue in `storage/postgres`.
- `internal/auth` (JWT, password hashing), `internal/domain` (core entities/errors).

## Async job model (important for debugging)

Image upload enqueues a job; processing is **not** synchronous. Workers claim
jobs with `FOR UPDATE SKIP LOCKED` and idle on the Postgres `LISTEN/NOTIFY
jobs_new` channel. A reaper reclaims jobs stale past `pipeline.stale_threshold`
(default 10m) and fails them after `max_queue_attempts`. If "an image isn't
getting processed," look at the worker pool / its DB connection / the reaper,
not the upload handler.

## Testing

- Integration tests use **Testcontainers** and start `pgvector/pgvector:pg16`,
  so they need Docker running and network access to pull the image. They
  self-skip under `-short`.
- There are **no build tags** — the `-short` flag is the only gate.
- For DB-backed tests, use `setupTestDB(t)` in `internal/storage/postgres/testhelpers_test.go`.

## Repo gotchas

- **`*.md` and `docs/` are gitignored.** New markdown (this file included) won't
  appear in `git status`; use `git add -f` to track it. This is intentional for
  planning/scratch docs.
- Real secrets live in the gitignored `.env` — never paste its contents into output.
