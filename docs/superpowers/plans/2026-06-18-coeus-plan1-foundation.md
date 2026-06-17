# Coeus Plan 1 — Foundation + Auth API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the foundation layer of the Coeus image-question-analysis service: Go module, domain types, config, Postgres storage (pool + migrations + all repositories), JWT auth, and a Gin server skeleton with health/ready checks and auth endpoints (register/login/refresh).

**Architecture:** Standard Go layout (`cmd/` + `internal/`) with narrow interface ports. Domain types are pure (no I/O). Storage uses PGX v5 + pgvector. Repos are behind interfaces so the pipeline (Plan 2) can fake them. Auth is JWT with `user`/`expert` roles. The Gin layer is thin — handlers validate input, call services, map domain errors to HTTP.

**Tech Stack:** Go 1.26, Gin, PGX v5, pgvector, golang-jwt/v5, bcrypt, Testcontainers (Postgres+pgvector) for storage tests, slog for logging, embed.FS for migrations.

**Module path:** `github.com/vlgrigoriev/coeus`

**Plan 1 scope only.** Plans 2 (sessions/images/pipeline) and 3 (AI clients/questions API/expert moderation) follow separately.

---

## File Structure (Plan 1)

| File | Responsibility |
|---|---|
| `go.mod` | Module definition + dependencies |
| `internal/domain/errors.go` | Typed domain errors + HTTP status mapping |
| `internal/domain/question.go` | Question, Tag, Status constants, InferChoiceLabeling |
| `internal/domain/session.go` | Session, Image, Job types |
| `internal/config/config.go` | Typed Config struct + YAML + env loader |
| `internal/config/config.yaml` | Default config values (secrets via env) |
| `internal/storage/ports.go` | Repository interface definitions |
| `internal/storage/postgres/pool.go` | PGX pool factory + migration runner |
| `internal/storage/postgres/migrations.go` | embed.FS for migrations |
| `internal/storage/postgres/migrations/0001_extensions.sql` | pgvector + users + tags |
| `internal/storage/postgres/migrations/0002_core.sql` | sessions, images, questions, session_questions, question_tags, jobs |
| `internal/storage/postgres/user_repo.go` | UserRepo impl |
| `internal/storage/postgres/session_repo.go` | SessionRepo impl |
| `internal/storage/postgres/image_repo.go` | ImageRepo impl (bytea + cleanup) |
| `internal/storage/postgres/question_repo.go` | QuestionRepo impl (exact + semantic dedup) |
| `internal/storage/postgres/job_queue.go` | JobQueue impl (claim + reaper) |
| `internal/auth/password.go` | bcrypt hash/verify |
| `internal/auth/jwt.go` | JWT issue/verify with role claims |
| `internal/httpapi/middleware.go` | Auth, RoleGuard, RequestLog, Recover |
| `internal/httpapi/handlers/common.go` | Shared errorResponse helper |
| `internal/httpapi/handlers/auth.go` | register, login, refresh |
| `internal/httpapi/server.go` | Router wiring, healthz, readyz |
| `internal/app/wire.go` | Composition root |
| `cmd/coeus/main.go` | Entry point: load config, wire deps, run migrations, start Gin |

---

## Task 1: Go Module Init

**Files:**
- Create: `go.mod`
- Modify: `.gitignore`

- [ ] **Step 1: Initialize the module**

Run:
```bash
cd /Users/vlgrigoriev/Projects/develop/coeus
go mod init github.com/vlgrigoriev/coeus
```
Expected: `go: creating new go.mod: module github.com/vlgrigoriev/coeus`

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/gin-gonic/gin@latest
go get github.com/jackc/pgx/v5@latest
go get github.com/jackc/pgx/v5/pgxpool@latest
go get github.com/golang-jwt/jwt/v5@latest
go get golang.org/x/crypto/bcrypt
go get github.com/pgvector/pgvector-go@latest
go get gopkg.in/yaml.v3@latest
go get github.com/google/uuid@latest
go get github.com/testcontainers/testcontainers-go@latest
go get github.com/testcontainers/testcontainers-go/modules/postgres@latest
go mod tidy
```
Expected: `go.mod` and `go.sum` populated.

- [ ] **Step 3: Update .gitignore**

Read the existing `.gitignore` (currently just `.idea`). Replace its content with:

```
.idea
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
go.work
go.work.sum
.env
config.local.yaml
data/
coeus
```

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: no output (empty module compiles cleanly)

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .gitignore
git commit -m "chore: initialize go module with dependencies"
```

---

## Task 2: Domain Errors

**Files:**
- Create: `internal/domain/errors.go`
- Test: `internal/domain/errors_test.go`

- [ ] **Step 1: Write the failing test**

`internal/domain/errors_test.go`:
```go
package domain

import (
	"errors"
	"net/http"
	"testing"
)

func TestErrorHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{ErrNotFound, http.StatusNotFound},
		{ErrSessionExpired, http.StatusGone},
		{ErrDuplicate, http.StatusConflict},
		{ErrValidation, http.StatusBadRequest},
		{ErrUnauthorized, http.StatusUnauthorized},
		{ErrForbidden, http.StatusForbidden},
		{ErrAIUnavailable, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			got := HTTPStatus(tt.err)
			if got != tt.want {
				t.Errorf("HTTPStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrorsIs(t *testing.T) {
	wrapped := errors.Join(ErrNotFound, errors.New("question 123"))
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is should match wrapped sentinel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestError -v`
Expected: FAIL — `undefined: ErrNotFound`, `undefined: HTTPStatus`

- [ ] **Step 3: Write the implementation**

`internal/domain/errors.go`:
```go
package domain

import (
	"errors"
	"net/http"
)

// Sentinel domain errors. Wrap with fmt.Errorf("context: %w", ErrXxx) at I/O boundaries.
var (
	ErrNotFound       = NewError("not_found", "resource not found")
	ErrSessionExpired = NewError("session_expired", "session has expired")
	ErrDuplicate      = NewError("duplicate", "resource already exists")
	ErrValidation     = NewError("validation", "invalid input")
	ErrUnauthorized   = NewError("unauthorized", "authentication required")
	ErrForbidden      = NewError("forbidden", "insufficient role")
	ErrAIUnavailable  = NewError("ai_unavailable", "AI service unavailable")
)

// Error is a typed domain error carrying a stable code for API responses.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// HTTPStatus maps a domain error to its HTTP status code.
// Returns 500 for non-domain errors (unexpected).
func HTTPStatus(err error) int {
	var e *Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case "not_found":
		return http.StatusNotFound
	case "session_expired":
		return http.StatusGone
	case "duplicate":
		return http.StatusConflict
	case "validation":
		return http.StatusBadRequest
	case "unauthorized":
		return http.StatusUnauthorized
	case "forbidden":
		return http.StatusForbidden
	case "ai_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -run TestError -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/domain/errors.go internal/domain/errors_test.go
git commit -m "feat(domain): add typed domain errors with HTTP status mapping"
```

---

## Task 3: Domain Question Types

**Files:**
- Create: `internal/domain/question.go`
- Test: `internal/domain/question_test.go`

- [ ] **Step 1: Write the failing test**

`internal/domain/question_test.go`:
```go
package domain

import "testing"

func TestQuestionStatusConstants(t *testing.T) {
	if QuestionStatusModeration != "moderation" {
		t.Errorf("moderation = %q, want %q", QuestionStatusModeration, "moderation")
	}
	if QuestionStatusVerified != "verified" {
		t.Errorf("verified = %q, want %q", QuestionStatusVerified, "verified")
	}
	if QuestionStatusError != "error" {
		t.Errorf("error = %q, want %q", QuestionStatusError, "error")
	}
}

func TestInferChoiceLabeling(t *testing.T) {
	tests := []struct {
		ids  []string
		want string
	}{
		{[]string{"A", "B", "C"}, "letter"},
		{[]string{"1", "2", "3"}, "number"},
		{[]string{"а", "б", "в"}, "letter"},
		{[]string{}, "letter"}, // default when no ids
	}
	for _, tt := range tests {
		got := InferChoiceLabeling(tt.ids)
		if got != tt.want {
			t.Errorf("InferChoiceLabeling(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestQuestion -v`
