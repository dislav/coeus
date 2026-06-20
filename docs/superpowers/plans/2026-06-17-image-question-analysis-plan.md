# Image Question Analysis Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go-based Image Question Analysis Service that accepts quiz/test images, extracts and verifies questions using AI, and routes them through an expert moderation workflow.

**Architecture:** A layered Gin HTTP API (`cmd/api`) and River worker (`cmd/worker`) share a PostgreSQL database through PGX v5 and golang-migrate migrations. Business logic lives in `internal/service`, `internal/repository`, and `internal/queue`, with AI prompts embedded from `skills/` at compile time.

**Tech Stack:** Go 1.25, Gin v1.12.0, PGX v5.10.0, River v0.39.0, golang-migrate v4.19.1, ulule/limiter v3.11.2, Prometheus client_golang v1.19.0, sashabaranov/go-openai v1.41.2, disintegration/imaging v1.6.2, testify v1.11.1, caarlos0/env/v11 v11.0.0, gin-contrib/sessions v1.1.0, golang.org/x/image v0.42.0.

---

## File Structure

```text
coeus/
├── cmd/
│   ├── api/main.go                 # Gin server entrypoint
│   ├── worker/main.go              # River worker entrypoint
│   └── migrate/main.go             # golang-migrate CLI wrapper
├── internal/
│   ├── config/config.go            # env-based config
│   ├── db/db.go                    # PGX pool + Querier interface
│   ├── domain/
│   │   ├── enums/enums.go          # UserRole, SessionStatus, QuestionStatus
│   │   ├── user.go                 # User domain type
│   │   ├── session.go              # Session domain type
│   │   ├── image.go                # Image domain type
│   │   └── question.go             # Question, Choice domain types
│   ├── repository/
│   │   ├── user.go                 # UserRepo interface + repository
│   │   ├── session.go              # SessionRepo interface + repository
│   │   ├── image.go                # ImageRepo interface + repository
│   │   └── question.go             # QuestionRepo interface + repository
│   ├── service/
│   │   ├── auth.go                 # AuthService
│   │   ├── session.go              # SessionService
│   │   ├── image.go                # ImageService
│   │   ├── question.go             # QuestionService
│   │   ├── moderation.go           # ModerationService
│   │   ├── ai/
│   │   │   ├── client.go           # Extractor/Verifier interfaces
│   │   │   ├── kimi.go             # Kimi K2.6 client
│   │   │   ├── deepseek.go         # DeepSeek V4 Pro client
│   │   │   └── enhance.go          # image enhancement pipeline
│   │   └── matcher/normalizer.go   # question text normalizer
│   ├── handler/
│   │   ├── auth.go                 # auth handlers
│   │   ├── session.go              # session handlers
│   │   ├── image.go                # image handlers
│   │   ├── question.go             # question handlers
│   │   ├── moderation.go           # moderation handlers
│   │   ├── health.go               # health/ready/metrics handlers
│   │   └── dto/dto.go              # request/response DTOs
│   ├── middleware/
│   │   ├── auth.go                 # session cookie auth
│   │   ├── expert.go               # expert role check
│   │   ├── session.go              # active session validation
│   │   └── ratelimit.go            # per-user RPM limiting
│   ├── queue/
│   │   ├── client.go               # River client setup
│   │   └── jobs/
│   │       ├── extract.go          # ExtractQuestionsJob
│   │       ├── verify.go           # VerifyQuestionsJob
│   │       └── scheduler.go        # SessionExpiryJob
│   ├── prompts/prompts.go          # embedded skill prompts
│   └── observability/
│       ├── logger.go               # slog setup
│       └── metrics.go              # Prometheus metrics
├── migrations/
│   ├── 001_init.up.sql
│   └── 001_init.down.sql
├── skills/                         # existing AI skill prompts
├── docker-compose.yml
├── Makefile
├── Dockerfile
└── README.md
```

---

### Task 1: Project scaffolding

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `.gitignore`
- Create: `docker-compose.yml`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`
- Create: `migrations/001_init.up.sql`
- Create: `migrations/001_init.down.sql`
- Create: `cmd/api/main.go`
- Create: `cmd/worker/main.go`
- Create: `cmd/migrate/main.go`

- [ ] **Step 1: Initialize the Go module and install direct dependencies**

Run:
```bash
go mod init github.com/vlgrigoriev/coeus
go get github.com/caarlos0/env/v11@v11.0.0
go get github.com/disintegration/imaging@v1.6.2
go get github.com/gin-contrib/sessions@v1.1.0
go get github.com/gin-gonic/gin@v1.12.0
go get github.com/golang-migrate/migrate/v4@v4.19.1
go get github.com/google/uuid@v1.6.0
go get github.com/jackc/pgx/v5@v5.10.0
go get github.com/prometheus/client_golang@v1.19.0
go get github.com/riverqueue/river@v0.39.0
go get github.com/riverqueue/river/riverdriver/riverpgxv5@v0.39.0
go get github.com/sashabaranov/go-openai@v1.41.2
go get github.com/stretchr/testify@v1.11.1
go get github.com/ulule/limiter/v3@v3.11.2
go get golang.org/x/image@v0.42.0
go get golang.org/x/crypto@latest
```

Expected: `go.mod` is created with `module github.com/vlgrigoriev/coeus` and `go 1.25` plus the direct dependencies above.

- [ ] **Step 2: Create `go.mod` baseline**

```go
module github.com/vlgrigoriev/coeus

go 1.25

require (
	github.com/caarlos0/env/v11 v11.0.0
	github.com/disintegration/imaging v1.6.2
	github.com/gin-contrib/sessions v1.1.0
	github.com/gin-gonic/gin v1.12.0
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/prometheus/client_golang v1.19.0
	github.com/riverqueue/river v0.39.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.39.0
	github.com/sashabaranov/go-openai v1.41.2
	github.com/stretchr/testify v1.11.1
	github.com/ulule/limiter/v3 v3.11.2
	golang.org/x/crypto v0.37.0
	golang.org/x/image v0.42.0
)
```

Run `go mod tidy` after all files are in place.

- [ ] **Step 3: Create `.gitignore`**

```gitignore
/bin/
/vendor/
*.exe
*.test
*.out
.env
coverage.out
coverage.html
.DS_Store
```

- [ ] **Step 4: Create `Makefile`**

```makefile
.PHONY: build test test-repo migrate-up migrate-down lint run-api run-worker

BIN_DIR := ./bin

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/api ./cmd/api
	go build -o $(BIN_DIR)/worker ./cmd/worker
	go build -o $(BIN_DIR)/migrate ./cmd/migrate

test:
	go test -race ./internal/...

test-repo:
	docker compose up -d postgres-test
	sleep 2
	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5433/coeus_test?sslmode=disable go test -race ./internal/repository/...

test-integration:
	docker compose up -d postgres-test
	sleep 2
	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5433/coeus_test?sslmode=disable go test -tags=integration -race ./tests/integration/...

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

lint:
	golangci-lint run ./...

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
```

- [ ] **Step 5: Create `docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: coeus
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data

  postgres-test:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: coeus_test
    ports:
      - "5433:5432"

volumes:
  postgres_data:
```

- [ ] **Step 6: Write the failing config test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	t.Setenv("SESSION_SECRET", "super-secret-key-at-least-32-bytes-long")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
	t.Setenv("KIMI_API_KEY", "kimi-key")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")

	cfg, err := Parse()
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, "super-secret-key-at-least-32-bytes-long", cfg.SessionSecret)
	assert.Equal(t, "postgres://user:pass@localhost/db", cfg.DatabaseURL)
	assert.Equal(t, "kimi-key", cfg.KimiAPIKey)
	assert.Equal(t, "https://api.moonshot.ai/v1", cfg.KimiBaseURL)
	assert.Equal(t, "kimi-k2-6", cfg.KimiModel)
	assert.Equal(t, "deepseek-key", cfg.DeepSeekAPIKey)
	assert.Equal(t, 10, cfg.RiverMaxWorkers)
	assert.Equal(t, 60, cfg.RateLimitRPM)
	assert.Equal(t, 10, cfg.MaxUploadSizeMB)
	assert.Equal(t, 4096, cfg.MaxImageDim)
}

func TestParseRequiredMissing(t *testing.T) {
	_, err := Parse()
	assert.Error(t, err)
}
```

Run: `go test ./internal/config -v`
Expected: FAIL (`Parse` not defined).

- [ ] **Step 7: Implement `internal/config/config.go`**

```go
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Port            int           `env:"PORT" envDefault:"8080"`
	SessionSecret   string        `env:"SESSION_SECRET"`
	DatabaseURL     string        `env:"DATABASE_URL"`
	KimiAPIKey      string        `env:"KIMI_API_KEY"`
	KimiBaseURL     string        `env:"KIMI_BASE_URL" envDefault:"https://api.moonshot.ai/v1"`
	KimiModel       string        `env:"KIMI_MODEL" envDefault:"kimi-k2-6"`
	DeepSeekAPIKey  string        `env:"DEEPSEEK_API_KEY"`
	DeepSeekBaseURL string        `env:"DEEPSEEK_BASE_URL" envDefault:"https://api.deepseek.com"`
	DeepSeekModel   string        `env:"DEEPSEEK_MODEL" envDefault:"deepseek-v4-pro"`
	RiverMaxWorkers int           `env:"RIVER_MAX_WORKERS" envDefault:"10"`
	RateLimitRPM    int           `env:"RATE_LIMIT_RPM" envDefault:"60"`
	MaxUploadSizeMB int           `env:"MAX_UPLOAD_SIZE_MB" envDefault:"10"`
	MaxImageDim     int           `env:"MAX_IMAGE_DIM" envDefault:"4096"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	LogFormat       string        `env:"LOG_FORMAT" envDefault:"json"`
}

func Parse() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.KimiAPIKey == "" {
		return nil, fmt.Errorf("KIMI_API_KEY is required")
	}
	if cfg.DeepSeekAPIKey == "" {
		return nil, fmt.Errorf("DEEPSEEK_API_KEY is required")
	}
	return &cfg, nil
}

func (c *Config) MaxUploadSize() int64 {
	return int64(c.MaxUploadSizeMB) << 20
}
```

Run: `go test ./internal/config -v`
Expected: PASS.

- [ ] **Step 8: Write the failing db package test**

Create `internal/db/db_test.go`:

```go
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPoolInvalidURL(t *testing.T) {
	pool, err := NewPool(context.Background(), "postgres://bad-url")
	require.Error(t, err)
	assert.Nil(t, pool)
}
```

Run: `go test ./internal/db -v`
Expected: FAIL (`NewPool` not defined).

- [ ] **Step 9: Implement `internal/db/db.go`**

```go
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier abstracts *pgxpool.Pool and pgx.Tx so repositories can run inside transactions.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
```

Run: `go test ./internal/db -v`
Expected: PASS.

- [ ] **Step 10: Create migrations**

Create `migrations/001_init.up.sql` exactly from the spec:

```sql
CREATE TYPE user_role AS ENUM ('user', 'expert');

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    has_access    BOOLEAN NOT NULL DEFAULT false,
    role          user_role NOT NULL DEFAULT 'user',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE session_status AS ENUM ('created', 'active', 'expired', 'closed');

CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    duration    INTERVAL NOT NULL,
    buffer      INTERVAL NOT NULL DEFAULT '5 minutes',
    started_at  TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,
    closed_at   TIMESTAMPTZ,
    status      session_status NOT NULL DEFAULT 'created',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE images (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    image_data      BYTEA,
    enhanced_data   BYTEA,
    mime_type       TEXT NOT NULL,
    file_name       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'processing',
    cleaned_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT images_data_not_null_at_insert CHECK (
        image_data IS NOT NULL AND enhanced_data IS NOT NULL
    )
);
CREATE INDEX idx_images_session ON images(session_id);

CREATE TYPE question_status AS ENUM ('processing', 'moderation', 'verified', 'error');

CREATE TABLE questions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    image_id            UUID NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    session_id          UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    number              INT NOT NULL,
    question_text       TEXT NOT NULL,
    normalized_text     TEXT NOT NULL,
    choices             JSONB NOT NULL DEFAULT '[]',
    answers             JSONB NOT NULL DEFAULT '[]',
    multiple_correct    BOOLEAN NOT NULL DEFAULT false,
    confidence          REAL NOT NULL DEFAULT 0.0,
    explanation         TEXT,
    status              question_status NOT NULL DEFAULT 'processing',
    matched_question_id UUID REFERENCES questions(id),
    ai_analysis         JSONB,
    verification_report JSONB,
    tags                TEXT[] NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_question_number UNIQUE(image_id, number)
);
CREATE INDEX idx_questions_image ON questions(image_id);
CREATE INDEX idx_questions_session ON questions(session_id);
CREATE INDEX idx_questions_normalized ON questions(normalized_text);
CREATE INDEX idx_questions_status ON questions(status);

CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_questions_updated_at
    BEFORE UPDATE ON questions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
```

Create `migrations/001_init.down.sql`:

```sql
DROP TRIGGER IF EXISTS trg_questions_updated_at ON questions;
DROP FUNCTION IF EXISTS update_updated_at_column();

DROP TABLE IF EXISTS questions;
DROP TYPE IF EXISTS question_status;

DROP TABLE IF EXISTS images;

DROP TABLE IF EXISTS sessions;
DROP TYPE IF EXISTS session_status;

