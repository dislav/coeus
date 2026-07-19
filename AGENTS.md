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

## context-mode — MANDATORY routing rules

context-mode MCP tools available. Rules protect context window from flooding. One unrouted command dumps 56 KB into context.

## Think in Code — MANDATORY

Analyze/count/filter/compare/search/parse/transform data: **write code** via `context-mode_ctx_execute(language, code)`, `console.log()` only the answer. Do NOT read raw data into context. PROGRAM the analysis, not COMPUTE it. Pure JavaScript — Node.js built-ins only (`fs`, `path`, `child_process`). `try/catch`, handle `null`/`undefined`. One script replaces ten tool calls.

## BLOCKED — do NOT attempt

### curl / wget — BLOCKED
Shell `curl`/`wget` intercepted and blocked. Do NOT retry.
Use: `context-mode_ctx_fetch_and_index(url, source)` or `context-mode_ctx_execute(language: "javascript", code: "const r = await fetch(...)")`

### Inline HTTP — BLOCKED
`fetch('http`, `requests.get(`, `requests.post(`, `http.get(`, `http.request(` — intercepted. Do NOT retry.
Use: `context-mode_ctx_execute(language, code)` — only stdout enters context

### Direct web fetching — BLOCKED
Use: `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)`

## REDIRECTED — use sandbox

### Shell (>20 lines output)
Shell ONLY for: `git`, `mkdir`, `rm`, `mv`, `cd`, `ls`, `npm install`, `pip install`.
Otherwise: `context-mode_ctx_batch_execute(commands, queries)` or `context-mode_ctx_execute(language: "javascript", code: "...")`. Use `language: "shell"` only when code matches the host shell.

### File reading (for analysis)
Reading to **edit** → reading correct. Reading to **analyze/explore/summarize** → `context-mode_ctx_execute_file(path, language, code)`.

### grep / search (large results)
Use `context-mode_ctx_execute(language: "javascript", code: "...")` in sandbox for portable filtering/counting.

## Tool selection

0. **MEMORY**: `context-mode_ctx_search(sort: "timeline")` — after resume, check prior context before asking user.
1. **GATHER**: `context-mode_ctx_batch_execute(commands, queries)` — runs all commands, auto-indexes, returns search. ONE call replaces 30+. Each command: `{label: "header", command: "..."}`.
2. **FOLLOW-UP**: `context-mode_ctx_search(queries: ["q1", "q2", ...])` — all questions as array, ONE call (default relevance mode).
3. **PROCESSING**: `context-mode_ctx_execute(language, code)` | `context-mode_ctx_execute_file(path, language, code)` — sandbox, only stdout enters context.
4. **WEB**: `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)` — raw HTML never enters context.
5. **INDEX**: `context-mode_ctx_index(content, source)` — store in FTS5 for later search.

## Parallel I/O batches

For multi-URL fetches or multi-API calls, **always** include `concurrency: N` (1-8):

- `context-mode_ctx_batch_execute(commands: [3+ network commands], concurrency: 5)` — gh, curl, dig, docker inspect, multi-region cloud queries
- `context-mode_ctx_fetch_and_index(requests: [{url, source}, ...], concurrency: 5)` — multi-URL batch fetch

**Use concurrency 4-8** for I/O-bound work (network calls, API queries). **Keep concurrency 1** for CPU-bound (npm test, build, lint) or commands sharing state (ports, lock files, same-repo writes).

GitHub API rate-limit: cap at 4 for `gh` calls.

## Output

Write artifacts to FILES — never inline. Return: file path + 1-line description.
Descriptive source labels for `search(source: "label")`.

## Session Continuity

Skills, roles, and decisions persist for the entire session. Do not abandon them as the conversation grows.

## Memory

Session history is persistent and searchable. On resume, search BEFORE asking the user:

| Need | Command |
|------|---------|
| What did we decide? | `context-mode_ctx_search(queries: ["decision"], source: "decision", sort: "timeline")` |
| What constraints exist? | `context-mode_ctx_search(queries: ["constraint"], source: "constraint")` |

DO NOT ask "what were we working on?" — SEARCH FIRST.
If search returns 0 results, proceed as a fresh session.

## ctx commands

| Command | Action |
|---------|--------|
| `ctx stats` | Call `stats` MCP tool, display full output verbatim |
| `ctx doctor` | Call `doctor` MCP tool, run returned shell command, display as checklist |
| `ctx upgrade` | Call `upgrade` MCP tool, run returned shell command, display as checklist |
| `ctx purge` | Call `purge` MCP tool with confirm: true. Warns before wiping knowledge base. |

After /clear or /compact: knowledge base and session stats preserved. Use `ctx purge` to start fresh.