Expected: FAIL — undefined constants and function

- [ ] **Step 3: Write the implementation**

`internal/domain/question.go`:
```go
package domain

import "unicode"

// QuestionStatus values — the three lifecycle states.
const (
	QuestionStatusModeration = "moderation"
	QuestionStatusVerified   = "verified"
	QuestionStatusError      = "error"
)

// ChoiceLabeling values — how answer ids are rendered for display.
const (
	ChoiceLabelingLetter = "letter"
	ChoiceLabelingNumber = "number"
)

// Question is the canonical, deduplicated knowledge base entry.
type Question struct {
	ID              string
	Number          int
	Question        string
	QuestionNorm    string
	QuestionHash    string
	MultipleCorrect bool
	Choices         []string
	Answers         []string // value-only, shuffle-safe
	ChoiceLabeling  string
	Confidence      float64
	Explanation     string
	Embedding       []float32
	Status          string
	VerifiedAt      *string // ISO timestamp, nil if not verified
	VerifiedBy      *string // user UUID, nil if not verified
	Tags            []string
}

// InferChoiceLabeling determines whether answer ids use letters or numbers.
// Defaults to "letter" when no ids are present.
func InferChoiceLabeling(ids []string) string {
	for _, id := range ids {
		for _, r := range id {
			if unicode.IsDigit(r) {
				return ChoiceLabelingNumber
			}
			return ChoiceLabelingLetter
		}
	}
	return ChoiceLabelingLetter
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -v`
Expected: PASS — all domain tests green

- [ ] **Step 5: Commit**

```bash
git add internal/domain/question.go internal/domain/question_test.go
git commit -m "feat(domain): add question types and choice labeling inference"
```

---

## Task 4: Domain Session Types

**Files:**
- Create: `internal/domain/session.go`
- Test: `internal/domain/session_test.go`

- [ ] **Step 1: Write the failing test**

`internal/domain/session_test.go`:
```go
package domain

import "testing"

func TestJobStatusConstants(t *testing.T) {
	if JobStatusPending != "pending" {
		t.Errorf("pending = %q", JobStatusPending)
	}
	if JobStatusProcessing != "processing" {
		t.Errorf("processing = %q", JobStatusProcessing)
	}
	if JobStatusDone != "done" {
		t.Errorf("done = %q", JobStatusDone)
	}
	if JobStatusFailed != "failed" {
		t.Errorf("failed = %q", JobStatusFailed)
	}
}

func TestSessionStatusConstants(t *testing.T) {
	if SessionStatusOpen != "open" {
		t.Errorf("open = %q", SessionStatusOpen)
	}
	if SessionStatusClosed != "closed" {
		t.Errorf("closed = %q", SessionStatusClosed)
	}
	if SessionStatusExpired != "expired" {
		t.Errorf("expired = %q", SessionStatusExpired)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestSession -v`
Expected: FAIL — undefined constants

- [ ] **Step 3: Write the implementation**

`internal/domain/session.go`:
```go
package domain

// SessionStatus values.
const (
	SessionStatusOpen    = "open"
	SessionStatusClosed  = "closed"
	SessionStatusExpired = "expired"
)

// JobStatus values.
const (
	JobStatusPending   = "pending"
	JobStatusProcessing = "processing"
	JobStatusDone      = "done"
	JobStatusFailed    = "failed"
)

// Session is a user's timed test window.
type Session struct {
	ID              string
	UserID          string
	DurationSeconds int
	BufferSeconds   int
	StartedAt       string // ISO timestamp
	ExpiresAt       string // ISO timestamp
	Status          string
}

// Image is an uploaded exam photo.
type Image struct {
	ID                  string
	SessionID           string
	Original            []byte // nil after post-review cleanup
	Enhanced            []byte // nil after post-review cleanup
	Mime                string
	Width               int
	Height              int
	VerificationReport  []byte // raw JSON, nil if none
	ExtractionError     []byte  // raw JSON, nil if none
	CreatedAt           string
}

// Job is a queued pipeline task.
type Job struct {
	ID         string
	ImageID    string
	SessionID  string
	Status     string
	Attempts   int
	LastError  string
	QueuedAt   string
	StartedAt  string
	FinishedAt string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/domain/session.go internal/domain/session_test.go
git commit -m "feat(domain): add session, image, job types"
```

---

## Task 5: Config Package

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config.yaml`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:
```go
package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	os.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	os.Setenv("COEUS_JWT_SECRET", "test-secret")
	os.Setenv("COEUS_AI_KIMI_API_KEY", "kimi-key")
	os.Setenv("COEUS_AI_DEEPSEEK_API_KEY", "ds-key")
	os.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
	defer func() {
		os.Unsetenv("COEUS_POSTGRES_DSN")
		os.Unsetenv("COEUS_JWT_SECRET")
		os.Unsetenv("COEUS_AI_KIMI_API_KEY")
		os.Unsetenv("COEUS_AI_DEEPSEEK_API_KEY")
		os.Unsetenv("COEUS_AI_EMBEDDER_API_KEY")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("server.addr = %q, want %q", cfg.Server.Addr, ":8080")
	}
	if cfg.JWT.Secret != "test-secret" {
		t.Errorf("jwt.secret = %q, want %q", cfg.JWT.Secret, "test-secret")
	}
	if cfg.Pipeline.ExtractMaxAttempts != 3 {
		t.Errorf("extract_max_attempts = %d, want 3", cfg.Pipeline.ExtractMaxAttempts)
	}
	if cfg.Workers.Count != 4 {
		t.Errorf("workers.count = %d, want 4", cfg.Workers.Count)
	}
	if cfg.JWT.AccessTTL != time.Hour {
		t.Errorf("jwt.access_ttl = %v, want %v", cfg.JWT.AccessTTL, time.Hour)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Write config.yaml**

`internal/config/config.yaml`:
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
  kimi:
    model: "kimi-k2.7"
    timeout: 90s
  deepseek:
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
  max_bytes: 10485760
  allowed_mimes:
    - "image/jpeg"
    - "image/png"
    - "image/webp"
```

- [ ] **Step 4: Write the implementation**

`internal/config/config.go`:
```go
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Postgres PostgresConfig `yaml:"postgres"`
	JWT      JWTConfig      `yaml:"jwt"`
	AI       AIConfig       `yaml:"ai"`
	Pipeline PipelineConfig `yaml:"pipeline"`
	Workers  WorkersConfig  `yaml:"workers"`
	Upload   UploadConfig   `yaml:"upload"`
}

type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type PostgresConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int32  `yaml:"max_conns"`
	MinConns int32  `yaml:"min_conns"`
}

type JWTConfig struct {
	Secret     string        `yaml:"secret"`
	AccessTTL  time.Duration `yaml:"access_ttl"`
	RefreshTTL time.Duration `yaml:"refresh_ttl"`
}

type AIConfig struct {
	Kimi     KimiConfig     `yaml:"kimi"`
	DeepSeek DeepSeekConfig `yaml:"deepseek"`
	Embedder EmbedderConfig `yaml:"embedder"`
}

type KimiConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}

type DeepSeekConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}

type EmbedderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	Dim     int    `yaml:"dim"`
}

type PipelineConfig struct {
	ExtractMaxAttempts int           `yaml:"extract_max_attempts"`
	SemanticThreshold  float64       `yaml:"semantic_threshold"`
	ReaperInterval     time.Duration `yaml:"reaper_interval"`
	StaleThreshold     time.Duration `yaml:"stale_threshold"`
	MaxQueueAttempts   int           `yaml:"max_queue_attempts"`
}

type WorkersConfig struct {
	Count int `yaml:"count"`
}

type UploadConfig struct {
	MaxBytes     int64    `yaml:"max_bytes"`
	AllowedMimes []string `yaml:"allowed_mimes"`
}