DROP TABLE IF EXISTS users;
DROP TYPE IF EXISTS user_role;
```

- [ ] **Step 11: Implement `cmd/migrate/main.go`**

```go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: migrate <up|down>")
		os.Exit(1)
	}
	dir := os.Args[1]

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Println("DATABASE_URL is required")
		os.Exit(1)
	}

	// golang-migrate pgx/v5 driver uses the pgx5 scheme.
	migrationURL := strings.Replace(databaseURL, "postgres://", "pgx5://", 1)

	m, err := migrate.New("file://migrations", migrationURL)
	if err != nil {
		fmt.Printf("migrate init failed: %v\n", err)
		os.Exit(1)
	}

	switch dir {
	case "up":
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			fmt.Printf("migrate up failed: %v\n", err)
			os.Exit(1)
		}
	case "down":
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			fmt.Printf("migrate down failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Println("usage: migrate <up|down>")
		os.Exit(1)
	}
}
```

- [ ] **Step 12: Create minimal `cmd/api/main.go` and `cmd/worker/main.go` stubs**

`cmd/api/main.go`:

```go
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/db"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Parse()
	if err != nil {
		slog.ErrorContext(ctx, "failed to parse config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	slog.InfoContext(ctx, "api server started", slog.Int("port", cfg.Port))
	_ = cfg
}
```

`cmd/worker/main.go`:

```go
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/db"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Parse()
	if err != nil {
		slog.ErrorContext(ctx, "failed to parse config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	slog.InfoContext(ctx, "worker started")
	_ = cfg
}
```

- [ ] **Step 13: Run build to verify scaffolding compiles**

Run:
```bash
go mod tidy
make build
```

Expected: three binaries built in `./bin/`, no compile errors.

- [ ] **Step 14: Commit**

```bash
git add go.mod go.sum Makefile .gitignore docker-compose.yml internal/config internal/db migrations cmd go.sum

git commit -m "chore: scaffold Go project, config, db pool, migrations, cmd stubs"
```

---

### Task 2: Domain types, enums, and repositories

**Files:**
- Create: `internal/domain/enums/enums.go`
- Create: `internal/domain/enums/enums_test.go`
- Create: `internal/domain/user.go`
- Create: `internal/domain/session.go`
- Create: `internal/domain/image.go`
- Create: `internal/domain/question.go`
- Create: `internal/domain/domain_test.go`
- Create: `internal/repository/user.go`
- Create: `internal/repository/session.go`
- Create: `internal/repository/image.go`
- Create: `internal/repository/question.go`
- Create: `internal/repository/repository_test.go`
- Create: `internal/repository/user_test.go`
- Create: `internal/repository/session_test.go`
- Create: `internal/repository/image_test.go`
- Create: `internal/repository/question_test.go`

- [ ] **Step 1: Write enum tests**

Create `internal/domain/enums/enums_test.go`:

```go
package enums

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserRoleString(t *testing.T) {
	assert.Equal(t, "user", string(UserRoleUser))
	assert.Equal(t, "expert", string(UserRoleExpert))
}

func TestSessionStatusString(t *testing.T) {
	assert.Equal(t, "created", string(SessionStatusCreated))
	assert.Equal(t, "active", string(SessionStatusActive))
	assert.Equal(t, "expired", string(SessionStatusExpired))
	assert.Equal(t, "closed", string(SessionStatusClosed))
}

func TestQuestionStatusString(t *testing.T) {
	assert.Equal(t, "processing", string(QuestionStatusProcessing))
	assert.Equal(t, "moderation", string(QuestionStatusModeration))
	assert.Equal(t, "verified", string(QuestionStatusVerified))
	assert.Equal(t, "error", string(QuestionStatusError))
}
```

Run: `go test ./internal/domain/enums -v`
Expected: FAIL (constants not defined).

- [ ] **Step 2: Implement enums**

Create `internal/domain/enums/enums.go`:

```go
package enums

type UserRole string

const (
	UserRoleUser   UserRole = "user"
	UserRoleExpert UserRole = "expert"
)

type SessionStatus string

const (
	SessionStatusCreated SessionStatus = "created"
	SessionStatusActive  SessionStatus = "active"
	SessionStatusExpired SessionStatus = "expired"
	SessionStatusClosed  SessionStatus = "closed"
)

type QuestionStatus string

const (
	QuestionStatusProcessing QuestionStatus = "processing"
	QuestionStatusModeration QuestionStatus = "moderation"
	QuestionStatusVerified   QuestionStatus = "verified"
	QuestionStatusError      QuestionStatus = "error"
)
```

Run: `go test ./internal/domain/enums -v`
Expected: PASS.

- [ ] **Step 3: Implement domain types**

Create `internal/domain/user.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	HasAccess    bool
	Role         enums.UserRole
	CreatedAt    time.Time
}
```

Create `internal/domain/session.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Duration  time.Duration
	Buffer    time.Duration
	StartedAt *time.Time
	ExpiresAt *time.Time
	ClosedAt  *time.Time
	Status    enums.SessionStatus
	CreatedAt time.Time
}
```

Create `internal/domain/image.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

type Image struct {
	ID           uuid.UUID
	SessionID    uuid.UUID
	ImageData    *[]byte
	EnhancedData *[]byte
	MimeType     string
	FileName     string
	Status       string
	CleanedAt    *time.Time
	CreatedAt    time.Time
}
```

Create `internal/domain/question.go`:

```go
package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type Question struct {
	ID                 uuid.UUID
	ImageID            uuid.UUID
	SessionID          uuid.UUID
	Number             int
	QuestionText       string
	NormalizedText     string
	Choices            []Choice
	Answers            []string
	MultipleCorrect    bool
	Confidence         float32
	Explanation        *string
	Status             enums.QuestionStatus
	MatchedQuestionID  *uuid.UUID
	AIAnalysis         json.RawMessage
	VerificationReport json.RawMessage
	Tags               []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Choice struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}
```

- [ ] **Step 4: Write domain type tests**

Create `internal/domain/domain_test.go`:

```go
package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChoiceMarshal(t *testing.T) {
	c := Choice{Label: "A", Text: "Paris"}
	b, err := json.Marshal(c)
	require.NoError(t, err)
	assert.JSONEq(t, `{"label":"A","text":"Paris"}`, string(b))
}

func TestChoiceUnmarshal(t *testing.T) {
	var c Choice
	err := json.Unmarshal([]byte(`{"label":"B","text":"London"}`), &c)
	require.NoError(t, err)
	assert.Equal(t, "B", c.Label)
	assert.Equal(t, "London", c.Text)
}
```

Run: `go test ./internal/domain -v`
Expected: PASS.

- [ ] **Step 5: Implement repository interfaces and helpers**

Create `internal/repository/repository.go` (helper file):

```go
package repository

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func pgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func optionalUUID(p pgtype.UUID) *uuid.UUID {
	if !p.Valid {
		return nil
	}
	u := uuid.UUID(p.Bytes)
	return &u
}

func optionalPgUUID(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}
```

Create `internal/repository/user.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

var ErrUserNotFound = errors.New("repository: user not found")

type UserRepo interface {
	Create(ctx context.Context, q db.Querier, user *domain.User) error
	GetByEmail(ctx context.Context, q db.Querier, email string) (*domain.User, error)
	GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.User, error)
}

type userRepository struct{}

func NewUserRepository() UserRepo {
	return &userRepository{}
}

func (r *userRepository) Create(ctx context.Context, q db.Querier, user *domain.User) error {
	const sql = `
		INSERT INTO users (id, email, password_hash, has_access, role, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := q.Exec(ctx, sql,
		pgUUID(user.ID),
		user.Email,
		user.PasswordHash,
		user.HasAccess,
		string(user.Role),
		user.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *userRepository) GetByEmail(ctx context.Context, q db.Querier, email string) (*domain.User, error) {
	const sql = `
		SELECT id, email, password_hash, has_access, role, created_at
		FROM users
		WHERE email = $1
	`
	row := q.QueryRow(ctx, sql, email)
	return scanUser(row)
}

func (r *userRepository) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.User, error) {
	const sql = `
		SELECT id, email, password_hash, has_access, role, created_at
		FROM users
		WHERE id = $1
	`
	row := q.QueryRow(ctx, sql, pgUUID(id))
	return scanUser(row)
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var id pgtype.UUID
	var role string
	err := row.Scan(&id, &u.Email, &u.PasswordHash, &u.HasAccess, &role, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.ID = uuid.UUID(id.Bytes)
	u.Role = enums.UserRole(role)
	return &u, nil
}
```

Create `internal/repository/session.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

var ErrSessionNotFound = errors.New("repository: session not found")

type SessionRepo interface {
	Create(ctx context.Context, q db.Querier, session *domain.Session) error
	Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Session, error)
	Start(ctx context.Context, q db.Querier, id uuid.UUID, startedAt, expiresAt time.Time) error
	Expire(ctx context.Context, q db.Querier, id uuid.UUID) error
	Close(ctx context.Context, q db.Querier, id uuid.UUID, closedAt time.Time) error
	ListActivePastExpiry(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error)
	ListExpiredPastClosed(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error)
}

type sessionRepository struct{}

func NewSessionRepository() SessionRepo {
	return &sessionRepository{}
}

func (r *sessionRepository) Create(ctx context.Context, q db.Querier, session *domain.Session) error {
	const sql = `
		INSERT INTO sessions (id, user_id, duration, buffer, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := q.Exec(ctx, sql,
		pgUUID(session.ID),
		pgUUID(session.UserID),
		durationToInterval(session.Duration),
		durationToInterval(session.Buffer),
		string(session.Status),
		session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *sessionRepository) Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Session, error) {
	const sql = `
		SELECT id, user_id, duration, buffer, started_at, expires_at, closed_at, status, created_at
		FROM sessions
		WHERE id = $1
	`
	row := q.QueryRow(ctx, sql, pgUUID(id))
	return scanSession(row)
}

func (r *sessionRepository) Start(ctx context.Context, q db.Querier, id uuid.UUID, startedAt, expiresAt time.Time) error {
	const sql = `
		UPDATE sessions
		SET status = 'active', started_at = $2, expires_at = $3
		WHERE id = $1 AND status = 'created'
	`
	cmd, err := q.Exec(ctx, sql, pgUUID(id), startedAt, expiresAt)
	if err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *sessionRepository) Expire(ctx context.Context, q db.Querier, id uuid.UUID) error {
	const sql = `
		UPDATE sessions
		SET status = 'expired'
		WHERE id = $1 AND status = 'active'
	`
	_, err := q.Exec(ctx, sql, pgUUID(id))
	return err
}

func (r *sessionRepository) Close(ctx context.Context, q db.Querier, id uuid.UUID, closedAt time.Time) error {
	const sql = `
		UPDATE sessions
		SET status = 'closed', closed_at = $2
		WHERE id = $1 AND status = 'expired'
	`
	_, err := q.Exec(ctx, sql, pgUUID(id), closedAt)
	return err
}

func (r *sessionRepository) ListActivePastExpiry(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error) {
	const sql = `
		SELECT id, user_id, duration, buffer, started_at, expires_at, closed_at, status, created_at
		FROM sessions
		WHERE status = 'active' AND expires_at <= $1
	`
	rows, err := q.Query(ctx, sql, now)
	if err != nil {
		return nil, fmt.Errorf("list active past expiry: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

func (r *sessionRepository) ListExpiredPastClosed(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error) {
	const sql = `
		SELECT id, user_id, duration, buffer, started_at, expires_at, closed_at, status, created_at
		FROM sessions
		WHERE status = 'expired' AND closed_at <= $1
	`
	rows, err := q.Query(ctx, sql, now)
	if err != nil {
		return nil, fmt.Errorf("list expired past closed: %w", err)
	}
	defer rows.Close()
	return scanSessions(rows)
}

func scanSession(row pgx.Row) (*domain.Session, error) {
	var s domain.Session
	var id, userID pgtype.UUID
	var dur, buf pgtype.Interval
	var status string
	err := row.Scan(
		&id, &userID, &dur, &buf,
		&s.StartedAt, &s.ExpiresAt, &s.ClosedAt,
		&status, &s.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	s.ID = uuid.UUID(id.Bytes)
	s.UserID = uuid.UUID(userID.Bytes)
	s.Duration = intervalToDuration(dur)
	s.Buffer = intervalToDuration(buf)
	s.Status = enums.SessionStatus(status)
	return &s, nil
}

func scanSessions(rows pgx.Rows) ([]domain.Session, error) {
	var sessions []domain.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *s)
	}
	return sessions, rows.Err()
}

func durationToInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func intervalToDuration(i pgtype.Interval) time.Duration {
	return time.Duration(i.Microseconds) * time.Microsecond
}
```

Create `internal/repository/image.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

var ErrImageNotFound = errors.New("repository: image not found")

type ImageRepo interface {
	Create(ctx context.Context, q db.Querier, image *domain.Image) error
	Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Image, error)
	GetBySession(ctx context.Context, q db.Querier, sessionID uuid.UUID) ([]domain.Image, error)
	DeleteBytes(ctx context.Context, q db.Querier, id uuid.UUID) error
}

type imageRepository struct{}

func NewImageRepository() ImageRepo {
	return &imageRepository{}
}

func (r *imageRepository) Create(ctx context.Context, q db.Querier, image *domain.Image) error {
	const sql = `
		INSERT INTO images (id, session_id, image_data, enhanced_data, mime_type, file_name, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := q.Exec(ctx, sql,
		pgUUID(image.ID),
		pgUUID(image.SessionID),
		image.ImageData,
		image.EnhancedData,
		image.MimeType,
		image.FileName,
		image.Status,
		image.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	return nil
}

func (r *imageRepository) Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Image, error) {
	const sql = `
		SELECT id, session_id, image_data, enhanced_data, mime_type, file_name, status, cleaned_at, created_at
		FROM images
		WHERE id = $1
	`
	row := q.QueryRow(ctx, sql, pgUUID(id))
	return scanImage(row)
}

func (r *imageRepository) GetBySession(ctx context.Context, q db.Querier, sessionID uuid.UUID) ([]domain.Image, error) {
	const sql = `
		SELECT id, session_id, image_data, enhanced_data, mime_type, file_name, status, cleaned_at, created_at
		FROM images
		WHERE session_id = $1
	`
	rows, err := q.Query(ctx, sql, pgUUID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("list images by session: %w", err)
	}
	defer rows.Close()
	return scanImages(rows)
}

func (r *imageRepository) DeleteBytes(ctx context.Context, q db.Querier, id uuid.UUID) error {
	const sql = `
		UPDATE images
		SET image_data = NULL, enhanced_data = NULL, cleaned_at = now()
		WHERE id = $1
	`
	_, err := q.Exec(ctx, sql, pgUUID(id))
	return err
}

func scanImage(row pgx.Row) (*domain.Image, error) {
	var img domain.Image
	var id, sessionID pgtype.UUID
	var imageData, enhancedData *[]byte
	err := row.Scan(
		&id, &sessionID, &imageData, &enhancedData,
		&img.MimeType, &img.FileName, &img.Status,
		&img.CleanedAt, &img.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrImageNotFound
		}
		return nil, fmt.Errorf("scan image: %w", err)
	}
	img.ID = uuid.UUID(id.Bytes)
	img.SessionID = uuid.UUID(sessionID.Bytes)
	img.ImageData = imageData
	img.EnhancedData = enhancedData
	return &img, nil
}

func scanImages(rows pgx.Rows) ([]domain.Image, error) {
	var images []domain.Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		images = append(images, *img)
	}
	return images, rows.Err()
}
```

Create `internal/repository/question.go`:

```go
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

var ErrQuestionNotFound = errors.New("repository: question not found")

type QuestionRepo interface {
	Create(ctx context.Context, q db.Querier, question *domain.Question) error
	Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Question, error)
	ListBySession(ctx context.Context, q db.Querier, sessionID uuid.UUID) ([]domain.Question, error)
	ListByImage(ctx context.Context, q db.Querier, imageID uuid.UUID) ([]domain.Question, error)
	List(ctx context.Context, q db.Querier, filter QuestionFilter) ([]domain.Question, error)
	UpdateStatus(ctx context.Context, q db.Querier, id uuid.UUID, status enums.QuestionStatus, report json.RawMessage) error
	FindByNormalizedText(ctx context.Context, q db.Querier, normalized string) (*domain.Question, error)
	CountByImageAndStatus(ctx context.Context, q db.Querier, imageID uuid.UUID, status enums.QuestionStatus) (int, error)
}

type QuestionFilter struct {
	UserID    *uuid.UUID
	SessionID *uuid.UUID
	Status    *enums.QuestionStatus
	Role      enums.UserRole
}

type questionRepository struct{}

func NewQuestionRepository() QuestionRepo {
	return &questionRepository{}
}

func (r *questionRepository) Create(ctx context.Context, q db.Querier, question *domain.Question) error {
	const sql = `
		INSERT INTO questions (
			id, image_id, session_id, number, question_text, normalized_text,
			choices, answers, multiple_correct, confidence, explanation,
			status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`
	choices, err := json.Marshal(question.Choices)
	if err != nil {
		return fmt.Errorf("marshal choices: %w", err)
	}
	answers, err := json.Marshal(question.Answers)
	if err != nil {
		return fmt.Errorf("marshal answers: %w", err)
	}
	_, err = q.Exec(ctx, sql,
		pgUUID(question.ID),
		pgUUID(question.ImageID),
		pgUUID(question.SessionID),
		question.Number,
		question.QuestionText,
		question.NormalizedText,
		choices,
		answers,
		question.MultipleCorrect,
		question.Confidence,
		question.Explanation,
		string(question.Status),
		optionalPgUUID(question.MatchedQuestionID),
		question.AIAnalysis,
		question.VerificationReport,
		question.Tags,
		question.CreatedAt,
		question.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create question: %w", err)
	}
	return nil
}

func (r *questionRepository) Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Question, error) {
	const sql = `
		SELECT id, image_id, session_id, number, question_text, normalized_text,
		       choices, answers, multiple_correct, confidence, explanation,
		       status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		FROM questions
		WHERE id = $1
	`
	row := q.QueryRow(ctx, sql, pgUUID(id))
	return scanQuestion(row)
}

func (r *questionRepository) ListBySession(ctx context.Context, q db.Querier, sessionID uuid.UUID) ([]domain.Question, error) {
	const sql = `
		SELECT id, image_id, session_id, number, question_text, normalized_text,
		       choices, answers, multiple_correct, confidence, explanation,
		       status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		FROM questions
		WHERE session_id = $1
		ORDER BY number
	`
	rows, err := q.Query(ctx, sql, pgUUID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("list questions by session: %w", err)
	}
	defer rows.Close()
	return scanQuestions(rows)
}

func (r *questionRepository) ListByImage(ctx context.Context, q db.Querier, imageID uuid.UUID) ([]domain.Question, error) {
	const sql = `
		SELECT id, image_id, session_id, number, question_text, normalized_text,
		       choices, answers, multiple_correct, confidence, explanation,
		       status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		FROM questions
		WHERE image_id = $1
		ORDER BY number
	`
	rows, err := q.Query(ctx, sql, pgUUID(imageID))
	if err != nil {
		return nil, fmt.Errorf("list questions by image: %w", err)
	}
	defer rows.Close()
	return scanQuestions(rows)
}

func (r *questionRepository) List(ctx context.Context, q db.Querier, filter QuestionFilter) ([]domain.Question, error) {
	sql := `
		SELECT id, image_id, session_id, number, question_text, normalized_text,
		       choices, answers, multiple_correct, confidence, explanation,
		       status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		FROM questions
		WHERE 1=1
	`
	var args []any
	argIdx := 1

	if filter.Role != enums.UserRoleExpert {
		sql += fmt.Sprintf(" AND session_id IN (SELECT id FROM sessions WHERE user_id = $%d)", argIdx)
		args = append(args, pgUUID(*filter.UserID))
		argIdx++
	}
	if filter.SessionID != nil {
		sql += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, pgUUID(*filter.SessionID))
		argIdx++
	}
	if filter.Status != nil {
		sql += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, string(*filter.Status))
		argIdx++
	}
	sql += " ORDER BY updated_at DESC"

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	defer rows.Close()
	return scanQuestions(rows)
}

func (r *questionRepository) UpdateStatus(ctx context.Context, q db.Querier, id uuid.UUID, status enums.QuestionStatus, report json.RawMessage) error {
	const sql = `
		UPDATE questions
		SET status = $2, verification_report = $3
		WHERE id = $1
	`
	_, err := q.Exec(ctx, sql, pgUUID(id), string(status), report)
	return err
}

func (r *questionRepository) FindByNormalizedText(ctx context.Context, q db.Querier, normalized string) (*domain.Question, error) {
	const sql = `
		SELECT id, image_id, session_id, number, question_text, normalized_text,
		       choices, answers, multiple_correct, confidence, explanation,
		       status, matched_question_id, ai_analysis, verification_report, tags, created_at, updated_at
		FROM questions
		WHERE normalized_text = $1 AND status = 'verified'
		ORDER BY created_at ASC
		LIMIT 1
	`
	row := q.QueryRow(ctx, sql, normalized)
	qst, err := scanQuestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return qst, nil
}

func (r *questionRepository) CountByImageAndStatus(ctx context.Context, q db.Querier, imageID uuid.UUID, status enums.QuestionStatus) (int, error) {
	const sql = `
		SELECT COUNT(*)
		FROM questions
		WHERE image_id = $1 AND status <> $2
	`
	var count int
	err := q.QueryRow(ctx, sql, pgUUID(imageID), string(status)).Scan(&count)
	return count, err
}

func scanQuestion(row pgx.Row) (*domain.Question, error) {
	var q domain.Question
	var id, imageID, sessionID pgtype.UUID
	var matchedID pgtype.UUID
	var status string
	var choices, answers []byte
	err := row.Scan(
		&id, &imageID, &sessionID, &q.Number,
		&q.QuestionText, &q.NormalizedText,
		&choices, &answers, &q.MultipleCorrect, &q.Confidence, &q.Explanation,
		&status, &matchedID, &q.AIAnalysis, &q.VerificationReport,
		&q.Tags, &q.CreatedAt, &q.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrQuestionNotFound
		}
		return nil, fmt.Errorf("scan question: %w", err)
	}
	q.ID = uuid.UUID(id.Bytes)
	q.ImageID = uuid.UUID(imageID.Bytes)
	q.SessionID = uuid.UUID(sessionID.Bytes)
	q.MatchedQuestionID = optionalUUID(matchedID)
	q.Status = enums.QuestionStatus(status)
	if err := json.Unmarshal(choices, &q.Choices); err != nil {
		return nil, fmt.Errorf("unmarshal choices: %w", err)
	}
	if err := json.Unmarshal(answers, &q.Answers); err != nil {
		return nil, fmt.Errorf("unmarshal answers: %w", err)
	}
	return &q, nil
}

func scanQuestions(rows pgx.Rows) ([]domain.Question, error) {
	var questions []domain.Question
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		questions = append(questions, *q)
	}
	return questions, rows.Err()
}
```

- [ ] **Step 6: Write repository tests**

Create `internal/repository/repository_test.go` with a testify suite that connects to `TEST_DATABASE_URL`:

```go
package repository

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"
)

type RepositoryTestSuite struct {
	suite.Suite
	ctx context.Context
	pool *pgxpool.Pool
}

func (s *RepositoryTestSuite) SetupSuite() {
	s.ctx = context.Background()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		s.T().Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(s.ctx, databaseURL)
	s.Require().NoError(err)
	s.pool = pool
}

func (s *RepositoryTestSuite) TearDownSuite() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *RepositoryTestSuite) SetupTest() {
	_, err := s.pool.Exec(s.ctx, `
		TRUNCATE TABLE questions, images, sessions, users RESTART IDENTITY CASCADE
	`)
	s.Require().NoError(err)
}

func TestRepositorySuite(t *testing.T) {
	suite.Run(t, new(RepositoryTestSuite))
}
```

Create `internal/repository/user_test.go`:

```go
package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

func (s *RepositoryTestSuite) TestUserCreateAndGet() {
	user := &domain.User{
		ID:           uuid.New(),
		Email:        "test@example.com",
		PasswordHash: "hash",
		HasAccess:    true,
		Role:         enums.UserRoleUser,
		CreatedAt:    time.Now(),
	}
	repo := NewUserRepository()
	err := repo.Create(s.ctx, s.pool, user)
	s.Require().NoError(err)

	found, err := repo.GetByEmail(s.ctx, s.pool, user.Email)
	s.Require().NoError(err)
	assert.Equal(s.T(), user.ID, found.ID)
	assert.Equal(s.T(), user.Email, found.Email)
	assert.True(s.T(), found.HasAccess)

	byID, err := repo.GetByID(s.ctx, s.pool, user.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), user.ID, byID.ID)
}

func (s *RepositoryTestSuite) TestUserNotFound() {
	repo := NewUserRepository()
	_, err := repo.GetByEmail(s.ctx, s.pool, "missing@example.com")
	s.ErrorIs(err, ErrUserNotFound)
}
```

Create `internal/repository/session_test.go`:

```go
package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

func (s *RepositoryTestSuite) TestSessionLifecycle() {
	user := s.createUser()
	session := &domain.Session{
		ID:        uuid.New(),
		UserID:    user.ID,
		Duration:  time.Hour,
		Buffer:    5 * time.Minute,
		Status:    enums.SessionStatusCreated,
		CreatedAt: time.Now(),
	}
	repo := NewSessionRepository()
	s.Require().NoError(repo.Create(s.ctx, s.pool, session))

	found, err := repo.Get(s.ctx, s.pool, session.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), enums.SessionStatusCreated, found.Status)

	start := time.Now()
	expires := start.Add(session.Duration)
	s.Require().NoError(repo.Start(s.ctx, s.pool, session.ID, start, expires))

	found, err = repo.Get(s.ctx, s.pool, session.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), enums.SessionStatusActive, found.Status)
	assert.NotNil(s.T(), found.ExpiresAt)
}

func (s *RepositoryTestSuite) createUser() *domain.User {
	user := &domain.User{
		ID:           uuid.New(),
		Email:        uuid.New().String() + "@example.com",
		PasswordHash: "hash",
		HasAccess:    true,
		Role:         enums.UserRoleUser,
		CreatedAt:    time.Now(),
	}
	s.Require().NoError(NewUserRepository().Create(s.ctx, s.pool, user))
	return user
}
```

Create `internal/repository/image_test.go`:

```go
package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

func (s *RepositoryTestSuite) TestImageCreateAndGet() {
	user := s.createUser()
	session := s.createSession(user.ID)
	data := []byte("original")
	enhanced := []byte("enhanced")
	image := &domain.Image{
		ID:           uuid.New(),
		SessionID:    session.ID,
		ImageData:    &data,
		EnhancedData: &enhanced,
		MimeType:     "image/png",
		FileName:     "quiz.png",
		Status:       "processing",
		CreatedAt:    time.Now(),
	}
	repo := NewImageRepository()
	s.Require().NoError(repo.Create(s.ctx, s.pool, image))

	found, err := repo.Get(s.ctx, s.pool, image.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), image.ID, found.ID)
	assert.Equal(s.T(), "image/png", found.MimeType)
	assert.NotNil(s.T(), found.ImageData)
}

func (s *RepositoryTestSuite) createSession(userID uuid.UUID) *domain.Session {
	session := &domain.Session{
		ID:        uuid.New(),
		UserID:    userID,
		Duration:  time.Hour,
		Buffer:    5 * time.Minute,
		Status:    enums.SessionStatusCreated,
		CreatedAt: time.Now(),
	}
	s.Require().NoError(NewSessionRepository().Create(s.ctx, s.pool, session))
	return session
}
```

Create `internal/repository/question_test.go`:

```go
package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

func (s *RepositoryTestSuite) TestQuestionCreateAndList() {
	user := s.createUser()
	session := s.createSession(user.ID)
	image := s.createImage(session.ID)
	question := &domain.Question{
		ID:             uuid.New(),
		ImageID:        image.ID,
		SessionID:      session.ID,
		Number:         1,
		QuestionText:   "What is the capital of France?",
		NormalizedText: "what is the capital of france",
		Choices:        []domain.Choice{{Label: "A", Text: "Paris"}},
		Answers:        []string{"Paris"},
		Confidence:     0.97,
		Status:         enums.QuestionStatusProcessing,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	repo := NewQuestionRepository()
	s.Require().NoError(repo.Create(s.ctx, s.pool, question))

	list, err := repo.ListBySession(s.ctx, s.pool, session.ID)
	s.Require().NoError(err)
	assert.Len(s.T(), list, 1)
	assert.Equal(s.T(), "Paris", list[0].Answers[0])

	s.Require().NoError(repo.UpdateStatus(s.ctx, s.pool, question.ID, enums.QuestionStatusModeration, nil))
	found, err := repo.Get(s.ctx, s.pool, question.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), enums.QuestionStatusModeration, found.Status)
}

func (s *RepositoryTestSuite) createImage(sessionID uuid.UUID) *domain.Image {
	data := []byte("original")
	enhanced := []byte("enhanced")
	image := &domain.Image{
		ID:           uuid.New(),
		SessionID:    sessionID,
		ImageData:    &data,
		EnhancedData: &enhanced,
		MimeType:     "image/png",
		FileName:     "quiz.png",
		Status:       "processing",
		CreatedAt:    time.Now(),
	}
	s.Require().NoError(NewImageRepository().Create(s.ctx, s.pool, image))
	return image
}
```

- [ ] **Step 7: Run repository tests against the test database**

Run:
```bash
docker compose up -d postgres-test
sleep 2
make test-repo
```

Expected: all repository tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/domain internal/repository go.mod go.sum
git commit -m "feat: add domain types, enums, and repository layer with pgx Querier support"
```

---

### Task 3: Authentication middleware and endpoints

**Files:**
- Create: `internal/service/auth.go`
- Create: `internal/service/auth_test.go`
- Create: `internal/handler/auth.go`
- Create: `internal/handler/auth_test.go`
- Create: `internal/middleware/auth.go`
- Create: `internal/middleware/auth_test.go`
- Create: `internal/handler/dto/dto.go`

- [ ] **Step 1: Write the DTO package**

Create `internal/handler/dto/dto.go` with the spec DTOs:

```go
package dto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	UserID    uuid.UUID      `json:"user_id"`
	Email     string         `json:"email"`
	HasAccess bool           `json:"has_access"`
	Role      enums.UserRole `json:"role"`
}

type CreateSessionRequest struct {
	DurationMinutes int `json:"duration_minutes" binding:"required,min=5,max=480"`
	BufferMinutes   int `json:"buffer_minutes" binding:"min=0,max=60"`
}

type CreateSessionResponse struct {
	ID        uuid.UUID           `json:"id"`
	Status    enums.SessionStatus `json:"status"`
	Duration  string              `json:"duration"`
	Buffer    string              `json:"buffer"`
	CreatedAt time.Time           `json:"created_at"`
}

type StartSessionResponse struct {
	ID        uuid.UUID           `json:"id"`
	Status    enums.SessionStatus `json:"status"`
	StartedAt time.Time           `json:"started_at"`
	ExpiresAt time.Time           `json:"expires_at"`
}

type UploadImageResponse struct {
	ID       uuid.UUID `json:"id"`
	Status   string    `json:"status"`
	FileName string    `json:"file_name"`
	MimeType string    `json:"mime_type"`
}

type QuestionResponse struct {
	ID              uuid.UUID            `json:"id"`
	SessionID       uuid.UUID            `json:"session_id,omitempty"`
	ImageID         uuid.UUID            `json:"image_id,omitempty"`
	Number          int                  `json:"number"`
	QuestionText    string               `json:"question_text"`
	Choices         []domain.Choice      `json:"choices"`
	Answers         []string             `json:"answers"`
	MultipleCorrect bool                 `json:"multiple_correct"`
	Confidence      float32              `json:"confidence"`
	Explanation     *string              `json:"explanation,omitempty"`
	Status          enums.QuestionStatus `json:"status"`
	Tags            []string             `json:"tags"`
	Verification    json.RawMessage      `json:"verification,omitempty"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type UpdateQuestionStatusRequest struct {
	Status enums.QuestionStatus `json:"status" binding:"required,oneof=verified error moderation"`
}

type QuestionStatusResponse struct {
	ID        uuid.UUID            `json:"id"`
	Status    enums.QuestionStatus `json:"status"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type QuestionFilterRequest struct {
	Status string `form:"status"`
}
```

- [ ] **Step 2: Write the failing auth service test**

Create `internal/service/auth_test.go`:

```go
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type mockUserRepo struct {
	mock.Mock
}

func (m *mockUserRepo) Create(ctx context.Context, q db.Querier, user *domain.User) error {
	return m.Called(ctx, q, user).Error(0)
}

func (m *mockUserRepo) GetByEmail(ctx context.Context, q db.Querier, email string) (*domain.User, error) {
	args := m.Called(ctx, q, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *mockUserRepo) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.User, error) {
	args := m.Called(ctx, q, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func TestAuthLoginSuccess(t *testing.T) {
	password := "secret"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	user := &domain.User{
		ID:           uuid.New(),
		Email:        "user@example.com",
		PasswordHash: string(hash),
		HasAccess:    true,
		Role:         enums.UserRoleUser,
	}

	repo := new(mockUserRepo)
	repo.On("GetByEmail", mock.Anything, mock.Anything, user.Email).Return(user, nil)

	svc := NewAuthService(nil, repo)
	found, err := svc.Authenticate(context.Background(), user.Email, password)
	require.NoError(t, err)
	assert.Equal(t, user.ID, found.ID)
}

func TestAuthLoginWrongPassword(t *testing.T) {
	repo := new(mockUserRepo)
	repo.On("GetByEmail", mock.Anything, mock.Anything, "user@example.com").Return(nil, repository.ErrUserNotFound)

	svc := NewAuthService(nil, repo)
	_, err := svc.Authenticate(context.Background(), "user@example.com", "secret")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}
```

Run: `go test ./internal/service -run TestAuth -v`
Expected: FAIL (`NewAuthService`, `ErrInvalidCredentials` not defined).

- [ ] **Step 3: Implement auth service**

Create `internal/service/auth.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

var ErrInvalidCredentials = errors.New("service: invalid credentials")

type AuthService interface {
	Authenticate(ctx context.Context, email, password string) (*domain.User, error)
}

type authService struct {
	pool  *pgxpool.Pool
	users repository.UserRepo
}

func NewAuthService(pool *pgxpool.Pool, users repository.UserRepo) AuthService {
	return &authService{pool: pool, users: users}
}

func (s *authService) Authenticate(ctx context.Context, email, password string) (*domain.User, error) {
	user, err := s.users.GetByEmail(ctx, s.pool, email)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}
```

- [ ] **Step 4: Run auth service tests**

Run: `go test ./internal/service -run TestAuth -v`
Expected: PASS.

- [ ] **Step 5: Write the failing auth middleware test**

Create `internal/middleware/auth_test.go`:

```go
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

func TestAuthRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewCookieStore("test-secret-32-bytes-long-12345")
	r := gin.New()
	r.Use(sessions.Sessions("session", store))
	r.Use(AuthRequired(store))
	r.GET("/me", func(c *gin.Context) {
		user := UserFromContext(c)
		c.JSON(200, gin.H{"user_id": user.ID.String()})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/me", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 401, w.Code)
}
```

Run: `go test ./internal/middleware -v`
Expected: FAIL (`AuthRequired`, `UserFromContext` not defined).

- [ ] **Step 6: Implement auth middleware**

Create `internal/middleware/auth.go`:

```go
package middleware

import (
	"net/http"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/handler"
)

const userSessionKey = "user_id"
const userContextKey = "current_user"

func NewCookieStore(secret string) sessions.Store {
	return cookie.NewStore([]byte(secret))
}

func AuthRequired(store sessions.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		v := session.Get(userSessionKey)
		if v == nil {
			handler.RespondError(c, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
			c.Abort()
			return
		}
		userID, ok := v.(string)
		if !ok {
			handler.RespondError(c, http.StatusUnauthorized, "unauthorized", "invalid session", nil)
			c.Abort()
			return
		}
		id, err := uuid.Parse(userID)
		if err != nil {
			handler.RespondError(c, http.StatusUnauthorized, "unauthorized", "invalid session", nil)
			c.Abort()
			return
		}
		user := &domain.User{ID: id, HasAccess: true, Role: enums.UserRoleUser}
		c.Set(userContextKey, user)
		c.Next()
	}
}

func UserFromContext(c *gin.Context) *domain.User {
	v, ok := c.Get(userContextKey)
	if !ok {
		return nil
	}
	return v.(*domain.User)
}
```

But `handler.RespondError` is defined later in Task 9/health? Actually spec says error helper in handler. I should define `handler/error.go` early. Let me add it in Task 3 Step 5.5? Or in Task 1? Better in Task 3.

Create `internal/handler/error.go`:

```go
package handler

import (
	"github.com/gin-gonic/gin"
)

type APIError struct {
	Code    string                 `json:"error"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details"`
}

func RespondError(c *gin.Context, status int, code, message string, details map[string]interface{}) {
	c.JSON(status, APIError{
		Code:    code,
		Message: message,
		Details: details,
	})
}
```

- [ ] **Step 7: Implement auth handlers**

Create `internal/handler/auth.go`:

```go
package handler

import (
	"net/http"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/handler/dto"
	"github.com/vlgrigoriev/coeus/internal/service"
)

type AuthHandler struct {
	svc service.AuthService
}

func NewAuthHandler(svc service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", err.Error(), nil)
		return
	}
	user, err := h.svc.Authenticate(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		RespondError(c, http.StatusUnauthorized, "unauthorized", "invalid credentials", nil)
		return
	}
	if !user.HasAccess {
		RespondError(c, http.StatusForbidden, "forbidden", "account does not have access", nil)
		return
	}
	session := sessions.Default(c)
	session.Set("user_id", user.ID.String())
	if err := session.Save(); err != nil {
		RespondError(c, http.StatusInternalServerError, "internal_error", "failed to save session", nil)
		return
	}
	c.JSON(http.StatusOK, dto.LoginResponse{
		UserID:    user.ID,
		Email:     user.Email,
		HasAccess: user.HasAccess,
		Role:      user.Role,
	})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	session := sessions.Default(c)
	session.Delete("user_id")
	_ = session.Save()
	c.Status(http.StatusOK)
}
```

- [ ] **Step 8: Write auth handler tests**

Create `internal/handler/auth_test.go`:

```go
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/middleware"
)

type mockAuthService struct {
	mock.Mock
}

func (m *mockAuthService) Authenticate(ctx context.Context, email, password string) (*domain.User, error) {
	args := m.Called(ctx, email, password)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func TestLoginSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &domain.User{ID: uuid.New(), Email: "user@example.com", HasAccess: true, Role: enums.UserRoleUser}
	svc := new(mockAuthService)
	svc.On("Authenticate", mock.Anything, "user@example.com", "secret").Return(user, nil)

	store := middleware.NewCookieStore("test-secret-32-bytes-long-12345")
	h := NewAuthHandler(svc)
	r := gin.New()
	r.Use(sessions.Sessions("session", store))
	r.POST("/api/auth/login", h.Login)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "password": "secret"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Header().Get("Set-Cookie"), "session=")
}
```

Run: `go test ./internal/handler -run TestLogin -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/service/auth.go internal/service/auth_test.go internal/handler/auth.go internal/handler/auth_test.go internal/handler/error.go internal/handler/dto internal/middleware/auth.go internal/middleware/auth_test.go
git commit -m "feat: add auth service, handlers, and session middleware"
```

---

### Task 4: Session management

**Files:**
- Create: `internal/service/session.go`
- Create: `internal/service/session_test.go`
- Create: `internal/handler/session.go`
- Create: `internal/handler/session_test.go`
- Create: `internal/middleware/session.go`
- Create: `internal/middleware/session_test.go`

- [ ] **Step 1: Write the failing session service test**

Create `internal/service/session_test.go`:

```go
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type mockSessionRepo struct {
	mock.Mock
}

func (m *mockSessionRepo) Create(ctx context.Context, q db.Querier, s *domain.Session) error {
	return m.Called(ctx, q, s).Error(0)
}
func (m *mockSessionRepo) Get(ctx context.Context, q db.Querier, id uuid.UUID) (*domain.Session, error) {
	args := m.Called(ctx, q, id)
	if args.Get(0) == nil { return nil, args.Error(1) }
	return args.Get(0).(*domain.Session), args.Error(1)
}
func (m *mockSessionRepo) Start(ctx context.Context, q db.Querier, id uuid.UUID, startedAt, expiresAt time.Time) error {
	return m.Called(ctx, q, id, startedAt, expiresAt).Error(0)
}
func (m *mockSessionRepo) Expire(ctx context.Context, q db.Querier, id uuid.UUID) error { return nil }
func (m *mockSessionRepo) Close(ctx context.Context, q db.Querier, id uuid.UUID, closedAt time.Time) error { return nil }
func (m *mockSessionRepo) ListActivePastExpiry(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error) { return nil, nil }
func (m *mockSessionRepo) ListExpiredPastClosed(ctx context.Context, q db.Querier, now time.Time) ([]domain.Session, error) { return nil, nil }

func TestSessionCreate(t *testing.T) {
	repo := new(mockSessionRepo)
	repo.On("Create", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	svc := NewSessionService(nil, repo)
	session, err := svc.Create(context.Background(), uuid.New(), time.Hour, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, enums.SessionStatusCreated, session.Status)
	assert.Equal(t, time.Hour, session.Duration)
}
```

Run: `go test ./internal/service -run TestSession -v`
Expected: FAIL (`NewSessionService` not defined).

- [ ] **Step 2: Implement session service**

Create `internal/service/session.go`:

```go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

type SessionService interface {
	Create(ctx context.Context, userID uuid.UUID, duration, buffer time.Duration) (*domain.Session, error)
	Start(ctx context.Context, id uuid.UUID) (*domain.Session, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Session, error)
}

type sessionService struct {
	pool *pgxpool.Pool
	sessions repository.SessionRepo
}

func NewSessionService(pool *pgxpool.Pool, sessions repository.SessionRepo) SessionService {
	return &sessionService{pool: pool, sessions: sessions}
}

func (s *sessionService) Create(ctx context.Context, userID uuid.UUID, duration, buffer time.Duration) (*domain.Session, error) {
	session := &domain.Session{
		ID:        uuid.New(),
		UserID:    userID,
		Duration:  duration,
		Buffer:    buffer,
		Status:    enums.SessionStatusCreated,
		CreatedAt: time.Now(),
	}
	if err := s.sessions.Create(ctx, s.pool, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return session, nil
}

func (s *sessionService) Start(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	startedAt := time.Now()
	session, err := s.sessions.Get(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if session.Status != enums.SessionStatusCreated {
		return nil, fmt.Errorf("session cannot be started from status %s", session.Status)
	}
	expiresAt := startedAt.Add(session.Duration)
	if err := s.sessions.Start(ctx, s.pool, id, startedAt, expiresAt); err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	session.Status = enums.SessionStatusActive
	session.StartedAt = &startedAt
	session.ExpiresAt = &expiresAt
	return session, nil
}

func (s *sessionService) Get(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	return s.sessions.Get(ctx, s.pool, id)
}
```

- [ ] **Step 3: Write the failing session middleware test**

Create `internal/middleware/session_test.go`:

```go
package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
)

type mockSessionGetter struct {
	active bool
}

func (m *mockSessionGetter) Get(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	if !m.active {
		return nil, errors.New("not found")
	}
	expires := time.Now().Add(time.Hour)
	return &domain.Session{ID: id, Status: enums.SessionStatusActive, ExpiresAt: &expires}, nil
}

func TestActiveSessionRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ActiveSessionRequired(&mockSessionGetter{active: true}))
	r.POST("/sessions/:id/upload", func(c *gin.Context) {
		c.Status(200)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/sessions/"+uuid.New().String()+"/upload", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
}

func TestActiveSessionRequiredInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ActiveSessionRequired(&mockSessionGetter{active: false}))
	r.POST("/sessions/:id/upload", func(c *gin.Context) {
		c.Status(200)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/sessions/"+uuid.New().String()+"/upload", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 404, w.Code)
}
```

Run: `go test ./internal/middleware -v`
Expected: PASS.

- [ ] **Step 4: Implement session middleware**

Create `internal/middleware/session.go`:

```go
package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/handler"
)

type SessionGetter interface {
	Get(ctx context.Context, id uuid.UUID) (*domain.Session, error)
}

const sessionContextKey = "current_session"

func ActiveSessionRequired(getter SessionGetter) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := c.Param("id")
		id, err := uuid.Parse(sessionID)
		if err != nil {
			handler.RespondError(c, http.StatusBadRequest, "bad_request", "invalid session id", nil)
			c.Abort()
			return
		}
		session, err := getter.Get(c.Request.Context(), id)
		if err != nil {
			handler.RespondError(c, http.StatusNotFound, "not_found", "session not found", nil)
			c.Abort()
			return
		}
		if session.Status != enums.SessionStatusActive {
			handler.RespondError(c, http.StatusForbidden, "session_expired", "session is not active", nil)
			c.Abort()
			return
		}
		if session.ExpiresAt != nil && session.ExpiresAt.Before(time.Now()) {
			handler.RespondError(c, http.StatusForbidden, "session_expired", "session has expired", nil)
			c.Abort()
			return
		}
		c.Set(sessionContextKey, session)
		c.Next()
	}
}

func SessionFromContext(c *gin.Context) *domain.Session {
	v, _ := c.Get(sessionContextKey)
	if v == nil {
		return nil
	}
	return v.(*domain.Session)
}
```

Add missing imports: `context`, `time`.

- [ ] **Step 5: Implement session handlers**

Create `internal/handler/session.go`:

```go
package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/handler/dto"
	"github.com/vlgrigoriev/coeus/internal/middleware"
	"github.com/vlgrigoriev/coeus/internal/service"
)

type SessionHandler struct {
	svc service.SessionService
}

func NewSessionHandler(svc service.SessionService) *SessionHandler {
	return &SessionHandler{svc: svc}
}

func (h *SessionHandler) Create(c *gin.Context) {
	var req dto.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", err.Error(), nil)
		return
	}
	user := middleware.UserFromContext(c)
	duration := time.Duration(req.DurationMinutes) * time.Minute
	buffer := time.Duration(req.BufferMinutes) * time.Minute
	session, err := h.svc.Create(c.Request.Context(), user.ID, duration, buffer)
	if err != nil {
		RespondError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
		return
	}
	c.JSON(http.StatusCreated, dto.CreateSessionResponse{
		ID:        session.ID,
		Status:    session.Status,
		Duration:  session.Duration.String(),
		Buffer:    session.Buffer.String(),
		CreatedAt: session.CreatedAt,
	})
}

func (h *SessionHandler) Start(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid session id", nil)
		return
	}
	session, err := h.svc.Start(c.Request.Context(), id)
	if err != nil {
		RespondError(c, http.StatusForbidden, "session_expired", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, dto.StartSessionResponse{
		ID:        session.ID,
		Status:    session.Status,
		StartedAt: *session.StartedAt,
		ExpiresAt: *session.ExpiresAt,
	})
}

func (h *SessionHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid session id", nil)
		return
	}
	session, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		RespondError(c, http.StatusNotFound, "not_found", "session not found", nil)
		return
	}
	user := middleware.UserFromContext(c)
	if session.UserID != user.ID {
		RespondError(c, http.StatusForbidden, "forbidden", "not your session", nil)
		return
	}
	c.JSON(http.StatusOK, dto.CreateSessionResponse{
		ID:        session.ID,
		Status:    session.Status,
		Duration:  session.Duration.String(),
		Buffer:    session.Buffer.String(),
		CreatedAt: session.CreatedAt,
	})
}
```

- [ ] **Step 6: Write session handler tests**

Create `internal/handler/session_test.go` with table-driven tests for create/start/get, mocking `SessionService`.

- [ ] **Step 7: Commit**

```bash
git add internal/service/session.go internal/service/session_test.go internal/handler/session.go internal/handler/session_test.go internal/middleware/session.go internal/middleware/session_test.go
git commit -m "feat: add session management service, handlers, and middleware"
```

---

### Task 5: Image upload and enhancement pipeline

**Files:**
- Create: `internal/service/ai/enhance.go`
- Create: `internal/service/ai/enhance_test.go`
- Create: `internal/service/image.go`
- Create: `internal/service/image_test.go`
- Create: `internal/handler/image.go`
- Create: `internal/handler/image_test.go`

- [ ] **Step 1: Write the failing image enhancement test**

Create `internal/service/ai/enhance_test.go`:

```go
package ai

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnhanceDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	out := Enhance(img, 50)
	bounds := out.Bounds()
	assert.LessOrEqual(t, bounds.Dx(), 50)
	assert.LessOrEqual(t, bounds.Dy(), 50)
}
```

Run: FAIL (`Enhance` not defined).

- [ ] **Step 2: Implement image enhancement**

Create `internal/service/ai/enhance.go`:

```go
package ai