// Load reads config.yaml and applies env overrides.
// Secrets (DSN, JWT secret, API keys) must come from env.
func Load() (*Config, error) {
	data, err := os.ReadFile("internal/config/config.yaml")
	if err != nil {
		return nil, fmt.Errorf("read config.yaml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.yaml: %w", err)
	}

	applyEnvOverrides(&cfg)
	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("COEUS_POSTGRES_DSN"); v != "" {
		cfg.Postgres.DSN = v
	}
	if v := os.Getenv("COEUS_JWT_SECRET"); v != "" {
		cfg.JWT.Secret = v
	}
	if v := os.Getenv("COEUS_AI_KIMI_API_KEY"); v != "" {
		cfg.AI.Kimi.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_KIMI_BASE_URL"); v != "" {
		cfg.AI.Kimi.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_DEEPSEEK_API_KEY"); v != "" {
		cfg.AI.DeepSeek.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_DEEPSEEK_BASE_URL"); v != "" {
		cfg.AI.DeepSeek.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_EMBEDDER_API_KEY"); v != "" {
		cfg.AI.Embedder.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_EMBEDDER_BASE_URL"); v != "" {
		cfg.AI.Embedder.BaseURL = v
	}
	if v := os.Getenv("COEUS_SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("COEUS_WORKERS_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Workers.Count = n
		}
	}
}

func (c *UploadConfig) AllowedMimesMap() map[string]bool {
	m := make(map[string]bool, len(c.AllowedMimes))
	for _, mime := range c.AllowedMimes {
		m[strings.ToLower(mime)] = true
	}
	return m
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add typed config with yaml defaults and env overrides"
```

---

## Task 6: Storage Ports (Repository Interfaces)

**Files:**
- Create: `internal/storage/ports.go`

- [ ] **Step 1: Write the implementation**

`internal/storage/ports.go`:
```go
package storage

import (
	"context"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// User is the storage-level user record (includes password hash for auth).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    string
}

// QuestionWithSession is a question joined with its session_questions link.
type QuestionWithSession struct {
	*domain.Question
	SessionID           string
	ImageID             string
	ExtractedNumber     int
	ExtractedConfidence float64
}

// UserRepo manages user records.
type UserRepo interface {
	Create(ctx context.Context, email, passwordHash, role string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByID(ctx context.Context, id string) (*User, error)
}

// SessionRepo manages session records.
type SessionRepo interface {
	Create(ctx context.Context, userID string, durationSec, bufferSec int) (*domain.Session, error)
	FindByID(ctx context.Context, id string) (*domain.Session, error)
	ListByUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Session, error)
	Close(ctx context.Context, id string) error
}

// ImageRepo manages uploaded image records.
type ImageRepo interface {
	Create(ctx context.Context, sessionID string, original []byte, mime string, width, height int) (string, error)
	FindByID(ctx context.Context, id string) (*domain.Image, error)
	ListBySession(ctx context.Context, sessionID string) ([]*domain.Image, error)
	UpdateEnhanced(ctx context.Context, id string, enhanced []byte) error
	UpdateVerificationReport(ctx context.Context, id string, report []byte) error
	UpdateExtractionError(ctx context.Context, id string, errJSON []byte) error
	CleanBytes(ctx context.Context, id string) error
}

// QuestionRepo manages the canonical question knowledge base.
type QuestionRepo interface {
	Create(ctx context.Context, q *domain.Question) (string, error)
	FindByID(ctx context.Context, id string) (*domain.Question, error)
	FindExact(ctx context.Context, hash string) (*domain.Question, error)
	FindSemantic(ctx context.Context, embedding []float32, threshold float64) (*domain.Question, error)
	UpdateFromVerification(ctx context.Context, id string, confidence float64, explanation string) error
	ListForUser(ctx context.Context, sessionID string, statusFilter string, limit, offset int) ([]*QuestionWithSession, error)
	ListForModeration(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*domain.Question, error)
	UpdateByExpert(ctx context.Context, id string, answers []string, choices []string, explanation string, confidence float64, tags []string, expertID string) error
	CountUnresolvedForImage(ctx context.Context, imageID string) (int, error)
	LinkToSession(ctx context.Context, sessionID, imageID, questionID string, number int, confidence float64) error
}

// JobQueue manages the Postgres-backed job queue.
type JobQueue interface {
	Enqueue(ctx context.Context, imageID, sessionID string) (string, error)
	Claim(ctx context.Context) (*domain.Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id, errMsg string) error
	ReaperReclaim(ctx context.Context, staleThreshold time.Duration) (int, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/storage/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/storage/ports.go
git commit -m "feat(storage): add repository interface definitions"
```

---

## Task 7: Postgres Pool + Migrations

**Files:**
- Create: `internal/storage/postgres/migrations.go`
- Create: `internal/storage/postgres/pool.go`
- Create: `internal/storage/postgres/migrations/0001_extensions.sql`
- Create: `internal/storage/postgres/migrations/0002_core.sql`

- [ ] **Step 1: Write migration 0001**

`internal/storage/postgres/migrations/0001_extensions.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('user', 'expert')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tags (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE
);

INSERT INTO tags (name) VALUES ('ai-generated') ON CONFLICT DO NOTHING;
INSERT INTO tags (name) VALUES ('needs-manual') ON CONFLICT DO NOTHING;
```

- [ ] **Step 2: Write migration 0002**

`internal/storage/postgres/migrations/0002_core.sql`:
```sql
CREATE TABLE IF NOT EXISTS sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    duration_seconds int NOT NULL CHECK (duration_seconds > 0),
    buffer_seconds   int NOT NULL CHECK (buffer_seconds >= 0),
    started_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'expired'))
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, started_at DESC);

CREATE TABLE IF NOT EXISTS images (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id          uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    original            bytea,
    enhanced            bytea,
    mime                text NOT NULL,
    width               int,
    height              int,
    verification_report jsonb,
    extraction_error    jsonb,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_images_session ON images(session_id, created_at);

CREATE TABLE IF NOT EXISTS questions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    number              int NOT NULL,
    question            text NOT NULL DEFAULT '',
    question_normalized text NOT NULL DEFAULT '',
    question_hash       text NOT NULL UNIQUE,
    multiple_correct    boolean NOT NULL DEFAULT false,
    choices             jsonb NOT NULL DEFAULT '[]',
    answers             jsonb NOT NULL DEFAULT '[]',
    choice_labeling     text NOT NULL DEFAULT 'letter' CHECK (choice_labeling IN ('letter', 'number')),
    confidence          numeric(3,2) NOT NULL DEFAULT 0,
    explanation         text NOT NULL DEFAULT '',
    embedding           vector(1536),
    status              text NOT NULL DEFAULT 'moderation' CHECK (status IN ('moderation', 'verified', 'error')),
    verified_at         timestamptz,
    verified_by         uuid REFERENCES users(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);
CREATE INDEX IF NOT EXISTS idx_questions_embedding ON questions USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

CREATE TABLE IF NOT EXISTS session_questions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id            uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    image_id              uuid NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    question_id           uuid NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    extracted_number      int NOT NULL,
    extracted_confidence  numeric(3,2) NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE(session_id, image_id, question_id)
);
CREATE INDEX IF NOT EXISTS idx_session_questions_image ON session_questions(image_id);
CREATE INDEX IF NOT EXISTS idx_session_questions_session ON session_questions(session_id);

CREATE TABLE IF NOT EXISTS question_tags (
    question_id uuid NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    tag_id      uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, tag_id)
);

CREATE TABLE IF NOT EXISTS jobs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    image_id    uuid NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    attempts    int NOT NULL DEFAULT 0,
    last_error  text,
    queued_at   timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_jobs_status_queued ON jobs(status, queued_at);
```

- [ ] **Step 3: Write the embed file**

`internal/storage/postgres/migrations.go`:
```go
package postgres

import "embed"

//go:embed migrations/*.sql
var migrationFS embed.FS
```

- [ ] **Step 4: Write the pool + migration runner**

`internal/storage/postgres/pool.go`:
```go
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/config"
)

// NewPool creates a PGX connection pool from config.
func NewPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// RunMigrations applies all embedded SQL files in order.
// Migrations are idempotent (CREATE TABLE IF NOT EXISTS).
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		slog.Info("migration applied", "file", name)
	}
	return nil
}
```

- [ ] **Step 5: Verify it compiles**

Run: `go build ./internal/storage/postgres/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add internal/storage/postgres/
git commit -m "feat(storage): add pgx pool, embedded migrations, and migration runner"
```

---

## Task 8: Testcontainers Helper + User Repository

**Files:**
- Create: `internal/storage/postgres/testhelpers_test.go`
- Create: `internal/storage/postgres/user_repo.go`
- Test: `internal/storage/postgres/user_repo_test.go`

> **Note:** Storage tests require Docker running. They skip in `-short` mode.

- [ ] **Step 1: Write the Testcontainers helper**

`internal/storage/postgres/testhelpers_test.go`:
```go
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"pgvector/pgvector:pg16",
		postgres.WithDatabase("coeus_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pool
}
```

- [ ] **Step 2: Write the failing test**

`internal/storage/postgres/user_repo_test.go`:
```go
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestUserRepo_CreateAndFindByEmail(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	user, err := repo.Create(ctx, "test@example.com", "hashed-pwd", "user")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID == "" {
		t.Fatal("expected non-empty user ID")
	}
	if user.Email != "test@example.com" {
		t.Errorf("email = %q", user.Email)
	}

	found, err := repo.FindByEmail(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("found ID = %q, want %q", found.ID, user.ID)
	}
	if found.PasswordHash != "hashed-pwd" {
		t.Errorf("password_hash = %q", found.PasswordHash)
	}
}

func TestUserRepo_CreateDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "dup@example.com", "hash", "user")
	_, err := repo.Create(ctx, "dup@example.com", "hash2", "user")
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got: %v", err)
	}
}

func TestUserRepo_FindByEmailNotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.FindByEmail(ctx, "nonexistent@example.com")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUserRepo_FindByID(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	created, _ := repo.Create(ctx, "byid@example.com", "hash", "expert")
	found, err := repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Role != "expert" {
		t.Errorf("role = %q", found.Role)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestUserRepo -v -timeout 120s`
Expected: FAIL — `undefined: NewUserRepo`

- [ ] **Step 4: Write the implementation**

`internal/storage/postgres/user_repo.go`:
```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

var _ storage.UserRepo = (*UserRepo)(nil)

func (r *UserRepo) Create(ctx context.Context, email, passwordHash, role string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, role,
		          to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`, email, passwordHash, role)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("create user: %w", domain.ErrDuplicate)
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE email = $1
	`, email)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user by email: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return &u, nil
}

func (r *UserRepo) FindByID(ctx context.Context, id string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE id = $1
	`, id)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user by id: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &u, nil
}

// isUniqueViolation checks for Postgres SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestUserRepo -v -timeout 120s`
Expected: PASS (requires Docker)

- [ ] **Step 6: Commit**

```bash
git add internal/storage/postgres/testhelpers_test.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go
git commit -m "feat(storage): add user repository with testcontainers tests"
```

---

## Task 9: Session Repository

**Files:**
- Create: `internal/storage/postgres/session_repo.go`
- Test: `internal/storage/postgres/session_repo_test.go`

- [ ] **Step 1: Write the failing test**

`internal/storage/postgres/session_repo_test.go`:
```go
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestSessionRepo_Create(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "sess@example.com", "hash", "user")
	sess, err := sessRepo.Create(ctx, user.ID, 3600, 300)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("empty session ID")
	}
	if sess.DurationSeconds != 3600 {
		t.Errorf("duration = %d", sess.DurationSeconds)
	}
	if sess.Status != "open" {
		t.Errorf("status = %q", sess.Status)
	}
}

func TestSessionRepo_FindByIDNotFound(t *testing.T) {
	pool := setupTestDB(t)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	_, err := sessRepo.FindByID(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestSessionRepo_ListByUser(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "list@example.com", "hash", "user")
	sessRepo.Create(ctx, user.ID, 3600, 300)
	sessRepo.Create(ctx, user.ID, 1800, 120)

	list, err := sessRepo.ListByUser(ctx, user.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestSessionRepo_Close(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "close@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	sessRepo.Close(ctx, sess.ID)

	found, _ := sessRepo.FindByID(ctx, sess.ID)
	if found.Status != "closed" {
		t.Errorf("status = %q, want 'closed'", found.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestSessionRepo -v -timeout 120s`
Expected: FAIL — `undefined: NewSessionRepo`

- [ ] **Step 3: Write the implementation**

`internal/storage/postgres/session_repo.go`:
```go
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

var _ storage.SessionRepo = (*SessionRepo)(nil)

func (r *SessionRepo) Create(ctx context.Context, userID string, durationSec, bufferSec int) (*domain.Session, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, duration_seconds, buffer_seconds, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $2 + $3))
		RETURNING id, user_id, duration_seconds, buffer_seconds,
		          to_char(started_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          to_char(expires_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          status
	`, userID, durationSec, bufferSec)

	var s domain.Session
	err := row.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
		&s.StartedAt, &s.ExpiresAt, &s.Status)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &s, nil
}