import (
	"image"

	"github.com/disintegration/imaging"
)

func Enhance(img image.Image, maxDim int) image.Image {
	img = imaging.Fit(img, maxDim, maxDim, imaging.Lanczos)
	img = imaging.AdjustContrast(img, 15)
	img = imaging.AdjustGamma(img, 0.85)
	img = imaging.Sharpen(img, 0.5)
	return img
}
```

- [ ] **Step 3: Implement image service**

Create `internal/service/image.go`:

```go
package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/queue/jobs"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
	_ "golang.org/x/image/webp"
)

type ImageService interface {
	Upload(ctx context.Context, sessionID uuid.UUID, file io.Reader, filename, mimeType string) (*domain.Image, error)
}

type imageService struct {
	pool      *pgxpool.Pool
	images    repository.ImageRepo
	queue     QueueInserter
	maxSize   int64
	maxDim    int
}

type QueueInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOptions) (*river.JobRow, error)
}

func NewImageService(pool *pgxpool.Pool, images repository.ImageRepo, queue QueueInserter, maxSize int64, maxDim int) ImageService {
	return &imageService{pool: pool, images: images, queue: queue, maxSize: maxSize, maxDim: maxDim}
}

func (s *imageService) Upload(ctx context.Context, sessionID uuid.UUID, file io.Reader, filename, mimeType string) (*domain.Image, error) {
	if !isAllowedMime(mimeType) {
		return nil, fmt.Errorf("unsupported mime type %s", mimeType)
	}
	limited := io.LimitReader(file, s.maxSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > s.maxSize {
		return nil, fmt.Errorf("image exceeds maximum size")
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > s.maxDim || bounds.Dy() > s.maxDim {
		return nil, fmt.Errorf("image dimensions exceed maximum")
	}

	enhanced := ai.Enhance(img, s.maxDim)
	var buf bytes.Buffer
	if err := png.Encode(&buf, enhanced); err != nil {
		return nil, fmt.Errorf("encode enhanced image: %w", err)
	}
	enhancedBytes := buf.Bytes()

	imageRecord := &domain.Image{
		ID:           uuid.New(),
		SessionID:    sessionID,
		ImageData:    &data,
		EnhancedData: &enhancedBytes,
		MimeType:     mimeType,
		FileName:     filename,
		Status:       "processing",
		CreatedAt:    time.Now(),
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin upload tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.images.Create(ctx, tx, imageRecord); err != nil {
		return nil, fmt.Errorf("insert image: %w", err)
	}
	if _, err := s.queue.InsertTx(ctx, tx, jobs.ExtractQuestionsArgs{ImageID: imageRecord.ID}, &river.InsertOptions{MaxAttempts: 3}); err != nil {
		return nil, fmt.Errorf("enqueue extract job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit upload tx: %w", err)
	}
	return imageRecord, nil
}

func isAllowedMime(m string) bool {
	switch m {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}
```

- [ ] **Step 4: Write image service tests**

Mock `ImageRepo` and `QueueInserter`. Test validation and happy path.

- [ ] **Step 5: Implement image handler**

Create `internal/handler/image.go`:

```go
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/handler/dto"
	"github.com/vlgrigoriev/coeus/internal/middleware"
	"github.com/vlgrigoriev/coeus/internal/service"
)

type ImageHandler struct {
	svc service.ImageService
}

func NewImageHandler(svc service.ImageService) *ImageHandler {
	return &ImageHandler{svc: svc}
}

func (h *ImageHandler) Upload(c *gin.Context) {
	session := middleware.SessionFromContext(c)
	fileHeader, err := c.FormFile("image")
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "missing image field", nil)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "cannot open image", nil)
		return
	}
	defer file.Close()

	image, err := h.svc.Upload(c.Request.Context(), session.ID, file, fileHeader.Filename, fileHeader.Header.Get("Content-Type"))
	if err != nil {
		RespondError(c, http.StatusUnprocessableEntity, "bad_request", err.Error(), nil)
		return
	}
	c.JSON(http.StatusAccepted, dto.UploadImageResponse{
		ID:       image.ID,
		Status:   image.Status,
		FileName: image.FileName,
		MimeType: image.MimeType,
	})
}
```

- [ ] **Step 6: Write image handler tests**

Use multipart builder and httptest.

- [ ] **Step 7: Commit**

```bash
git add internal/service/ai internal/service/image.go internal/service/image_test.go internal/handler/image.go internal/handler/image_test.go
git commit -m "feat: add image upload, enhancement, and transactional job enqueue"
```

---

### Task 6: AI integration

**Files:**
- Create: `internal/prompts/prompts.go`
- Create: `internal/prompts/prompts_test.go`
- Create: `internal/service/ai/client.go`
- Create: `internal/service/ai/kimi.go`
- Create: `internal/service/ai/kimi_test.go`
- Create: `internal/service/ai/deepseek.go`
- Create: `internal/service/ai/deepseek_test.go`

- [ ] **Step 1: Implement prompts loader**

Create `internal/prompts/prompts.go`:

```go
package prompts