func (r *SessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, duration_seconds, buffer_seconds,
		       to_char(started_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(expires_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       status
		FROM sessions WHERE id = $1
	`, id)

	var s domain.Session
	err := row.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
		&s.StartedAt, &s.ExpiresAt, &s.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find session: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find session: %w", err)
	}
	return &s, nil
}

func (r *SessionRepo) ListByUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, duration_seconds, buffer_seconds,
		       to_char(started_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(expires_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       status
		FROM sessions WHERE user_id = $1
		ORDER BY started_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*domain.Session
	for rows.Next() {
		var s domain.Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
			&s.StartedAt, &s.ExpiresAt, &s.Status); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, &s)
	}
	return sessions, nil
}

func (r *SessionRepo) Close(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE sessions SET status = 'closed' WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestSessionRepo -v -timeout 120s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/session_repo.go internal/storage/postgres/session_repo_test.go
git commit -m "feat(storage): add session repository with testcontainers tests"
```

---

## Task 10: Image Repository

**Files:**
- Create: `internal/storage/postgres/image_repo.go`
- Test: `internal/storage/postgres/image_repo_test.go`

- [ ] **Step 1: Write the failing test**

`internal/storage/postgres/image_repo_test.go`:
```go
package postgres

import (
	"context"
	"testing"
)

func TestImageRepo_CreateAndFind(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "img@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	imgID, err := imgRepo.Create(ctx, sess.ID, []byte("fake-jpeg"), "image/jpeg", 800, 600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if imgID == "" {
		t.Fatal("empty image ID")
	}

	img, err := imgRepo.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if img.Mime != "image/jpeg" {
		t.Errorf("mime = %q", img.Mime)
	}
	if string(img.Original) != "fake-jpeg" {
		t.Error("original bytes mismatch")
	}
}

func TestImageRepo_UpdateEnhanced(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "enh@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	imgRepo.UpdateEnhanced(ctx, imgID, []byte("enhanced"))

	img, _ := imgRepo.FindByID(ctx, imgID)
	if string(img.Enhanced) != "enhanced" {
		t.Errorf("enhanced = %q", string(img.Enhanced))
	}
}

func TestImageRepo_CleanBytes(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "clean@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)
	imgRepo.UpdateEnhanced(ctx, imgID, []byte("enhanced"))

	imgRepo.CleanBytes(ctx, imgID)

	img, _ := imgRepo.FindByID(ctx, imgID)
	if img.Original != nil {
		t.Error("original should be nil after cleanup")
	}
	if img.Enhanced != nil {
		t.Error("enhanced should be nil after cleanup")
	}
	if img.Mime != "image/jpeg" {
		t.Error("metadata should remain after cleanup")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestImageRepo -v -timeout 120s`
Expected: FAIL — `undefined: NewImageRepo`

- [ ] **Step 3: Write the implementation**

`internal/storage/postgres/image_repo.go`:
```go
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type ImageRepo struct {
	pool *pgxpool.Pool
}

func NewImageRepo(pool *pgxpool.Pool) *ImageRepo {
	return &ImageRepo{pool: pool}
}

var _ storage.ImageRepo = (*ImageRepo)(nil)

func (r *ImageRepo) Create(ctx context.Context, sessionID string, original []byte, mime string, width, height int) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO images (session_id, original, mime, width, height)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, sessionID, original, mime, width, height).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create image: %w", err)
	}
	return id, nil
}

func (r *ImageRepo) FindByID(ctx context.Context, id string) (*domain.Image, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, session_id, original, enhanced, mime, width, height,
		       verification_report, extraction_error,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM images WHERE id = $1
	`, id)

	var img domain.Image
	err := row.Scan(&img.ID, &img.SessionID, &img.Original, &img.Enhanced,
		&img.Mime, &img.Width, &img.Height,
		&img.VerificationReport, &img.ExtractionError, &img.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find image: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find image: %w", err)
	}
	return &img, nil
}

func (r *ImageRepo) ListBySession(ctx context.Context, sessionID string) ([]*domain.Image, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, session_id, original, enhanced, mime, width, height,
		       verification_report, extraction_error,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM images WHERE session_id = $1 ORDER BY created_at
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	var images []*domain.Image
	for rows.Next() {
		var img domain.Image
		if err := rows.Scan(&img.ID, &img.SessionID, &img.Original, &img.Enhanced,
			&img.Mime, &img.Width, &img.Height,
			&img.VerificationReport, &img.ExtractionError, &img.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		images = append(images, &img)
	}
	return images, nil
}

func (r *ImageRepo) UpdateEnhanced(ctx context.Context, id string, enhanced []byte) error {
	_, err := r.pool.Exec(ctx, `UPDATE images SET enhanced = $1 WHERE id = $2`, enhanced, id)
	if err != nil {
		return fmt.Errorf("update enhanced: %w", err)
	}
	return nil
}

func (r *ImageRepo) UpdateVerificationReport(ctx context.Context, id string, report []byte) error {
	_, err := r.pool.Exec(ctx, `UPDATE images SET verification_report = $1 WHERE id = $2`, report, id)
	if err != nil {
		return fmt.Errorf("update verification report: %w", err)
	}
	return nil
}

func (r *ImageRepo) UpdateExtractionError(ctx context.Context, id string, errJSON []byte) error {
	_, err := r.pool.Exec(ctx, `UPDATE images SET extraction_error = $1 WHERE id = $2`, errJSON, id)
	if err != nil {
		return fmt.Errorf("update extraction error: %w", err)
	}
	return nil
}

func (r *ImageRepo) CleanBytes(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE images SET original = NULL, enhanced = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("clean image bytes: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestImageRepo -v -timeout 120s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/image_repo.go internal/storage/postgres/image_repo_test.go
git commit -m "feat(storage): add image repository with bytea and cleanup"
```

---

## Task 11: Question Repository (Exact + Semantic Dedup)

**Files:**
- Create: `internal/storage/postgres/question_repo.go`
- Test: `internal/storage/postgres/question_repo_test.go`

> This is the most critical repository — it implements the hybrid dedup (exact hash + pgvector semantic).

- [ ] **Step 1: Write the failing test**

`internal/storage/postgres/question_repo_test.go`:
```go
package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestQuestionRepo_CreateAndFindExact(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Question: "What is the capital of France?",
		QuestionNorm: "what is the capital of france", QuestionHash: "abc123hash",
		Choices: []string{"Paris", "London", "Berlin"}, Answers: []string{"Paris"},
		ChoiceLabeling: "letter", Confidence: 0.95,
		Explanation: "Paris is the capital.", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated", "geography"},
	}
	id, err := repo.Create(ctx, q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty question ID")
	}

	found, err := repo.FindExact(ctx, "abc123hash")
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}
	if found.ID != id {
		t.Errorf("found ID = %q, want %q", found.ID, id)
	}
	if len(found.Answers) != 1 || found.Answers[0] != "Paris" {
		t.Errorf("answers = %v, want [Paris]", found.Answers)
	}
}

func TestQuestionRepo_FindExactNoMatch(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	found, err := repo.FindExact(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("FindExact err: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil, got %v", found)
	}
}

func TestQuestionRepo_FindSemanticAboveThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Question: "q", QuestionNorm: "q", QuestionHash: "h1",
		Choices: []string{}, Answers: []string{}, Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	repo.Create(ctx, q)

	search := make([]float32, 1536)
	search[0] = 1.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found == nil {
		t.Fatal("expected semantic match, got nil")
	}
}

func TestQuestionRepo_FindSemanticBelowThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Question: "q", QuestionNorm: "q", QuestionHash: "h1",
		Choices: []string{}, Answers: []string{}, Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	repo.Create(ctx, q)

	search := make([]float32, 1536)
	search[1] = 1.0 // orthogonal — cosine similarity = 0.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found != nil {
		t.Error("expected no match, got result")
	}
}

func TestQuestionRepo_UpdateFromVerification(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Question: "q", QuestionNorm: "q", QuestionHash: "h",
		Choices: []string{"a"}, Answers: []string{"a"}, Confidence: 0.90,
		Explanation: "original", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	id, _ := repo.Create(ctx, q)

	repo.UpdateFromVerification(ctx, id, 0.75, "original [VERIFICATION FLAG]")

	found, _ := repo.FindByID(ctx, id)
	if found.Confidence != 0.75 {
		t.Errorf("confidence = %v, want 0.75", found.Confidence)
	}
}

func TestQuestionRepo_CountUnresolvedForImage(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	qRepo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "count@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	statuses := []string{domain.QuestionStatusModeration, domain.QuestionStatusModeration, domain.QuestionStatusVerified}
	for i, status := range statuses {
		q := &domain.Question{
			Number: i + 1, Question: "q", QuestionNorm: "q", QuestionHash: "hash" + string(rune('a'+i)),
			Choices: []string{}, Answers: []string{}, Confidence: 0.9,
			Embedding: make([]float32, 1536), Status: status, Tags: []string{"ai-generated"},
		}
		qID, _ := qRepo.Create(ctx, q)
		qRepo.LinkToSession(ctx, sess.ID, imgID, qID, i+1, 0.9)
	}

	count, err := qRepo.CountUnresolvedForImage(ctx, imgID)
	if err != nil {
		t.Fatalf("CountUnresolvedForImage: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestQuestionRepo -v -timeout 120s`
Expected: FAIL — `undefined: NewQuestionRepo`

- [ ] **Step 3: Write the implementation**

`internal/storage/postgres/question_repo.go`:
```go
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type QuestionRepo struct {
	pool *pgxpool.Pool
}

func NewQuestionRepo(pool *pgxpool.Pool) *QuestionRepo {
	return &QuestionRepo{pool: pool}
}

var _ storage.QuestionRepo = (*QuestionRepo)(nil)

func (r *QuestionRepo) Create(ctx context.Context, q *domain.Question) (string, error) {
	choicesJSON, _ := json.Marshal(q.Choices)
	answersJSON, _ := json.Marshal(q.Answers)

	var embedding interface{}
	if q.Embedding != nil {
		embedding = pgvector.NewVector(q.Embedding)
	}

	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    multiple_correct, choices, answers, choice_labeling, confidence,
		    explanation, embedding, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id
	`, q.Number, q.Question, q.QuestionNorm, q.QuestionHash,
		q.MultipleCorrect, choicesJSON, answersJSON, q.ChoiceLabeling,
		q.Confidence, q.Explanation, embedding, q.Status,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create question: %w", err)
	}

	for _, tagName := range q.Tags {
		if err := r.linkTag(ctx, id, tagName); err != nil {
			return id, fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}
	return id, nil
}

func (r *QuestionRepo) LinkToSession(ctx context.Context, sessionID, imageID, questionID string, number int, confidence float64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO session_questions (session_id, image_id, question_id, extracted_number, extracted_confidence)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, image_id, question_id) DO NOTHING
	`, sessionID, imageID, questionID, number, confidence)
	if err != nil {
		return fmt.Errorf("link question to session: %w", err)
	}
	return nil
}

func (r *QuestionRepo) FindByID(ctx context.Context, id string) (*domain.Question, error) {
	row := r.pool.QueryRow(ctx, questionSelectBase+` WHERE q.id = $1`, id)
	return scanQuestion(row)
}

func (r *QuestionRepo) FindExact(ctx context.Context, hash string) (*domain.Question, error) {
	row := r.pool.QueryRow(ctx, questionSelectBase+` WHERE q.question_hash = $1`, hash)
	q, err := scanQuestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // no match — not an error
		}
		return nil, err
	}
	return q, nil
}

func (r *QuestionRepo) FindSemantic(ctx context.Context, embedding []float32, threshold float64) (*domain.Question, error) {
	maxDist := 1.0 - threshold
	row := r.pool.QueryRow(ctx, `
		SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
		       q.multiple_correct, q.choices, q.answers, q.choice_labeling,
		       q.confidence, q.explanation,
		       to_char(q.verified_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(q.verified_by, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       q.status
		FROM questions q
		WHERE q.embedding IS NOT NULL
		  AND q.embedding <=> $1 <= $2
		ORDER BY q.embedding <=> $1
		LIMIT 1
	`, pgvector.NewVector(embedding), maxDist)

	q, err := scanQuestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return q, nil
}

func (r *QuestionRepo) UpdateFromVerification(ctx context.Context, id string, confidence float64, explanation string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE questions SET confidence = $1, explanation = $2, updated_at = now()
		WHERE id = $3
	`, confidence, explanation, id)
	if err != nil {
		return fmt.Errorf("update from verification: %w", err)
	}
	return nil
}

func (r *QuestionRepo) ListForUser(ctx context.Context, sessionID string, statusFilter string, limit, offset int) ([]*storage.QuestionWithSession, error) {
	query := `
		SELECT q.id, q.number, q.question, q.multiple_correct, q.choices, q.answers,
		       q.choice_labeling, q.confidence, q.status,
		       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
		FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		WHERE sq.session_id = $1`
	args := []interface{}{sessionID}
	idx := 2
	if statusFilter != "" {
		query += fmt.Sprintf(` AND q.status = $%d`, idx)
		args = append(args, statusFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY sq.extracted_number LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list questions for user: %w", err)
	}
	defer rows.Close()

	var results []*storage.QuestionWithSession
	for rows.Next() {
		var qws storage.QuestionWithSession
		qws.Question = &domain.Question{}
		var choices, answers []byte
		if err := rows.Scan(
			&qws.ID, &qws.Number, &qws.Question, &qws.MultipleCorrect,
			&choices, &answers, &qws.ChoiceLabeling, &qws.Confidence, &qws.Status,
			&qws.SessionID, &qws.ImageID, &qws.ExtractedNumber, &qws.ExtractedConfidence,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		json.Unmarshal(choices, &qws.Choices)
		json.Unmarshal(answers, &qws.Answers)
		results = append(results, &qws)
	}
	return results, nil
}

func (r *QuestionRepo) ListForModeration(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*domain.Question, error) {
	query := `
		SELECT DISTINCT q.id, q.number, q.question, q.question_normalized, q.question_hash,
		       q.multiple_correct, q.choices, q.answers, q.choice_labeling,
		       q.confidence, q.explanation,
		       to_char(q.verified_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(q.verified_by, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       q.status
		FROM questions q`
	args := []interface{}{}
	idx := 1
	if tagFilter != "" {
		query += ` JOIN question_tags qt ON qt.question_id = q.id JOIN tags t ON t.id = qt.tag_id`
	}
	query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
	args = append(args, statusFilter)
	idx++
	if tagFilter != "" {
		query += fmt.Sprintf(` AND t.name = $%d`, idx)
		args = append(args, tagFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY q.created_at LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list for moderation: %w", err)
	}
	defer rows.Close()

	var questions []*domain.Question
	for rows.Next() {
		q, err := scanQuestionRow(rows)
		if err != nil {
			return nil, err
		}
		q.Tags, _ = r.getTags(ctx, q.ID)
		questions = append(questions, q)
	}
	return questions, nil
}

func (r *QuestionRepo) UpdateByExpert(ctx context.Context, id string, answers, choices []string, explanation string, confidence float64, tags []string, expertID string) error {
	choicesJSON, _ := json.Marshal(choices)
	answersJSON, _ := json.Marshal(answers)

	_, err := r.pool.Exec(ctx, `
		UPDATE questions
		SET answers = $1, choices = $2, explanation = $3, confidence = $4,
		    status = 'verified', verified_at = now(), verified_by = $5, updated_at = now()
		WHERE id = $6
	`, answersJSON, choicesJSON, explanation, confidence, expertID, id)
	if err != nil {
		return fmt.Errorf("update by expert: %w", err)
	}

	r.pool.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id)
	for _, tagName := range tags {
		r.linkTag(ctx, id, tagName)
	}
	return nil
}

func (r *QuestionRepo) CountUnresolvedForImage(ctx context.Context, imageID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		WHERE sq.image_id = $1 AND q.status IN ('moderation', 'error')
	`, imageID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unresolved: %w", err)
	}
	return count, nil
}

func (r *QuestionRepo) linkTag(ctx context.Context, questionID, tagName string) error {
	var tagID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tags (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`, tagName).Scan(&tagID)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO question_tags (question_id, tag_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, questionID, tagID)
	return err
}

func (r *QuestionRepo) getTags(ctx context.Context, questionID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.name FROM question_tags qt
		JOIN tags t ON t.id = qt.tag_id
		WHERE qt.question_id = $1
	`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tags = append(tags, name)
	}
	return tags, nil
}

const questionSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.multiple_correct, q.choices, q.answers, q.choice_labeling,
	       q.confidence, q.explanation,
	       to_char(q.verified_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       to_char(q.verified_by, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.status
	FROM questions q`

func scanQuestion(row pgx.Row) (*domain.Question, error) {
	q := &domain.Question{}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	err := row.Scan(
		&q.ID, &q.Number, &q.Question, &q.QuestionNorm, &q.QuestionHash,
		&q.MultipleCorrect, &choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(choices, &q.Choices)
	json.Unmarshal(answers, &q.Answers)
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verifiedBy
	return q, nil
}

func scanQuestionRow(rows pgx.Rows) (*domain.Question, error) {
	q := &domain.Question{}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	err := rows.Scan(
		&q.ID, &q.Number, &q.Question, &q.QuestionNorm, &q.QuestionHash,
		&q.MultipleCorrect, &choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
	)
	if err != nil {
		return nil, fmt.Errorf("scan question: %w", err)
	}
	json.Unmarshal(choices, &q.Choices)
	json.Unmarshal(answers, &q.Answers)
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verifiedBy
	return q, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestQuestionRepo -v -timeout 120s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/question_repo.go internal/storage/postgres/question_repo_test.go
git commit -m "feat(storage): add question repository with hybrid dedup (exact + pgvector)"
```

---

## Task 12: Job Queue Repository

**Files:**
- Create: `internal/storage/postgres/job_queue.go`
- Test: `internal/storage/postgres/job_queue_test.go`

- [ ] **Step 1: Write the failing test**

`internal/storage/postgres/job_queue_test.go`:
```go
package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestJobQueue_EnqueueAndClaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "job@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jobID, err := jq.Enqueue(ctx, imgID, sess.ID)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job, err := jq.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if job == nil || job.ID != jobID {
		t.Fatalf("expected job %s, got %v", jobID, job)
	}
	if job.Status != domain.JobStatusProcessing {
		t.Errorf("status = %q, want processing", job.Status)
	}
}

func TestJobQueue_ClaimEmpty(t *testing.T) {
	pool := setupTestDB(t)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	job, err := jq.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if job != nil {
		t.Error("expected nil on empty queue")
	}
}

func TestJobQueue_ConcurrentClaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "conc@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	for i := 0; i < 10; i++ {
		imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)
		jq.Enqueue(ctx, imgID, sess.ID)
	}

	var claimed int64
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := jq.Claim(context.Background())
				if err != nil || job == nil {
					return
				}
				atomic.AddInt64(&claimed, 1)
			}
		}()
	}
	wg.Wait()

	if claimed != 10 {
		t.Errorf("claimed = %d, want 10 (each job claimed exactly once)", claimed)
	}
}