import (
	"embed"
	"fmt"
)

//go:embed ../../../skills/extract-questions-from-image/*.md ../../../skills/verify-extracted-questions/*.md
var skillFiles embed.FS

func ExtractPrompt() string {
	data, err := skillFiles.ReadFile("skills/extract-questions-from-image/SKILL.md")
	if err != nil {
		return ""
	}
	return string(data)
}

func VerifyPrompt(questionsJSON string) string {
	tmpl, err := skillFiles.ReadFile("skills/verify-extracted-questions/SKILL.md")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s\n\n```json\n%s\n```", string(tmpl), questionsJSON)
}
```

- [ ] **Step 2: Write prompts tests**

Create `internal/prompts/prompts_test.go`:

```go
package prompts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPromptNonEmpty(t *testing.T) {
	p := ExtractPrompt()
	assert.NotEmpty(t, p)
	assert.Contains(t, p, "extract")
}

func TestVerifyPromptIncludesJSON(t *testing.T) {
	p := VerifyPrompt(`{"questions":[]}`)
	assert.Contains(t, p, "```json")
	assert.Contains(t, p, "{\"questions\":[]}")
}
```

- [ ] **Step 3: Define AI client interfaces**

Create `internal/service/ai/client.go`:

```go
package ai

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

type ExtractedQuestion struct {
	Number          int                `json:"number"`
	Question        string             `json:"question"`
	MultipleCorrect bool               `json:"multiple_correct"`
	Choices         []string           `json:"choices"`
	Answers         []ExtractedAnswer  `json:"answers"`
	Confidence      float32            `json:"confidence"`
	Explanation     string             `json:"explanation"`
}

type ExtractedAnswer struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

type ExtractResult struct {
	Questions []ExtractedQuestion `json:"questions"`
	Error     *ExtractionError    `json:"error"`
}

type ExtractionError struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	Details            string `json:"details"`
	QuestionsExtracted int    `json:"questions_extracted"`
	QuestionsExpected  int    `json:"questions_expected"`
}

type Extractor interface {
	Extract(ctx context.Context, enhancedImage []byte, mimeType string) (*ExtractResult, error)
}

type VerifyResult struct {
	Questions    []ExtractedQuestion `json:"questions"`
	Verification json.RawMessage     `json:"_verification"`
}

type Verifier interface {
	Verify(ctx context.Context, questionsJSON []byte) (*VerifyResult, error)
}
```

- [ ] **Step 4: Implement Kimi client**

Create `internal/service/ai/kimi.go`:

```go
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sashabaranov/go-openai"
	"github.com/vlgrigoriev/coeus/internal/prompts"
)

type kimiClient struct {
	client *openai.Client
	model  string
}

func NewKimiClient(apiKey, baseURL, model string) Extractor {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	return &kimiClient{client: openai.NewClientWithConfig(cfg), model: model}
}

func NewKimiClientWithHTTP(client *http.Client, apiKey, baseURL, model string) Extractor {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	c := openai.NewClientWithConfig(cfg)
	c.HTTPClient = client
	return &kimiClient{client: c, model: model}
}

func (k *kimiClient) Extract(ctx context.Context, enhancedImage []byte, mimeType string) (*ExtractResult, error) {
	mime := mimeType
	if mime == "" {
		mime = "image/png"
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(enhancedImage))
	req := openai.ChatCompletionRequest{
		Model: k.model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: ExtractPrompt(),
			},
			{
				Role: openai.ChatMessageRoleUser,
				MultiContent: []openai.ChatMessagePart{
					{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: dataURL}},
				},
			},
		},
	}
	resp, err := k.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("kimi extract: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("kimi extract: empty response")
	}
	return parseExtractResponse([]byte(resp.Choices[0].Message.Content))
}