func TestJobQueue_ReaperReclaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "reaper@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jq.Enqueue(ctx, imgID, sess.ID)
	jq.Claim(ctx) // mark as processing

	reclaimed, err := jq.ReaperReclaim(ctx, 0*time.Second)
	if err != nil {
		t.Fatalf("ReaperReclaim: %v", err)
	}
	if reclaimed != 1 {
		t.Errorf("reclaimed = %d, want 1", reclaimed)
	}

	job, err := jq.Claim(ctx)
	if err != nil || job == nil {
		t.Fatal("expected job after reclaim")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue -v -timeout 120s`
Expected: FAIL — `undefined: NewJobQueue`

- [ ] **Step 3: Write the implementation**

`internal/storage/postgres/job_queue.go`:
```go
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type JobQueue struct {
	pool *pgxpool.Pool
}

func NewJobQueue(pool *pgxpool.Pool) *JobQueue {
	return &JobQueue{pool: pool}
}

var _ storage.JobQueue = (*JobQueue)(nil)

func (q *JobQueue) Enqueue(ctx context.Context, imageID, sessionID string) (string, error) {
	var id string
	err := q.pool.QueryRow(ctx, `
		INSERT INTO jobs (image_id, session_id, status)
		VALUES ($1, $2, 'pending')
		RETURNING id
	`, imageID, sessionID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("enqueue job: %w", err)
	}
	q.pool.Exec(ctx, "NOTIFY jobs_new")
	return id, nil
}

func (q *JobQueue) Claim(ctx context.Context) (*domain.Job, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE jobs
		SET status = 'processing', started_at = now(), attempts = attempts + 1
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			ORDER BY queued_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, image_id, session_id, status, attempts,
		          to_char(queued_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          to_char(started_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`)

	var job domain.Job
	err = row.Scan(&job.ID, &job.ImageID, &job.SessionID, &job.Status,
		&job.Attempts, &job.QueuedAt, &job.StartedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no job available
		}
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return &job, nil
}

func (q *JobQueue) Complete(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status = 'done', finished_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

func (q *JobQueue) Fail(ctx context.Context, id, errMsg string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', finished_at = now(), last_error = $1 WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

func (q *JobQueue) ReaperReclaim(ctx context.Context, staleThreshold time.Duration) (int, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', started_at = NULL
		WHERE status = 'processing'
		  AND started_at < now() - $1::interval
	`, staleThreshold.String())
	if err != nil {
		return 0, fmt.Errorf("reaper reclaim: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue -v -timeout 120s`
Expected: PASS — concurrent claim test verifies each job claimed exactly once

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/job_queue.go internal/storage/postgres/job_queue_test.go
git commit -m "feat(storage): add job queue with FOR UPDATE SKIP LOCKED and reaper"
```

---

## Task 13: Auth — Password + JWT

**Files:**
- Create: `internal/auth/password.go`
- Create: `internal/auth/jwt.go`
- Test: `internal/auth/password_test.go`
- Test: `internal/auth/jwt_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/auth/password_test.go`:
```go
package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("mypassword")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "mypassword" {
		t.Fatal("hash should not equal plaintext")
	}
	if !VerifyPassword(hash, "mypassword") {
		t.Error("VerifyPassword should return true for correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("VerifyPassword should return false for wrong password")
	}
}

func TestHashPasswordUniqueness(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Error("hashes should differ (bcrypt salt)")
	}
}
```

`internal/auth/jwt_test.go`:
```go
package auth

import (
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
)

func TestIssueAndVerifyToken(t *testing.T) {
	cfg := config.JWTConfig{Secret: "test-secret", AccessTTL: time.Hour}
	mgr := NewJWTManager(cfg)

	token, err := mgr.Issue("user-123", "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID = %q", claims.UserID)
	}
	if claims.Role != "user" {
		t.Errorf("Role = %q", claims.Role)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	cfg := config.JWTConfig{Secret: "test-secret", AccessTTL: -time.Hour}
	mgr := NewJWTManager(cfg)
	token, _ := mgr.Issue("user-123", "user")
	_, err := mgr.Verify(token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerifyWrongSecret(t *testing.T) {
	mgr1 := NewJWTManager(config.JWTConfig{Secret: "s1", AccessTTL: time.Hour})
	mgr2 := NewJWTManager(config.JWTConfig{Secret: "s2", AccessTTL: time.Hour})
	token, _ := mgr1.Issue("user-123", "user")
	_, err := mgr2.Verify(token)
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestVerifyExpertRole(t *testing.T) {
	mgr := NewJWTManager(config.JWTConfig{Secret: "test-secret", AccessTTL: time.Hour})
	token, _ := mgr.Issue("expert-1", "expert")
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Role != "expert" {
		t.Errorf("Role = %q, want 'expert'", claims.Role)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — `undefined: HashPassword`, `undefined: NewJWTManager`

- [ ] **Step 3: Write password.go**

`internal/auth/password.go`:
```go
package auth

import "golang.org/x/crypto/bcrypt"

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
```

- [ ] **Step 4: Write jwt.go**

`internal/auth/jwt.go`:
```go
package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vlgrigoriev/coeus/internal/config"
)

// Claims is the JWT payload carrying user identity and role.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret    []byte
	accessTTL time.Duration
}

func NewJWTManager(cfg config.JWTConfig) *JWTManager {
	return &JWTManager{secret: []byte(cfg.Secret), accessTTL: cfg.AccessTTL}
}

func (m *JWTManager) Issue(userID, role string) (string, error) {
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(m.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *JWTManager) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS — all 6 auth tests green

- [ ] **Step 6: Commit**

```bash
git add internal/auth/
git commit -m "feat(auth): add bcrypt password hashing and JWT issue/verify"
```

---

## Task 14: HTTP Middleware

**Files:**
- Create: `internal/httpapi/middleware.go`
- Test: `internal/httpapi/middleware_test.go`

- [ ] **Step 1: Write the failing test**

`internal/httpapi/middleware_test.go`:
```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
)

func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.GET("/p", func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("u1", "user")
	req := httptest.NewRequest("GET", "/p", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAuthMiddleware_NoToken(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.GET("/p", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/p", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRoleGuard_AllowsExpert(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.PATCH("/q/:id", RoleGuard("expert"), func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("e1", "expert")
	req := httptest.NewRequest("PATCH", "/q/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRoleGuard_BlocksUser(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.PATCH("/q/:id", RoleGuard("expert"), func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("u1", "user")
	req := httptest.NewRequest("PATCH", "/q/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestAuth -v`
Expected: FAIL — `undefined: AuthMiddleware`

- [ ] **Step 3: Write the implementation**

`internal/httpapi/middleware.go`:
```go
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

func AuthMiddleware(jwtMgr *auth.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := jwtMgr.Verify(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		c.Set("claims", claims)
		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)
		c.Next()
	}
}

func RoleGuard(requiredRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role.(string) != requiredRole {
			c.AbortWithStatusJSON(http.StatusForbidden, apiError(domain.ErrForbidden))
			return
		}
		c.Next()
	}
}

func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-Id")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-Id", requestID)

		start := time.Now()
		c.Next()

		slog.Info("request",
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", "request_id", c.GetString("request_id"), "error", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{"code": "internal", "message": "internal server error"},
				})
			}
		}()
		c.Next()
	}
}

// apiError converts a domain error into the uniform API error shape.
// This is the middleware's private copy; handlers have their own in common.go.
func apiError(err error) gin.H {
	var de *domain.Error
	if errors.As(err, &de) {
		return gin.H{"error": gin.H{"code": de.Code, "message": de.Message}}
	}
	return gin.H{"error": gin.H{"code": "internal", "message": "internal server error"}}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/ -run TestAuth -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/middleware.go internal/httpapi/middleware_test.go
git commit -m "feat(httpapi): add auth, role guard, request log, recover middleware"
```

---

## Task 15: Auth HTTP Handlers

**Files:**
- Create: `internal/httpapi/handlers/common.go`
- Create: `internal/httpapi/handlers/auth.go`
- Test: `internal/httpapi/handlers/auth_test.go`

- [ ] **Step 1: Write the shared error helper**

`internal/httpapi/handlers/common.go`:
```go
package handlers

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// errorResponse converts a domain error into the uniform API error shape.
func errorResponse(err error) gin.H {
	var de *domain.Error
	if errors.As(err, &de) {
		return gin.H{"error": gin.H{"code": de.Code, "message": de.Message}}
	}
	return gin.H{"error": gin.H{"code": "internal", "message": "internal server error"}}
}
```

- [ ] **Step 2: Write the failing test**

`internal/httpapi/handlers/auth_test.go`:
```go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type mockUserRepo struct {
	users map[string]*storage.User
}

func (m *mockUserRepo) Create(_ context.Context, email, hash, role string) (*storage.User, error) {
	if _, ok := m.users[email]; ok {
		return nil, fmt.Errorf("create: %w", domain.ErrDuplicate)
	}
	u := &storage.User{ID: uuid.NewString(), Email: email, PasswordHash: hash, Role: role}
	m.users[email] = u
	return u, nil
}
func (m *mockUserRepo) FindByEmail(_ context.Context, email string) (*storage.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
	}
	return u, nil
}
func (m *mockUserRepo) FindByID(_ context.Context, id string) (*storage.User, error) {
	for _, u := range m.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
}

func newTestAuthHandler() (*AuthHandler, *mockUserRepo) {
	repo := &mockUserRepo{users: make(map[string]*storage.User)}
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	return NewAuthHandler(repo, mgr), repo
}

func TestRegisterHandler_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)

	body, _ := json.Marshal(map[string]string{"email": "new@test.com", "password": "pass1234"})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["email"] != "new@test.com" {
		t.Errorf("email = %v", resp["email"])
	}
	if resp["role"] != "user" {
		t.Errorf("role = %v", resp["role"])
	}
}

func TestRegisterHandler_Duplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)

	body, _ := json.Marshal(map[string]string{"email": "dup@test.com", "password": "pass1234"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body)))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestLoginHandler_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)

	regBody, _ := json.Marshal(map[string]string{"email": "login@test.com", "password": "pass1234"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody)))

	loginBody, _ := json.Marshal(map[string]string{"email": "login@test.com", "password": "pass1234"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(loginBody)))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected non-empty token")
	}
}

func TestLoginHandler_WrongPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)

	regBody, _ := json.Marshal(map[string]string{"email": "wp@test.com", "password": "correct"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody)))

	loginBody, _ := json.Marshal(map[string]string{"email": "wp@test.com", "password": "wrong"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(loginBody)))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/httpapi/handlers/ -v`
Expected: FAIL — `undefined: NewAuthHandler`

- [ ] **Step 4: Write the implementation**

`internal/httpapi/handlers/auth.go`:
```go
package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type AuthHandler struct {
	users  storage.UserRepo
	jwtMgr *auth.JWTManager
}

func NewAuthHandler(users storage.UserRepo, jwtMgr *auth.JWTManager) *AuthHandler {
	return &AuthHandler{users: users, jwtMgr: jwtMgr}
}

type registerRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type authResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	user, err := h.users.Create(c.Request.Context(), req.Email, hash, "user")
	if err != nil {
		if errors.Is(err, domain.ErrDuplicate) {
			c.JSON(http.StatusConflict, errorResponse(domain.ErrDuplicate))
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusCreated, userResponse{ID: user.ID, Email: user.Email, Role: user.Role})
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	user, err := h.users.FindByEmail(c.Request.Context(), req.Email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusUnauthorized, errorResponse(domain.ErrUnauthorized))
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, errorResponse(domain.ErrUnauthorized))
		return
	}

	token, err := h.jwtMgr.Issue(user.ID, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusOK, authResponse{Token: token, Role: user.Role})
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	userID, _ := c.Get("user_id")
	role, _ := c.Get("role")

	token, err := h.jwtMgr.Issue(userID.(string), role.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusOK, authResponse{Token: token, Role: role.(string)})
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/httpapi/handlers/ -v`
Expected: PASS — all 4 auth handler tests green

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/handlers/
git commit -m "feat(httpapi): add auth handlers (register, login, refresh) with tests"
```

---

## Task 16: HTTP Server (Router, Healthz, Readyz)

**Files:**
- Create: `internal/httpapi/server.go`

- [ ] **Step 1: Write the implementation**

`internal/httpapi/server.go`:
```go
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type Server struct {
	router   *gin.Engine
	userRepo storage.UserRepo
	jwtMgr   *auth.JWTManager
	pool     *pgxpool.Pool
}

func NewServer(userRepo storage.UserRepo, jwtMgr *auth.JWTManager, pool *pgxpool.Pool) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())

	s := &Server{router: r, userRepo: userRepo, jwtMgr: jwtMgr, pool: pool}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	r := s.router

	// Health
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", s.readyz)

	// Auth
	authHandler := handlers.NewAuthHandler(s.userRepo, s.jwtMgr)
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.POST("/register", authHandler.Register)
		authGroup.POST("/login", authHandler.Login)
		authGroup.POST("/refresh", AuthMiddleware(s.jwtMgr), authHandler.Refresh)
	}

	// Plan 2 will add: sessions, images
	// Plan 3 will add: questions, expert moderation
}

func (s *Server) readyz(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) Handler() http.Handler {
	return s.router
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/httpapi/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/httpapi/server.go
git commit -m "feat(httpapi): add gin server with health/ready and auth routes"
```

---

## Task 17: Composition Root

**Files:**
- Create: `internal/app/wire.go`

- [ ] **Step 1: Write the implementation**

`internal/app/wire.go`:
```go
package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

type App struct {
	Config   *config.Config
	Pool     *pgxpool.Pool
	UserRepo *postgres.UserRepo
	JWTMgr   *auth.JWTManager
	Server   *httpapi.Server
}

func Build(ctx context.Context, cfg *config.Config) (*App, error) {
	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("build pool: %w", err)
	}

	if err := postgres.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	userRepo := postgres.NewUserRepo(pool)
	jwtMgr := auth.NewJWTManager(cfg.JWT)
	server := httpapi.NewServer(userRepo, jwtMgr, pool)

	return &App{
		Config: cfg, Pool: pool, UserRepo: userRepo,
		JWTMgr: jwtMgr, Server: server,
	}, nil
}

func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/app/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/app/wire.go
git commit -m "feat(app): add composition root wiring config to deps"
```

---

## Task 18: Main Entry Point

**Files:**
- Create: `cmd/coeus/main.go`

- [ ] **Step 1: Write the implementation**

`cmd/coeus/main.go`:
```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/vlgrigoriev/coeus/internal/app"
	"github.com/vlgrigoriev/coeus/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	application, err := app.Build(ctx, cfg)
	if err != nil {
		slog.Error("failed to build app", "error", err)
		os.Exit(1)
	}
	defer application.Close()

	slog.Info("coeus started", "addr", cfg.Server.Addr)

	httpServer := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      application.Server.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
			cancel()
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("forced shutdown", "error", err)
	}
	cancel()
	slog.Info("coeus stopped")
}
```

- [ ] **Step 2: Build the binary**

Run: `go build -o coeus ./cmd/coeus/`
Expected: binary `coeus` created, no errors

- [ ] **Step 3: Run all tests**

Run: `go test ./... -timeout 180s`
Expected: all tests pass (storage tests need Docker; use `go test -short ./...` to skip integration tests)

- [ ] **Step 4: Commit**

```bash
git add cmd/coeus/main.go
git commit -m "feat(cmd): add main entry point with graceful shutdown"
```

---

## Plan 1 Complete

After all 18 tasks, the service has:
- Go module with all dependencies
- Domain types (Question, Session, Image, Job, typed errors)
- Config loaded from YAML + env
- Postgres pool + all 6 migrations (pgvector, users, tags, sessions, images, questions, session_questions, question_tags, jobs)
- All 6 repositories tested with Testcontainers (user, session, image, question with hybrid dedup, job queue with concurrent claim)
- JWT auth (bcrypt + golang-jwt/v5)
- Gin middleware (Auth, RoleGuard, RequestLog, Recover)
- Auth endpoints (register, login, refresh)
- Health endpoints (healthz, readyz)
- Composition root + main with graceful shutdown

The binary compiles, all tests pass, and the server starts and serves auth endpoints against a real Postgres.