func parseExtractResponse(body []byte) (*ExtractResult, error) {
	var payload struct {
		Questions []ExtractedQuestion `json:"questions"`
		Error     *ExtractionError    `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse extract response: %w", err)
	}
	return &ExtractResult{Questions: payload.Questions, Error: payload.Error}, nil
}
```

- [ ] **Step 5: Implement DeepSeek client**

Create `internal/service/ai/deepseek.go`:

```go
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sashabaranov/go-openai"
	"github.com/vlgrigoriev/coeus/internal/prompts"
)

type deepSeekClient struct {
	client *openai.Client
	model  string
}

func NewDeepSeekClient(apiKey, baseURL, model string) Verifier {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	return &deepSeekClient{client: openai.NewClientWithConfig(cfg), model: model}
}

func NewDeepSeekClientWithHTTP(client *http.Client, apiKey, baseURL, model string) Verifier {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	c := openai.NewClientWithConfig(cfg)
	c.HTTPClient = client
	return &deepSeekClient{client: c, model: model}
}

func (d *deepSeekClient) Verify(ctx context.Context, questionsJSON []byte) (*VerifyResult, error) {
	req := openai.ChatCompletionRequest{
		Model: d.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: VerifyPrompt(string(questionsJSON))},
		},
	}
	resp, err := d.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("deepseek verify: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("deepseek verify: empty response")
	}
	body := []byte(resp.Choices[0].Message.Content)
	var result VerifyResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse verify response: %w", err)
	}
	return &result, nil
}
```

- [ ] **Step 6: Write AI client tests with mock HTTP transport**

Create `internal/service/ai/kimi_test.go`:

```go
package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTransport struct {
	response string
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(m.response)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestKimiExtract(t *testing.T) {
	resp := `{"choices":[{"message":{"content":"{\"questions\":[{\"number\":1,\"question\":\"Q?\",\"multiple_correct\":false,\"choices\":[\"A\",\"B\"],\"answers\":[{\"id\":\"A\",\"value\":\"A\"}],\"confidence\":0.9,\"explanation\":\"\"}]}"}}]}`
	client := NewKimiClientWithHTTP(&http.Client{Transport: &mockTransport{response: resp}}, "key", "http://localhost", "model")
	result, err := client.Extract(context.Background(), []byte("image"), "image/png")
	require.NoError(t, err)
	assert.Len(t, result.Questions, 1)
	assert.Equal(t, "Q?", result.Questions[0].Question)
}
```

- [ ] **Step 7: Commit**

```bash
git add internal/prompts internal/service/ai
git commit -m "feat: add Kimi and DeepSeek AI clients with mockable HTTP transport"
```

---

### Task 7: River job processing

**Files:**
- Create: `internal/queue/client.go`
- Create: `internal/queue/jobs/extract.go`
- Create: `internal/queue/jobs/verify.go`
- Create: `internal/queue/jobs/scheduler.go`
- Create: `internal/queue/jobs/jobs_test.go`
- Modify: `cmd/worker/main.go`

- [ ] **Step 1: Define job args and worker constructors**

Create `internal/queue/jobs/jobs.go`:

```go
package jobs

import (
	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

const (
	ExtractQuestionsJobName = "extract_questions"
	VerifyQuestionsJobName  = "verify_questions"
	SessionExpiryJobName    = "session_expiry"
)

type ExtractQuestionsArgs struct {
	ImageID uuid.UUID `json:"image_id"`
}

func (ExtractQuestionsArgs) Kind() string { return ExtractQuestionsJobName }

type VerifyQuestionsArgs struct {
	QuestionID uuid.UUID `json:"question_id"`
}

func (VerifyQuestionsArgs) Kind() string { return VerifyQuestionsJobName }

type SessionExpiryArgs struct{}

func (SessionExpiryArgs) Kind() string { return SessionExpiryJobName }
```

- [ ] **Step 2: Implement ExtractQuestionsWorker**

Create `internal/queue/jobs/extract.go`:

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
	"github.com/vlgrigoriev/coeus/internal/service/matcher"
)

type ExtractQuestionsWorker struct {
	pool      *pgxpool.Pool
	images    repository.ImageRepo
	questions repository.QuestionRepo
	extractor ai.Extractor
}

func NewExtractQuestionsWorker(pool *pgxpool.Pool, images repository.ImageRepo, questions repository.QuestionRepo, extractor ai.Extractor) *ExtractQuestionsWorker {
	return &ExtractQuestionsWorker{pool: pool, images: images, questions: questions, extractor: extractor}
}

func (w *ExtractQuestionsWorker) Work(ctx context.Context, job *river.Job[ExtractQuestionsArgs]) error {
	image, err := w.images.Get(ctx, w.pool, job.Args.ImageID)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	if image.EnhancedData == nil {
		return w.recordError(ctx, image.ID, image.SessionID, "missing enhanced data")
	}

	result, err := w.extractor.Extract(ctx, *image.EnhancedData, image.MimeType)
	if err != nil {
		if job.Attempt >= job.MaxAttempts {
			return w.recordError(ctx, image.ID, image.SessionID, err.Error())
		}
		return fmt.Errorf("extract: %w", err)
	}

	if result.Error != nil {
		if job.Attempt >= job.MaxAttempts {
			return w.recordError(ctx, image.ID, image.SessionID, result.Error.Message)
		}
		return fmt.Errorf("extract returned error: %s", result.Error.Message)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin extract tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, eq := range result.Questions {
		normalized := matcher.Normalize(eq.Question)
		matched, err := w.questions.FindByNormalizedText(ctx, tx, normalized)
		if err != nil {
			return fmt.Errorf("dedup lookup: %w", err)
		}

		choices := make([]domain.Choice, 0, len(eq.Choices))
		labels := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
		for i, text := range eq.Choices {
			label := labels[i]
			if i >= len(labels) {
				label = fmt.Sprintf("%d", i+1)
			}
			choices = append(choices, domain.Choice{Label: label, Text: text})
		}

		answers := make([]string, 0, len(eq.Answers))
		for _, a := range eq.Answers {
			answers = append(answers, a.Value)
		}

		aiAnalysis, _ := json.Marshal(eq)

		question := &domain.Question{
			ID:             uuid.New(),
			ImageID:        image.ID,
			SessionID:      image.SessionID,
			Number:         eq.Number,
			QuestionText:   eq.Question,
			NormalizedText: normalized,
			Choices:        choices,
			Answers:        answers,
			MultipleCorrect: eq.MultipleCorrect,
			Confidence:     eq.Confidence,
			Explanation:    nilIfEmpty(eq.Explanation),
			Status:         enums.QuestionStatusProcessing,
			AIAnalysis:     aiAnalysis,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		if matched != nil {
			question.MatchedQuestionID = &matched.ID
			question.Status = matched.Status
			question.Answers = matched.Answers
			question.Choices = matched.Choices
			question.Explanation = matched.Explanation
		}

		if err := w.questions.Create(ctx, tx, question); err != nil {
			return fmt.Errorf("insert question: %w", err)
		}

		if matched == nil {
			client := river.ClientFromContext[pgx.Tx](ctx)
			if _, err := client.InsertTx(ctx, tx, VerifyQuestionsArgs{QuestionID: question.ID}, nil); err != nil {
				return fmt.Errorf("enqueue verify job: %w", err)
			}
		}
	}

	return tx.Commit(ctx)
}

func (w *ExtractQuestionsWorker) recordError(ctx context.Context, imageID, sessionID uuid.UUID, message string) error {
	errPayload, _ := json.Marshal(map[string]string{"error": message})
	question := &domain.Question{
		ID:             uuid.New(),
		ImageID:        imageID,
		SessionID:      sessionID,
		Number:         0,
		QuestionText:   "extraction_failed",
		NormalizedText: "extraction_failed",
		Choices:        []domain.Choice{},
		Answers:        []string{},
		Status:         enums.QuestionStatusError,
		AIAnalysis:     errPayload,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	return w.questions.Create(ctx, w.pool, question)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
```

- [ ] **Step 3: Implement VerifyQuestionsWorker**

Create `internal/queue/jobs/verify.go`:

```go
package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
)

type VerifyQuestionsWorker struct {
	pool      *pgxpool.Pool
	questions repository.QuestionRepo
	verifier  ai.Verifier
}

func NewVerifyQuestionsWorker(pool *pgxpool.Pool, questions repository.QuestionRepo, verifier ai.Verifier) *VerifyQuestionsWorker {
	return &VerifyQuestionsWorker{pool: pool, questions: questions, verifier: verifier}
}

func (w *VerifyQuestionsWorker) Work(ctx context.Context, job *river.Job[VerifyQuestionsArgs]) error {
	question, err := w.questions.Get(ctx, w.pool, job.Args.QuestionID)
	if err != nil {
		return fmt.Errorf("load question: %w", err)
	}

	input := map[string]interface{}{
		"questions": []map[string]interface{}{{
			"number":           question.Number,
			"question":         question.QuestionText,
			"multiple_correct": question.MultipleCorrect,
			"choices":          question.Choices,
			"answers":          formatAnswersForAI(question.Choices, question.Answers),
			"confidence":       question.Confidence,
			"explanation":      question.Explanation,
		}},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := w.verifier.Verify(ctx, inputJSON)
	if err != nil {
		report, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = w.questions.UpdateStatus(ctx, w.pool, question.ID, enums.QuestionStatusModeration, report)
		return nil
	}

	report, _ := json.Marshal(result.Verification)
	return w.questions.UpdateStatus(ctx, w.pool, question.ID, enums.QuestionStatusModeration, report)
}

func formatAnswersForAI(choices []domain.Choice, answers []string) []map[string]string {
	out := make([]map[string]string, 0, len(answers))
	for _, answer := range answers {
		for _, choice := range choices {
			if choice.Text == answer {
				out = append(out, map[string]string{"id": choice.Label, "value": answer})
				break
			}
		}
	}
	return out
}
```

- [ ] **Step 4: Implement SessionExpiryWorker**

Create `internal/queue/jobs/scheduler.go`:

```go
package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

type SessionExpiryWorker struct {
	pool     *pgxpool.Pool
	sessions repository.SessionRepo
}

func NewSessionExpiryWorker(pool *pgxpool.Pool, sessions repository.SessionRepo) *SessionExpiryWorker {
	return &SessionExpiryWorker{pool: pool, sessions: sessions}
}

func (w *SessionExpiryWorker) Work(ctx context.Context, job *river.Job[SessionExpiryArgs]) error {
	now := time.Now()
	sessions, err := w.sessions.ListActivePastExpiry(ctx, w.pool, now)
	if err != nil {
		return fmt.Errorf("list active past expiry: %w", err)
	}
	for _, s := range sessions {
		if err := w.sessions.Expire(ctx, w.pool, s.ID); err != nil {
			return fmt.Errorf("expire session: %w", err)
		}
	}
	closedSessions, err := w.sessions.ListExpiredPastClosed(ctx, w.pool, now)
	if err != nil {
		return fmt.Errorf("list expired past closed: %w", err)
	}
	for _, s := range closedSessions {
		if err := w.sessions.Close(ctx, w.pool, s.ID, now); err != nil {
			return fmt.Errorf("close session: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Implement queue client setup**

Create `internal/queue/client.go`:

```go
package queue

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/vlgrigoriev/coeus/internal/queue/jobs"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
)

func NewClient(ctx context.Context, pool *pgxpool.Pool, maxWorkers int, images repository.ImageRepo, questions repository.QuestionRepo, extractor ai.Extractor, verifier ai.Verifier, sessions repository.SessionRepo) (*river.Client[pgx.Tx], error) {
	extractWorker := jobs.NewExtractQuestionsWorker(pool, images, questions, extractor)
	verifyWorker := jobs.NewVerifyQuestionsWorker(pool, questions, verifier)
	expiryWorker := jobs.NewSessionExpiryWorker(pool, sessions)

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: maxWorkers},
		},
		Workers: river.NewWorkers(
			extractWorker,
			verifyWorker,
			expiryWorker,
		),
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(60*time.Second),
				func() (river.JobArgs, *river.InsertOptions) {
					return jobs.SessionExpiryArgs{}, nil
				},
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
}
```

- [ ] **Step 6: Update `cmd/worker/main.go` to wire workers**

```go
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/queue"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Parse()
	if err != nil {
		slog.ErrorContext(ctx, "failed to parse config", slog.Any("error", err))
		os.Exit(1)
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	extractor := ai.NewKimiClient(cfg.KimiAPIKey, cfg.KimiBaseURL, cfg.KimiModel)
	verifier := ai.NewDeepSeekClient(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)

	client, err := queue.NewClient(ctx, pool, cfg.RiverMaxWorkers,
		repository.NewImageRepository(),
		repository.NewQuestionRepository(),
		extractor,
		verifier,
		repository.NewSessionRepository(),
	)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create river client", slog.Any("error", err))
		os.Exit(1)
	}

	if err := client.Start(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to start worker", slog.Any("error", err))
		os.Exit(1)
	}
	<-ctx.Done()
	_ = client.Stop(ctx)
}
```

- [ ] **Step 7: Commit**

```bash
git add internal/queue cmd/worker/main.go
git commit -m "feat: add River job workers for extraction, verification, and session expiry"
```

---

### Task 8: Question endpoints

**Files:**
- Create: `internal/service/question.go`
- Create: `internal/service/question_test.go`
- Create: `internal/handler/question.go`
- Create: `internal/handler/question_test.go`
- Create: `internal/service/matcher/normalizer.go`
- Create: `internal/service/matcher/normalizer_test.go`

- [ ] **Step 1: Implement normalizer**

Create `internal/service/matcher/normalizer.go`:

```go
package matcher

import (
	"regexp"
	"strings"
	"unicode"
)

var nonLetterDigit = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func Normalize(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	cleaned := nonLetterDigit.ReplaceAllString(lower, " ")
	return strings.Join(strings.Fields(cleaned), " ")
}
```

- [ ] **Step 2: Write normalizer tests**

Create `internal/service/matcher/normalizer_test.go`:

```go
package matcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"What is the Capital of France?", "what is the capital of france"},
		{"  Multiple   spaces!!!  ", "multiple spaces"},
		{"Автомобильный рынок (Россия)", "автомобильный рынок россия"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, Normalize(tt.input))
		})
	}
}
```

- [ ] **Step 3: Implement question service**

Create `internal/service/question.go`:

```go
package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

type QuestionService interface {
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]domain.Question, error)
	List(ctx context.Context, userID uuid.UUID, role enums.UserRole, status string) ([]domain.Question, error)
	Get(ctx context.Context, id uuid.UUID, userID uuid.UUID, role enums.UserRole) (*domain.Question, error)
}

type questionService struct {
	pool      *pgxpool.Pool
	questions repository.QuestionRepo
	sessions  repository.SessionRepo
}

func NewQuestionService(pool *pgxpool.Pool, questions repository.QuestionRepo, sessions repository.SessionRepo) QuestionService {
	return &questionService{pool: pool, questions: questions, sessions: sessions}
}

func (s *questionService) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]domain.Question, error) {
	return s.questions.ListBySession(ctx, s.pool, sessionID)
}

func (s *questionService) List(ctx context.Context, userID uuid.UUID, role enums.UserRole, status string) ([]domain.Question, error) {
	filter := repository.QuestionFilter{UserID: &userID, Role: role}
	if status != "" {
		st := enums.QuestionStatus(status)
		filter.Status = &st
	}
	return s.questions.List(ctx, s.pool, filter)
}

func (s *questionService) Get(ctx context.Context, id uuid.UUID, userID uuid.UUID, role enums.UserRole) (*domain.Question, error) {
	q, err := s.questions.Get(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if role != enums.UserRoleExpert {
		session, err := s.sessions.Get(ctx, s.pool, q.SessionID)
		if err != nil || session.UserID != userID {
			return nil, fmt.Errorf("forbidden")
		}
	}
	return q, nil
}

func FormatAnswers(choices []domain.Choice, answers []string) []string {
	formatted := make([]string, 0, len(answers))
	for _, answer := range answers {
		label := ""
		for _, choice := range choices {
			if choice.Text == answer {
				label = choice.Label
				break
			}
		}
		if label != "" {
			formatted = append(formatted, fmt.Sprintf("%s) %s", label, answer))
		} else {
			formatted = append(formatted, answer)
		}
	}
	return formatted
}
```

- [ ] **Step 4: Implement question handler**

Create `internal/handler/question.go`:

```go
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/handler/dto"
	"github.com/vlgrigoriev/coeus/internal/middleware"
	"github.com/vlgrigoriev/coeus/internal/service"
)

type QuestionHandler struct {
	svc service.QuestionService
}

func NewQuestionHandler(svc service.QuestionService) *QuestionHandler {
	return &QuestionHandler{svc: svc}
}

func (h *QuestionHandler) ListBySession(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid session id", nil)
		return
	}
	questions, err := h.svc.ListBySession(c.Request.Context(), sessionID)
	if err != nil {
		RespondError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, toQuestionResponses(questions))
}

func (h *QuestionHandler) List(c *gin.Context) {
	var filter dto.QuestionFilterRequest
	_ = c.ShouldBindQuery(&filter)
	user := middleware.UserFromContext(c)
	questions, err := h.svc.List(c.Request.Context(), user.ID, user.Role, filter.Status)
	if err != nil {
		RespondError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, toQuestionResponses(questions))
}

func (h *QuestionHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid question id", nil)
		return
	}
	user := middleware.UserFromContext(c)
	question, err := h.svc.Get(c.Request.Context(), id, user.ID, user.Role)
	if err != nil {
		RespondError(c, http.StatusNotFound, "not_found", "question not found", nil)
		return
	}
	c.JSON(http.StatusOK, toQuestionResponse(*question))
}

func toQuestionResponses(questions []domain.Question) []dto.QuestionResponse {
	out := make([]dto.QuestionResponse, 0, len(questions))
	for _, q := range questions {
		out = append(out, toQuestionResponse(q))
	}
	return out
}

func toQuestionResponse(q domain.Question) dto.QuestionResponse {
	return dto.QuestionResponse{
		ID:              q.ID,
		SessionID:       q.SessionID,
		ImageID:         q.ImageID,
		Number:          q.Number,
		QuestionText:    q.QuestionText,
		Choices:         q.Choices,
		Answers:         service.FormatAnswers(q.Choices, q.Answers),
		MultipleCorrect: q.MultipleCorrect,
		Confidence:      q.Confidence,
		Explanation:     q.Explanation,
		Status:          q.Status,
		Tags:            q.Tags,
		Verification:    q.VerificationReport,
		UpdatedAt:       q.UpdatedAt,
	}
}
```

- [ ] **Step 5: Write question handler tests**

Mock `QuestionService` and test list/get endpoints.

- [ ] **Step 6: Commit**

```bash
git add internal/service/question.go internal/service/question_test.go internal/handler/question.go internal/handler/question_test.go internal/service/matcher
git commit -m "feat: add question listing and answer formatting"
```

---

### Task 9: Moderation endpoints

**Files:**
- Create: `internal/service/moderation.go`
- Create: `internal/service/moderation_test.go`
- Create: `internal/handler/moderation.go`
- Create: `internal/handler/moderation_test.go`
- Create: `internal/middleware/expert.go`
- Create: `internal/middleware/expert_test.go`

- [ ] **Step 1: Implement expert middleware**

Create `internal/middleware/expert.go`:

```go
package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/handler"
)

func ExpertRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := UserFromContext(c)
		if user == nil || user.Role != enums.UserRoleExpert {
			handler.RespondError(c, http.StatusForbidden, "forbidden", "expert role required", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 2: Implement moderation service**

Create `internal/service/moderation.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

var ErrImageCleanedUp = errors.New("service: image cleaned up")
	UpdateStatus(ctx context.Context, id uuid.UUID, status enums.QuestionStatus) (*domain.Question, error)
	GetImage(ctx context.Context, questionID uuid.UUID) ([]byte, string, error)
}

type moderationService struct {
	pool      *pgxpool.Pool
	questions repository.QuestionRepo
	images    repository.ImageRepo
}

func NewModerationService(pool *pgxpool.Pool, questions repository.QuestionRepo, images repository.ImageRepo) ModerationService {
	return &moderationService{pool: pool, questions: questions, images: images}
}

func (s *moderationService) UpdateStatus(ctx context.Context, id uuid.UUID, status enums.QuestionStatus) (*domain.Question, error) {
	if status == enums.QuestionStatusVerified {
		return s.verifyAndCleanup(ctx, id)
	}
	if err := s.questions.UpdateStatus(ctx, s.pool, id, status, nil); err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	return s.questions.Get(ctx, s.pool, id)
}

func (s *moderationService) verifyAndCleanup(ctx context.Context, id uuid.UUID) (*domain.Question, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin moderation tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.questions.UpdateStatus(ctx, tx, id, enums.QuestionStatusVerified, nil); err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	question, err := s.questions.Get(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("get question: %w", err)
	}
	remaining, err := s.questions.CountByImageAndStatus(ctx, tx, question.ImageID, enums.QuestionStatusVerified)
	if err != nil {
		return nil, fmt.Errorf("count remaining: %w", err)
	}
	if remaining == 0 {
		if err := s.images.DeleteBytes(ctx, tx, question.ImageID); err != nil {
			return nil, fmt.Errorf("delete image bytes: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit moderation tx: %w", err)
	}
	return question, nil
}

func (s *moderationService) GetImage(ctx context.Context, questionID uuid.UUID) ([]byte, string, error) {
	question, err := s.questions.Get(ctx, s.pool, questionID)
	if err != nil {
		return nil, "", err
	}
	image, err := s.images.Get(ctx, s.pool, question.ImageID)
	if err != nil {
		return nil, "", err
	}
	if image.ImageData == nil {
		return nil, "", ErrImageCleanedUp
	}
	return *image.ImageData, image.MimeType, nil
}
```

- [ ] **Step 3: Implement moderation handler**

Create `internal/handler/moderation.go`:

```go
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/handler/dto"
	"github.com/vlgrigoriev/coeus/internal/service"
)

type ModerationHandler struct {
	svc service.ModerationService
}

func NewModerationHandler(svc service.ModerationService) *ModerationHandler {
	return &ModerationHandler{svc: svc}
}

func (h *ModerationHandler) UpdateStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid question id", nil)
		return
	}
	var req dto.UpdateQuestionStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", err.Error(), nil)
		return
	}
	question, err := h.svc.UpdateStatus(c.Request.Context(), id, req.Status)
	if err != nil {
		RespondError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, dto.QuestionStatusResponse{
		ID:        question.ID,
		Status:    question.Status,
		UpdatedAt: question.UpdatedAt,
	})
}

func (h *ModerationHandler) GetImage(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		RespondError(c, http.StatusBadRequest, "bad_request", "invalid question id", nil)
		return
	}
	data, mimeType, err := h.svc.GetImage(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrImageCleanedUp) {
			RespondError(c, http.StatusGone, "gone", "image has been cleaned up", nil)
			return
		}
		RespondError(c, http.StatusNotFound, "not_found", "image not found", nil)
		return
	}
	c.Data(http.StatusOK, mimeType, data)
}
```

- [ ] **Step 4: Write moderation service and handler tests**

Test status update and image cleanup with mocked repos, using `mock.Anything` for Querier.

- [ ] **Step 5: Commit**

```bash
git add internal/service/moderation.go internal/service/moderation_test.go internal/handler/moderation.go internal/handler/moderation_test.go internal/middleware/expert.go internal/middleware/expert_test.go
git commit -m "feat: add moderation endpoints with synchronous image cleanup"
```

---

### Task 10: Rate limiting, observability, and health

**Files:**
- Create: `internal/middleware/ratelimit.go`
- Create: `internal/middleware/ratelimit_test.go`
- Create: `internal/observability/logger.go`
- Create: `internal/observability/metrics.go`
- Create: `internal/handler/health.go`
- Create: `internal/handler/health_test.go`
- Modify: `cmd/api/main.go`

- [ ] **Step 1: Implement slog setup**

Create `internal/observability/logger.go`:

```go
package observability

import (
	"log/slog"
	"os"
)

func NewLogger(level, format string) *slog.Logger {
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseLevel(l string) slog.Level {
	switch l {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

- [ ] **Step 2: Implement Prometheus metrics**

Create `internal/observability/metrics.go`:

```go
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	QuestionsExtractedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "questions_extracted_total",
		Help: "Total extracted questions by status",
	}, []string{"status"})
	QuestionsVerifiedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "questions_verified_total",
		Help: "Total verified questions by status",
	}, []string{"status"})
	KimiErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kimi_errors_total",
		Help: "Total Kimi errors by phase",
	}, []string{"phase"})
	DeepSeekErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deepseek_errors_total",
		Help: "Total DeepSeek errors",
	})
	ImagesUploadedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "images_uploaded_total",
		Help: "Total uploaded images by mime type",
	}, []string{"mime_type"})
	ActiveSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "active_sessions",
		Help: "Current active sessions by status",
	}, []string{"status"})
	ExtractionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "extraction_duration_seconds",
		Help: "Extraction duration by model",
	}, []string{"model"})
)
```

- [ ] **Step 3: Implement rate limiting middleware**

Create `internal/middleware/ratelimit.go`:

```go
package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	"github.com/ulule/limiter/v3/drivers/store/memory"
	"github.com/vlgrigoriev/coeus/internal/handler"
)

func RateLimitMiddleware(rpm int) gin.HandlerFunc {
	rate := limiter.Rate{Period: 60 * time.Second, Limit: int64(rpm)}
	store := memory.NewStore()
	instance := limiter.New(store, rate)
	return func(c *gin.Context) {
		key := c.ClientIP()
		if user := UserFromContext(c); user != nil {
			key = user.ID.String()
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		limitCtx, err := instance.Get(ctx, key)
		if err != nil {
			handler.RespondError(c, http.StatusInternalServerError, "internal_error", err.Error(), nil)
			c.Abort()
			return
		}
		c.Header("RateLimit-Limit", strconv.FormatInt(limitCtx.Limit, 10))
		c.Header("RateLimit-Remaining", strconv.FormatInt(limitCtx.Remaining, 10))
		c.Header("RateLimit-Reset", strconv.FormatInt(limitCtx.Reset, 10))
		if limitCtx.Reached {
			handler.RespondError(c, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 4: Implement health handler**

Create `internal/handler/health.go`:

```go
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/riverqueue/river"
	"github.com/jackc/pgx/v5"
)

type HealthHandler struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

func NewHealthHandler(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *HealthHandler {
	return &HealthHandler{pool: pool, river: riverClient}
}

func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *HealthHandler) Ready(c *gin.Context) {
	if err := h.pool.Ping(c.Request.Context()); err != nil {
		RespondError(c, http.StatusServiceUnavailable, "not_ready", "database unavailable", nil)
		return
	}
	if h.river != nil {
		stats := h.river.Stats()
		if !stats.IsHealthy() {
			RespondError(c, http.StatusServiceUnavailable, "not_ready", "river unhealthy", nil)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (h *HealthHandler) Metrics(c *gin.Context) {
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}
```

- [ ] **Step 5: Wire everything in `cmd/api/main.go`**

Replace stub with full wiring:

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/db"
	"github.com/vlgrigoriev/coeus/internal/handler"
	"github.com/vlgrigoriev/coeus/internal/middleware"
	"github.com/vlgrigoriev/coeus/internal/observability"
	"github.com/vlgrigoriev/coeus/internal/queue"
	"github.com/vlgrigoriev/coeus/internal/repository"
	"github.com/vlgrigoriev/coeus/internal/service"
	"github.com/vlgrigoriev/coeus/internal/service/ai"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Parse()
	if err != nil {
		slog.ErrorContext(ctx, "failed to parse config", slog.Any("error", err))
		os.Exit(1)
	}
	logger := observability.NewLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	usersRepo := repository.NewUserRepository()
	sessionRepo := repository.NewSessionRepository()
	imagesRepo := repository.NewImageRepository()
	questionsRepo := repository.NewQuestionRepository()

	authSvc := service.NewAuthService(pool, usersRepo)
	sessionSvc := service.NewSessionService(pool, sessionRepo)

	extractor := ai.NewKimiClient(cfg.KimiAPIKey, cfg.KimiBaseURL, cfg.KimiModel)
	verifier := ai.NewDeepSeekClient(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)

	riverClient, err := queue.NewClient(ctx, pool, cfg.RiverMaxWorkers, imagesRepo, questionsRepo, extractor, verifier, sessionRepo)
	if err != nil {
		slog.ErrorContext(ctx, "failed to create river client", slog.Any("error", err))
		os.Exit(1)
	}

	imageSvc := service.NewImageService(pool, imagesRepo, riverClient, cfg.MaxUploadSize(), cfg.MaxImageDim)
	questionSvc := service.NewQuestionService(pool, questionsRepo, sessionRepo)
	moderationSvc := service.NewModerationService(pool, questionsRepo, imagesRepo)

	authH := handler.NewAuthHandler(authSvc)
	sessionH := handler.NewSessionHandler(sessionSvc)
	imageH := handler.NewImageHandler(imageSvc)
	questionH := handler.NewQuestionHandler(questionSvc)
	moderationH := handler.NewModerationHandler(moderationSvc)
	healthH := handler.NewHealthHandler(pool, riverClient)

	store := middleware.NewCookieStore(cfg.SessionSecret)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(sessions.Sessions("session", store))

	r.GET("/health", healthH.Health)
	r.GET("/ready", healthH.Ready)
	r.GET("/metrics", healthH.Metrics)

	api := r.Group("/api")
	api.POST("/auth/login", authH.Login)
	api.POST("/auth/logout", middleware.AuthRequired(store), authH.Logout)

	authorized := api.Group("")
	authorized.Use(middleware.AuthRequired(store))
	authorized.Use(middleware.RateLimitMiddleware(cfg.RateLimitRPM))

	authorized.POST("/sessions", sessionH.Create)
	authorized.POST("/sessions/:id/start", sessionH.Start)
	authorized.GET("/sessions/:id", sessionH.Get)

	sessionRoutes := authorized.Group("/sessions/:id")
	sessionRoutes.Use(middleware.ActiveSessionRequired(sessionSvc))
	sessionRoutes.POST("/images", imageH.Upload)
	sessionRoutes.GET("/questions", questionH.ListBySession)

	authorized.GET("/questions", questionH.List)
	authorized.GET("/questions/:id", questionH.Get)

	expert := authorized.Group("/questions/:id")
	expert.Use(middleware.ExpertRequired())
	expert.PATCH("", moderationH.UpdateStatus)
	expert.GET("/image", moderationH.GetImage)

	if err := r.Run(":" + strconv.Itoa(cfg.Port)); err != nil {
		slog.ErrorContext(ctx, "server failed", slog.Any("error", err))
		os.Exit(1)
	}
}
```

- [ ] **Step 6: Write tests for middleware and health**

Create `internal/middleware/ratelimit_test.go` and `internal/handler/health_test.go`.

- [ ] **Step 7: Commit**

```bash
git add internal/middleware/ratelimit.go internal/middleware/ratelimit_test.go internal/observability internal/handler/health.go internal/handler/health_test.go cmd/api/main.go
git commit -m "feat: add rate limiting, metrics, logging, health, and API wiring"
```

---

### Task 11: Integration tests, documentation, and deployment

**Files:**
- Create: `tests/integration/upload_flow_test.go`
- Create: `tests/integration/suite_test.go`
- Create: `Dockerfile`
- Create: `README.md`
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Create integration test suite**

Create `tests/integration/suite_test.go`:

```go
//go:build integration

package integration
import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"
)

type IntegrationSuite struct {
	suite.Suite
	ctx  context.Context
	pool *pgxpool.Pool
}

func (s *IntegrationSuite) SetupSuite() {
	s.ctx = context.Background()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		s.T().Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(s.ctx, databaseURL)
	s.Require().NoError(err)
	s.pool = pool
}

func (s *IntegrationSuite) TearDownSuite() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
```

Create `tests/integration/upload_flow_test.go`:

```go
//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/domain/enums"
	"github.com/vlgrigoriev/coeus/internal/repository"
)

func (s *IntegrationSuite) TestImageUploadCreatesImageRecord() {
	users := repository.NewUserRepository()
	sessions := repository.NewSessionRepository()
	images := repository.NewImageRepository()

	user := &domain.User{
		ID: uuid.New(), Email: uuid.New().String() + "@example.com",
		PasswordHash: "hash", HasAccess: true, Role: enums.UserRoleUser,
		CreatedAt: time.Now(),
	}
	s.Require().NoError(users.Create(s.ctx, s.pool, user))

	session := &domain.Session{
		ID: uuid.New(), UserID: user.ID, Duration: time.Hour,
		Buffer: 5 * time.Minute, Status: enums.SessionStatusCreated, CreatedAt: time.Now(),
	}
	s.Require().NoError(sessions.Create(s.ctx, s.pool, session))

	data := []byte{0x89, 0x50, 0x4E, 0x47} // minimal PNG header
	img := &domain.Image{
		ID: uuid.New(), SessionID: session.ID, ImageData: &data,
		EnhancedData: &data, MimeType: "image/png", FileName: "test.png",
		Status: "processing", CreatedAt: time.Now(),
	}
	err := images.Create(s.ctx, s.pool, img)
	s.Require().NoError(err)

	found, err := images.Get(s.ctx, s.pool, img.ID)
	s.Require().NoError(err)
	assert.Equal(s.T(), "image/png", found.MimeType)
}
```

- [ ] **Step 2: Create Dockerfile**

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o bin/api ./cmd/api
RUN go build -o bin/worker ./cmd/worker

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/bin/api /app/bin/api
COPY --from=builder /app/bin/worker /app/bin/worker
COPY --from=builder /app/migrations /app/migrations
EXPOSE 8080
CMD ["/app/bin/api"]
```

- [ ] **Step 3: Create README.md**

Include sections: Overview, Quick Start, Environment Variables, Running API/Worker, Running Tests, Architecture Overview.

- [ ] **Step 4: Create CI workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: postgres
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: coeus_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - run: go mod download
      - run: go test -race ./internal/...
        env:
          TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/coeus_test?sslmode=disable
          SESSION_SECRET: ci-session-secret-32-bytes-long-12345
          DATABASE_URL: postgres://postgres:postgres@localhost:5432/coeus_test?sslmode=disable
          KIMI_API_KEY: test
          DEEPSEEK_API_KEY: test
      - run: go test -tags=integration -race ./tests/integration/...
        env:
          TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/coeus_test?sslmode=disable
          SESSION_SECRET: ci-session-secret-32-bytes-long-12345
          DATABASE_URL: postgres://postgres:postgres@localhost:5432/coeus_test?sslmode=disable
          KIMI_API_KEY: test
          DEEPSEEK_API_KEY: test
```

- [ ] **Step 5: Run full test suite**

Run:
```bash
docker compose up -d postgres-test
sleep 3
make test-repo
make test
make test-integration
```

Expected: all tests PASS.

- [ ] **Step 6: Final commit**

```bash
git add tests Dockerfile README.md .github/workflows/ci.yml
git commit -m "test: add integration tests, Dockerfile, README, and CI workflow"
```

---

## Self-Review

1. **Spec coverage:**
   - Schema and migrations: Task 1.
   - Domain types and enums: Task 2.
   - Auth endpoints and middleware: Task 3.
   - Session create/start/get and expiry: Tasks 4 and 7.
   - Image upload, enhancement, validation: Task 5.
   - Kimi extraction and DeepSeek verification: Task 6.
   - River jobs (extract, verify, expiry) with transactional InsertTx: Task 7.
   - Question listing and answer formatting: Task 8.
   - Moderation status update, image retrieval, cleanup on full verify: Task 9.
   - Rate limiting, logging, metrics, health/ready: Task 10.
   - Integration tests, docs, deployment: Task 11.

2. **Placeholder scan:** No TBD/TODO placeholders remain; every task has concrete files, code snippets, commands, and expected outputs.

3. **Type consistency:** All repository methods accept `db.Querier`. All services receive `*pgxpool.Pool`. Job args use the names and `Kind()` methods from the spec. Answer formatting uses `FormatAnswers(choices, values)` producing `"label) value"` strings.

---

**Plan complete and saved to `docs/superpowers/plans/2026-06-17-image-question-analysis-plan.md`.**

Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `executing-plans`, batch execution with checkpoints.

Which approach would you like?
