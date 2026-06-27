# Coeus — Manual Questions & CORS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `POST /api/v1/questions` expert-only endpoint that hand-authors verified canonical questions, and add `gin-contrib/cors` middleware so browsers can call the API cross-origin.

**Architecture:** Two independent features sharing the `NewServer` / `wire.go` seam. Feature 1 extracts `normalizeQuestion`/`sha256String` from the `pipeline` package into `domain` as exported helpers (byte-for-byte identical — no punctuation stripping), extends `QuestionRepo.Create` to persist `verified_at`/`verified_by`, and adds a `Create` handler that does exact-hash dedup → best-effort embed → `verified` insert. Feature 2 adds a `CORSConfig` sub-struct with YAML defaults + two env overrides + startup validation, then wires `gin-contrib/cors` between `Recover`/`RequestLog` and route registration so OPTIONS preflight returns 204 (not 401).

**Tech Stack:** Go 1.26.3, Gin, PGX/v5, pgvector, `github.com/gin-contrib/cors` (new).

**Spec:** `docs/superpowers/specs/2026-06-27-coeus-manual-questions-and-cors-design.md`

---

## Build & Test Notes (read first)

- **CGO + libvips are required for `go build ./...`** (the `internal/ai/enhancer` package uses govips). Per `AGENTS.md`: macOS `brew install vips pkg-config`; Debian `apt-get install gcc libc6-dev pkg-config libvips-dev`. If the local toolchain isn't set up, use `docker build -t coeus .`.
- **The unit tests for these features do NOT need libvips or Docker.** They target packages that don't transitively import govips: `internal/domain`, `internal/config`, `internal/pipeline`, `internal/httpapi/handlers`, `internal/httpapi`. Package-scoped `go test ./internal/domain/ ...` works without CGO.
- **`go build ./...` is the integration-level build check** (needs libvips). Where a task is pure-Go, the verification step targets only the affected packages so it runs without libvips.
- **No linter / no CI / no Makefile** — use `go` directly. Vet with `go vet ./...`.
- **`*.md` and `docs/` are gitignored** — this plan file won't appear in `git status`. The engineer does NOT need to track it to do the work. Use `git add -f` only if they want to commit the plan itself.
- **Migrations run automatically on boot** (`postgres.RunMigrations` in `app.Build`). No separate `migrate` step — just drop the new `.sql` file in.

---

## File Structure

**Feature 1 — Manual Question Creation:**

| File | Action | Responsibility |
|---|---|---|
| `internal/domain/question.go` | Modify | Add exported `NormalizeQuestion`, `HashQuestion` (moved verbatim from pipeline) |
| `internal/domain/question_test.go` | Create | Unit tests for the two helpers |
| `internal/pipeline/pipeline.go` | Modify | Switch 2 call sites to `domain.*`; delete local copies; drop 3 now-unused imports |
| `internal/storage/postgres/migrations/0003_manual_tag.sql` | Create | Seed `manual-entry` tag |
| `internal/storage/postgres/question_repo.go` | Modify | Extend `Create` INSERT to persist `verified_at`/`verified_by` |
| `internal/httpapi/dto/requests.go` | Modify | Add `CreateQuestionRequest` |
| `internal/httpapi/handlers/questions.go` | Modify | Add `embedder` field + ctor param; add `Create` method |
| `internal/httpapi/handlers/questions_test.go` | Modify | Make `fakeQuestionRepo.Create`/`FindExact` configurable; add `fakeEmbedder`; update `newQuestionRouter`; add Create tests |
| `internal/httpapi/server.go` | Modify | `NewServer` gains `embedder`; store on struct; pass to `NewQuestionHandler`; add `questions.POST` route |
| `internal/app/wire.go` | Modify | Pass `emb` to `NewServer` |

**Feature 2 — CORS Configuration:**

| File | Action | Responsibility |
|---|---|---|
| `go.mod` / `go.sum` | Modify | Add `github.com/gin-contrib/cors` |
| `internal/config/config.go` | Modify | Add `CORSConfig` struct, `ServerConfig.CORS` field, 2 env overrides, startup validation |
| `internal/config/config.yaml` | Modify | Add `server.cors` defaults |
| `internal/config/config_test.go` | Modify | Add CORS validation test |
| `internal/httpapi/server.go` | Modify | `NewServer` gains `corsCfg`; mount cors middleware after `Recover`/`RequestLog`, before `registerRoutes` |
| `internal/app/wire.go` | Modify | Pass `cfg.Server.CORS` to `NewServer` |
| `internal/httpapi/cors_test.go` | Create | httptest: preflight 204 (not 401), origin echo |

---

## Task Ordering Rationale

Refactor/dependency tasks first; each task compiles and passes tests on its own:

1. **Task 1** — domain helpers + tests (no deps; pure functions)
2. **Task 2** — pipeline refactor to use domain helpers (depends on Task 1; behavior-preserving — existing pipeline tests must pass unchanged)
3. **Task 3** — migration + `Create` repo extension (no dep on handlers; backward-compatible)
4. **Task 4** — `CreateQuestionRequest` DTO (no deps)
5. **Task 5** — `QuestionHandler.Create` + constructor change + handler tests (depends on Tasks 1, 3, 4; testable via standalone gin engine, no NewServer/DB)
6. **Task 6** — Feature 1 wiring: `NewServer` gains `embedder`, POST route, `wire.go` threads embedder (depends on Task 5; full build check)
7. **Task 7** — CORS config struct + defaults + env overrides + validation + test (independent of NewServer)
8. **Task 8** — CORS dependency + middleware wiring + tests (depends on Task 7; `NewServer` gains `corsCfg`)

The two features share `NewServer`/`wire.go`. Task 6 changes `NewServer` to add `embedder`; Task 8 changes it again to add `corsCfg`. They are sequential (6 before 8), so Task 8 builds on Task 6's result — no conflict.

---

## Task 1: Domain helpers — `NormalizeQuestion` + `HashQuestion`

**Files:**
- Modify: `internal/domain/question.go`
- Test: `internal/domain/question_test.go` (create)

These are byte-for-byte copies of the unexported `normalizeQuestion`/`sha256String` in `internal/pipeline/pipeline.go:290-297`. **Do not alter the algorithm** (no punctuation stripping) — existing DB hashes depend on it.

- [ ] **Step 1: Write the failing tests**

Create `internal/domain/question_test.go`:

```go
package domain

import (
	"testing"
)

func TestNormalizeQuestion_LowercasesAndFoldsWhitespace(t *testing.T) {
	got := NormalizeQuestion("  What IS   2+2?  ")
	want := "what is 2+2?"
	if got != want {
		t.Errorf("NormalizeQuestion: got %q want %q", got, want)
	}
}

func TestNormalizeQuestion_Idempotent(t *testing.T) {
	once := NormalizeQuestion("Foo\tBAR\n baz")
	twice := NormalizeQuestion(once)
	if once != twice {
		t.Errorf("NormalizeQuestion not idempotent: %q vs %q", once, twice)
	}
}

func TestNormalizeQuestion_EmptyAndWhitespaceOnly(t *testing.T) {
	if NormalizeQuestion("   ") != "" {
		t.Error("whitespace-only should normalize to empty")
	}
	if NormalizeQuestion("") != "" {
		t.Error("empty should stay empty")
	}
}

func TestHashQuestion_Deterministic(t *testing.T) {
	h1 := HashQuestion("what is 2+2?")
	h2 := HashQuestion("what is 2+2?")
	if h1 != h2 {
		t.Errorf("HashQuestion not deterministic: %q vs %q", h1, h2)
	}
}

func TestHashQuestion_KnownVector(t *testing.T) {
	// sha256("what is 2+2?") — pinned so a future algorithm change is caught.
	// (Computed with: printf '%s' 'what is 2+2?' | shasum -a 256)
	want := "61a3385003d9b9d390dc511fbe3e1eb6bee637ec8c3c9eeb555395cddf838f5e"
	got := HashQuestion("what is 2+2?")
	if len(got) != 64 {
		t.Fatalf("HashQuestion length: got %d want 64", len(got))
	}
	if got != want {
		t.Errorf("HashQuestion known vector: got %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (undefined symbols)**

Run: `go test ./internal/domain/ -run 'NormalizeQuestion|HashQuestion' -v`
Expected: build failure — `undefined: NormalizeQuestion` / `undefined: HashQuestion`.

- [ ] **Step 3: Implement the helpers**

Append to `internal/domain/question.go` (after `InferChoiceLabeling`). Add the needed imports (`crypto/sha256`, `encoding/hex`, `strings`) to the existing import block:

```go
// NormalizeQuestion folds a question string to a canonical form for dedup:
// trim, lowercase, collapse all runs of whitespace to single spaces.
// It is byte-for-byte identical to the former pipeline.normalizeQuestion —
// do NOT add punctuation stripping, it would invalidate stored hashes.
func NormalizeQuestion(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// HashQuestion returns the lowercase hex sha256 of the (already-normalized)
// question text. Used for exact-hash dedup against the questions.question_hash column.
func HashQuestion(norm string) string {
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}
```

The `internal/domain/question.go` import block becomes:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/domain/ -run 'NormalizeQuestion|HashQuestion' -v`
Expected: PASS — all five tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/question.go internal/domain/question_test.go
git commit -m "feat(domain): add exported NormalizeQuestion/HashQuestion helpers"
```

---

## Task 2: Pipeline refactor — switch to domain helpers (behavior-preserving)

**Files:**
- Modify: `internal/pipeline/pipeline.go`

`sha256String` is used at **two** sites: `pipeline.go:139` (`hash := sha256String(norm)`) and `pipeline.go:271` (`hash := sha256String("error:" + img.ID)`). `normalizeQuestion` is used once at `pipeline.go:138`. After switching all three call sites and deleting the two local functions, the imports `crypto/sha256`, `encoding/hex`, and `strings` become unused and must be removed (Go rejects unused imports).

- [ ] **Step 1: Switch the call sites**

In `internal/pipeline/pipeline.go`, replace the Step 4a/4b lines (currently):

```go
		norm := normalizeQuestion(eq.Text)
		hash := sha256String(norm)
```

with:

```go
		norm := domain.NormalizeQuestion(eq.Text)
		hash := domain.HashQuestion(norm)
```

Then in `handleExtractionFailure`, replace (currently `pipeline.go:271`):

```go
	hash := sha256String("error:" + img.ID)
```

with:

```go
	hash := domain.HashQuestion("error:" + img.ID)
```

- [ ] **Step 2: Delete the local helper functions**

Delete the entire block (currently `pipeline.go:290-297`):

```go
func normalizeQuestion(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func sha256String(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
```

- [ ] **Step 3: Remove the now-unused imports**

Edit the import block at the top of `internal/pipeline/pipeline.go`. Remove these three lines:

```go
	"crypto/sha256"
	"encoding/hex"
	"strings"
```

The resulting import block should be exactly:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

(`domain` is already imported and is now used by the switched call sites.)

- [ ] **Step 4: Build + vet the pipeline package**

Run: `go build ./internal/pipeline/ && go vet ./internal/pipeline/`
Expected: no output (success). If you see "imported and not used", re-check Step 3.

- [ ] **Step 5: Run pipeline unit tests — they must pass UNCHANGED**

Run: `go test ./internal/pipeline/ -v`
Expected: PASS — all existing pipeline tests green. This proves the refactor is behavior-preserving (the helpers are byte-for-byte identical, so hashes are unchanged).

> Note: if `go test ./internal/pipeline/` triggers integration tests needing Docker, run `go test -short ./internal/pipeline/` instead — the short tests cover the normalization/hashing behavior.

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/pipeline.go
git commit -m "refactor(pipeline): use domain.NormalizeQuestion/HashQuestion; delete local copies"
```

---

## Task 3: Migration `0003` + `QuestionRepo.Create` extension

**Files:**
- Create: `internal/storage/postgres/migrations/0003_manual_tag.sql`
- Modify: `internal/storage/postgres/question_repo.go`

The current `Create` INSERT (lines 36-45) lists 12 columns with placeholders `$1`–`$12` and omits `verified_at`/`verified_by`. Extend it to 14 columns/placeholders. Both columns are nullable; the pipeline passes `nil` (→ NULL, backward-compatible), the manual handler passes real values.

- [ ] **Step 1: Create the migration**

Create `internal/storage/postgres/migrations/0003_manual_tag.sql`:

```sql
-- Seeds the manual-entry tag used to mark hand-authored questions (spec §3.4).
-- Not strictly required (linkTag upserts by name) but keeps the tags table tidy
-- and makes manual-entry visible to the expert tag filter.
INSERT INTO tags (name) VALUES ('manual-entry') ON CONFLICT DO NOTHING;
```

- [ ] **Step 2: Extend the `Create` INSERT**

In `internal/storage/postgres/question_repo.go`, replace the `Create` method body's query block (currently lines 36-45):

```go
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    multiple_correct, choices, answers, choice_labeling, confidence,
		    explanation, embedding, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		q.MultipleCorrect, choicesJSON, answersJSON, q.ChoiceLabeling,
		q.Confidence, q.Explanation, embedding, q.Status,
	).Scan(&id)
```

with:

```go
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    multiple_correct, choices, answers, choice_labeling, confidence,
		    explanation, embedding, status, verified_at, verified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		q.MultipleCorrect, choicesJSON, answersJSON, q.ChoiceLabeling,
		q.Confidence, q.Explanation, embedding, q.Status,
		q.VerifiedAt, q.VerifiedBy,
	).Scan(&id)
```

`q.VerifiedAt` and `q.VerifiedBy` are `*string` on `domain.Question`; a nil pointer serializes to SQL NULL. The `QuestionRepo` interface in `internal/storage/ports.go` is **unchanged**.

- [ ] **Step 3: Build + vet the storage package**

Run: `go build ./internal/storage/... && go vet ./internal/storage/...`
Expected: no output (success).

- [ ] **Step 4: (Optional) Verify migration applies with Docker**

> Only if Docker is running and you want to confirm the SQL. Not required for unit tests.

Run: `go test ./internal/storage/postgres/ -run TestMigrations -timeout 180s`
Expected: PASS (migration 0003 applies cleanly alongside 0001/0002). If Docker isn't running, skip — the SQL is idempotent (`ON CONFLICT DO NOTHING`) and trivial.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/migrations/0003_manual_tag.sql internal/storage/postgres/question_repo.go
git commit -m "feat(storage): seed manual-entry tag; persist verified_at/by in QuestionRepo.Create"
```

---

## Task 4: `CreateQuestionRequest` DTO

**Files:**
- Modify: `internal/httpapi/dto/requests.go`

- [ ] **Step 1: Add the DTO**

Append to `internal/httpapi/dto/requests.go` (after `CreateSessionRequest`):

```go
// CreateQuestionRequest is the body of POST /api/v1/questions (expert-only, spec §3.5).
// `number` is intentionally absent (defaults to 0 in the DB); manual questions are
// free-standing canonical entries, not tied to a session or image.
type CreateQuestionRequest struct {
	Question        string   `json:"question" binding:"required"`
	Choices         []string `json:"choices" binding:"required,min=2"`
	Answers         []string `json:"answers" binding:"required,min=1"`
	MultipleCorrect bool     `json:"multiple_correct"`
	ChoiceLabeling  string   `json:"choice_labeling"`
	Explanation     string   `json:"explanation"`
	Tags            []string `json:"tags"`
	Confidence      *float64 `json:"confidence"`
}
```

- [ ] **Step 2: Build the dto package**

Run: `go build ./internal/httpapi/dto/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/httpapi/dto/requests.go
git commit -m "feat(dto): add CreateQuestionRequest"
```

---

## Task 5: `QuestionHandler.Create` + constructor change + handler tests

**Files:**
- Modify: `internal/httpapi/handlers/questions.go`
- Modify: `internal/httpapi/handlers/questions_test.go`

The handler's `Create` flow (spec §3.2): bind → validate `choice_labeling` + `confidence` → `domain.NormalizeQuestion`/`HashQuestion` → exact dedup (`FindExact`) → on hit `409` inline → best-effort embed → assemble `domain.Question` (`status=verified`, `verified_at=now`, `verified_by=caller`, tags = `req.Tags + ["manual-entry"]`, `number=0`) → `Create` → `FindExpertByID` → `201`.

The constructor gains `embedder pipeline.AIEmbedder` (nil-safe). This adds a new production dependency: `handlers` → `pipeline` (for the `AIEmbedder` interface type).

- [ ] **Step 1: Update the existing test fakes + helper FIRST (so the package still compiles after the constructor change)**

In `internal/httpapi/handlers/questions_test.go`:

**1a.** Make `fakeQuestionRepo.Create` and `FindExact` configurable. Replace the two current stubs (lines 35 and 39):

```go
func (f *fakeQuestionRepo) Create(context.Context, *domain.Question) (string, error) { return "", nil }
```

```go
func (f *fakeQuestionRepo) FindExact(context.Context, string) (*domain.Question, error) { return nil, nil }
```

with configurable versions, and add capture fields to the struct. First, extend the `fakeQuestionRepo` struct — add these fields (place near the other func fields at the top of the struct, before the `updateCalled`/`updateArgs` block):

```go
type fakeQuestionRepo struct {
	create    func(ctx context.Context, q *domain.Question) (string, error)
	createArg *domain.Question
	findExact func(ctx context.Context, hash string) (*domain.Question, error)

	expertByID     func(id string) (*storage.QuestionExpertView, error)
	listModeration func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error)
	listForUser    func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
	forUserByID    func(qid, uid string) (*storage.QuestionWithSession, error)
	updateByExpert func(id string, answers, choices []string, explanation string, conf float64, tags []string, expertID string) error
	updateCalled   bool
	updateArgs     struct {
		id, expertID     string
		answers, choices []string
		explanation      string
		conf             float64
		tags             []string
	}
}
```

Then replace the two method stubs:

```go
func (f *fakeQuestionRepo) Create(ctx context.Context, q *domain.Question) (string, error) {
	f.createArg = q
	if f.create != nil {
		return f.create(ctx, q)
	}
	return "q-new", nil
}
func (f *fakeQuestionRepo) FindExact(ctx context.Context, hash string) (*domain.Question, error) {
	if f.findExact != nil {
		return f.findExact(ctx, hash)
	}
	return nil, nil
}
```

**1b.** Add a fake embedder + a Create-aware router helper. Add to the test file (near the other fakes, after `fakeQuestionSessionRepo`):

```go
type fakeEmbedder struct {
	embed func(ctx context.Context, text string) ([]float32, error)
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}
```

**1c.** Update `newQuestionRouter` to pass a `nil` embedder (existing List/Get/Update tests don't exercise embedding). Replace the current helper (lines 91-100):

```go
func newQuestionRouter(role, userID string, q storage.QuestionRepo, s storage.SessionRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewQuestionHandler(q, s)
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	r.GET("/api/v1/questions", h.List)
	r.GET("/api/v1/questions/:id", h.Get)
	r.PATCH("/api/v1/questions/:id", h.Update)
	return r
}
```

with:

```go
func newQuestionRouter(role, userID string, q storage.QuestionRepo, s storage.SessionRepo) *gin.Engine {
	return newQuestionRouterWithEmbedder(role, userID, q, s, nil)
}

func newQuestionRouterWithEmbedder(role, userID string, q storage.QuestionRepo, s storage.SessionRepo, emb pipeline.AIEmbedder) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewQuestionHandler(q, s, emb)
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	r.GET("/api/v1/questions", h.List)
	r.GET("/api/v1/questions/:id", h.Get)
	r.PATCH("/api/v1/questions/:id", h.Update)
	r.POST("/api/v1/questions", h.Create)
	return r
}
```

Add the `pipeline` import to the test file's import block:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

- [ ] **Step 2: Change the `QuestionHandler` constructor + struct (so the package compiles with the updated test helper)**

In `internal/httpapi/handlers/questions.go`:

Add `pipeline` to the import block:

```go
import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

Change the struct + constructor (currently lines 22-30):

```go
type QuestionHandler struct {
	questions storage.QuestionRepo
	sessions  storage.SessionRepo
}

func NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo) *QuestionHandler {
	return &QuestionHandler{questions: questions, sessions: sessions}
}
```

to:

```go
type QuestionHandler struct {
	questions storage.QuestionRepo
	sessions  storage.SessionRepo
	embedder  pipeline.AIEmbedder
}

func NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo, embedder pipeline.AIEmbedder) *QuestionHandler {
	return &QuestionHandler{questions: questions, sessions: sessions, embedder: embedder}
}
```

- [ ] **Step 3: Build the package — everything except `Create` should compile**

Run: `go build ./internal/httpapi/handlers/`
Expected: no output (success). The POST route in the test helper references `h.Create` which doesn't exist yet, so the **test** won't compile yet — that's expected; we only build the non-test package here. If even this fails, re-check the import + constructor edits.

- [ ] **Step 4: Write the failing `Create` handler tests**

Append to `internal/httpapi/handlers/questions_test.go` (after the existing tests):

```go
func TestCreate_ExpertSuccess201Verified(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{
				Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified},
			}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"What is 2+2?",
		"choices":["3","4","5"],
		"answers":["4"],
		"tags":["math"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if q.createArg == nil {
		t.Fatal("Create was not called")
	}
	if q.createArg.Status != domain.QuestionStatusVerified {
		t.Errorf("status: got %q want verified", q.createArg.Status)
	}
	if q.createArg.Number != 0 {
		t.Errorf("number: got %d want 0", q.createArg.Number)
	}
	if q.createArg.ChoiceLabeling != domain.ChoiceLabelingLetter {
		t.Errorf("default choice_labeling: got %q want letter", q.createArg.ChoiceLabeling)
	}
	if q.createArg.Confidence != 0.99 {
		t.Errorf("default confidence: got %v want 0.99", q.createArg.Confidence)
	}
	if q.createArg.VerifiedBy == nil || *q.createArg.VerifiedBy != "e1" {
		t.Errorf("verified_by: got %v want e1", q.createArg.VerifiedBy)
	}
	if q.createArg.VerifiedAt == nil {
		t.Error("verified_at must be set")
	}
	// tags: req tags + manual-entry, and NO ai-generated
	gotManual, gotAI := false, false
	for _, tg := range q.createArg.Tags {
		if tg == "manual-entry" {
			gotManual = true
		}
		if tg == "ai-generated" {
			gotAI = true
		}
	}
	if !gotManual {
		t.Errorf("manual-entry tag missing: %v", q.createArg.Tags)
	}
	if gotAI {
		t.Errorf("ai-generated must NOT be injected on manual path: %v", q.createArg.Tags)
	}
}

func TestCreate_DuplicateHashReturns409WithQuestionID(t *testing.T) {
	q := &fakeQuestionRepo{
		findExact: func(context.Context, string) (*domain.Question, error) {
			return &domain.Question{ID: "existing-id"}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"dup","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d want 409: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Code       string `json:"code"`
			QuestionID string `json:"question_id"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error.Code != "duplicate" {
		t.Errorf("code: got %q want duplicate", body.Error.Code)
	}
	if body.Error.QuestionID != "existing-id" {
		t.Errorf("question_id: got %q want existing-id", body.Error.QuestionID)
	}
	if q.createArg != nil {
		t.Error("Create must not be called on duplicate")
	}
}

func TestCreate_EmbedderFailureStillCreates201(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	emb := &fakeEmbedder{embed: func(context.Context, string) ([]float32, error) {
		return nil, errors.New("embedder down")
	}}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, emb)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201 (embed failure must not fail request): %s", w.Code, w.Body.String())
	}
	if q.createArg == nil || q.createArg.Embedding != nil {
		t.Error("question created without embedding on embedder failure")
	}
}

func TestCreate_EmbedderConfiguredAttachesEmbedding(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, &fakeEmbedder{})
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if q.createArg == nil || len(q.createArg.Embedding) == 0 {
		t.Error("embedding must be attached when embedder succeeds")
	}
}

func TestCreate_InvalidChoiceLabeling400(t *testing.T) {
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","choices":["a","b"],"answers":["a"],"choice_labeling":"emoji"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestCreate_ConfidenceOutOfRange400(t *testing.T) {
	bad := 1.5
	body := `{"question":"q","choices":["a","b"],"answers":["a"],"confidence":` + jsonFloat(bad) + `}`
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 for confidence 1.5", w.Code)
	}
}

func TestCreate_MissingRequiredFields400(t *testing.T) {
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	// choices has only 1 element (< min=2)
	w := doReq(t, r, "POST", "/api/v1/questions", `{"question":"q","choices":["only"],"answers":["a"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 (choices min=2)", w.Code)
	}
}

// jsonFloat formats a float for inline JSON in table-ish tests.
func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
```

- [ ] **Step 5: Run the new tests to verify they fail (no Create method)**

Run: `go test ./internal/httpapi/handlers/ -run TestCreate -v`
Expected: compile failure — `h.Create undefined`.

- [ ] **Step 6: Implement the `Create` handler method**

Append to `internal/httpapi/handlers/questions.go` (after the `Update` method, before `toUserResponse`):

```go
// Create — POST /api/v1/questions. Expert only (RoleGuard enforces 403 at the route).
// Hand-authors a canonical verified question, bypassing the image pipeline (spec §3.2).
func (h *QuestionHandler) Create(c *gin.Context) {
	var req dto.CreateQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	if req.ChoiceLabeling != "" &&
		req.ChoiceLabeling != domain.ChoiceLabelingLetter &&
		req.ChoiceLabeling != domain.ChoiceLabelingNumber {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	confidence := 0.99
	if req.Confidence != nil {
		if *req.Confidence < 0 || *req.Confidence > 1 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		confidence = *req.Confidence
	}
	choiceLabeling := req.ChoiceLabeling
	if choiceLabeling == "" {
		choiceLabeling = domain.ChoiceLabelingLetter
	}

	norm := domain.NormalizeQuestion(req.Question)
	hash := domain.HashQuestion(norm)

	// Exact-hash dedup. On hit, return the existing id inline so the expert can PATCH it.
	existing, err := h.questions.FindExact(c.Request.Context(), hash)
	if err != nil {
		slog.Error("manual question exact dedup failed",
			"request_id", c.GetString("request_id"), "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	if existing != nil {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":        "duplicate",
			"message":     "question already exists",
			"question_id": existing.ID,
		}})
		return
	}

	// Best-effort embedding — never fails the request. Skipped entirely when unconfigured.
	var embedding []float32
	if h.embedder != nil {
		emb, embErr := h.embedder.Embed(c.Request.Context(), req.Question)
		if embErr != nil {
			slog.Error("manual question embedder failed",
				"request_id", c.GetString("request_id"), "error", embErr)
		} else {
			embedding = emb
		}
	}

	expertID := c.GetString("user_id")
	now := time.Now().UTC().Format(time.RFC3339)
	// tags = req.Tags + ["manual-entry"]; copy to avoid aliasing req.Tags.
	tags := make([]string, 0, len(req.Tags)+1)
	tags = append(tags, req.Tags...)
	tags = append(tags, "manual-entry")

	q := &domain.Question{
		Number:          0,
		Text:            req.Question,
		TextNorm:        norm,
		TextHash:        hash,
		MultipleCorrect: req.MultipleCorrect,
		Choices:         req.Choices,
		Answers:         req.Answers,
		ChoiceLabeling:  choiceLabeling,
		Confidence:      confidence,
		Explanation:     req.Explanation,
		Embedding:       embedding,
		Status:          domain.QuestionStatusVerified,
		VerifiedAt:      &now,
		VerifiedBy:      &expertID,
		Tags:            tags,
	}
	id, err := h.questions.Create(c.Request.Context(), q)
	if err != nil {
		slog.Error("manual question create failed",
			"expert_id", expertID, "request_id", c.GetString("request_id"), "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		slog.Warn("manual question re-fetch failed", "question_id", id, "err", err)
		c.JSON(http.StatusCreated, gin.H{"id": id, "status": domain.QuestionStatusVerified})
		return
	}
	c.JSON(http.StatusCreated, toExpertResponse(ev))
}
```

- [ ] **Step 7: Run the Create tests — verify they pass**

Run: `go test ./internal/httpapi/handlers/ -run TestCreate -v`
Expected: PASS — all 7 Create tests green.

- [ ] **Step 8: Run the full handlers package — existing tests still pass**

Run: `go test ./internal/httpapi/handlers/ -v`
Expected: PASS — all existing List/Get/Update tests + new Create tests green. (The constructor change is covered because `newQuestionRouter` passes `nil` embedder.)

- [ ] **Step 9: Vet the handlers package**

Run: `go vet ./internal/httpapi/handlers/`
Expected: no output (success).

- [ ] **Step 10: Commit**

```bash
git add internal/httpapi/handlers/questions.go internal/httpapi/handlers/questions_test.go
git commit -m "feat(questions): add expert POST /questions Create handler with exact-dedup + best-effort embed"
```

---

## Task 6: Feature 1 wiring — `NewServer` gains embedder + POST route + `wire.go`

**Files:**
- Modify: `internal/httpapi/server.go`
- Modify: `internal/app/wire.go`

`NewServer` is called only from `wire.go` (the existing `server_test.go` builds standalone gin engines, not `NewServer`). `registerRoutes` builds `NewQuestionHandler` and must pass the embedder; store it on the `Server` struct.

- [ ] **Step 1: Add `embedder` to the `Server` struct + `NewServer` signature**

In `internal/httpapi/server.go`, add the `pipeline` import:

```go
import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

Change the `Server` struct (add `embedder pipeline.AIEmbedder` field) and the `NewServer` signature (add `embedder pipeline.AIEmbedder` param) + the struct literal. The struct + constructor become:

```go
type Server struct {
	router       *gin.Engine
	userRepo     storage.UserRepo
	sessionRepo  storage.SessionRepo
	imageRepo    storage.ImageRepo
	questionRepo storage.QuestionRepo
	jobQueue     storage.JobQueue
	jwtMgr       *auth.JWTManager
	pool         *pgxpool.Pool
	uploadCfg    config.UploadConfig
	embedder     pipeline.AIEmbedder
}

func NewServer(
	userRepo storage.UserRepo,
	sessionRepo storage.SessionRepo,
	imageRepo storage.ImageRepo,
	questionRepo storage.QuestionRepo,
	jobQueue storage.JobQueue,
	jwtMgr *auth.JWTManager,
	pool *pgxpool.Pool,
	uploadCfg config.UploadConfig,
	embedder pipeline.AIEmbedder,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())

	s := &Server{
		router: r, userRepo: userRepo, sessionRepo: sessionRepo,
		imageRepo: imageRepo, questionRepo: questionRepo, jobQueue: jobQueue,
		jwtMgr: jwtMgr, pool: pool, uploadCfg: uploadCfg, embedder: embedder,
	}
	s.registerRoutes()
	return s
}
```

- [ ] **Step 2: Pass the embedder to `NewQuestionHandler` + register the POST route**

In `registerRoutes`, change the `NewQuestionHandler` call and add the POST route. Replace (currently lines 86-92):

```go
		questionHandler := handlers.NewQuestionHandler(s.questionRepo, s.sessionRepo)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.PATCH("/:id", RoleGuard("expert"), questionHandler.Update)
		}
```

with:

```go
		questionHandler := handlers.NewQuestionHandler(s.questionRepo, s.sessionRepo, s.embedder)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.POST("", RoleGuard("expert"), questionHandler.Create)
			questions.PATCH("/:id", RoleGuard("expert"), questionHandler.Update)
		}
```

- [ ] **Step 3: Thread the embedder through `wire.go`**

In `internal/app/wire.go`, change the `NewServer` call (currently lines 55-58):

```go
	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)
```

to:

```go
	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload, emb,
	)
```

(`emb` is the `pipeline.AIEmbedder` variable already declared at wire.go:67-73 — it's `nil` when the embedder is unconfigured. It is currently passed to `NewPipeline` after this point, so it's in scope. Note: `emb` is declared *after* the `NewServer` call in the current file, so **also move the `emb` declaration block above the `NewServer` call** — see Step 4.)

- [ ] **Step 4: Move the `emb` declaration above `NewServer`**

In `internal/app/wire.go`, the embedder block currently sits at lines 67-73 (after `NewServer`). Move it to **before** the `NewServer` call, so `emb` is in scope. Reorder so the relevant section reads:

```go
	userRepo := postgres.NewUserRepo(pool)
	sessionRepo := postgres.NewSessionRepo(pool)
	imageRepo := postgres.NewImageRepo(pool)
	questionRepo := postgres.NewQuestionRepo(pool)
	jobQueue := postgres.NewJobQueue(pool)
	jwtMgr := auth.NewJWTManager(cfg.JWT)

	// Embedder is optional — skip when no API key is configured.
	var emb pipeline.AIEmbedder
	if cfg.AI.Embedder.APIKey != "" {
		emb = embedder.New(cfg.AI.Embedder, log)
		log.Info("embedder enabled", "model", cfg.AI.Embedder.Model)
	} else {
		log.Info("embedder disabled — semantic dedup skipped (set COEUS_AI_EMBEDDER_API_KEY to enable)")
	}

	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload, emb,
	)

	vips.Startup(nil)

	enh := enhancer.New(log)
	ext := extractor.New(cfg.AI.Vision, log)
	ver := verifier.New(cfg.AI.Reviewer, log)

	pip := pipeline.NewPipeline(imageRepo, questionRepo, jobQueue,
		enh, ext, ver, emb, cfg.Pipeline, log)

	wp := pipeline.NewWorkerPool(jobQueue, pip,
		cfg.Workers, cfg.Pipeline, cfg.Postgres.DSN, log)
```

(The `vips.Startup(nil)` / enh / ext / ver block stays where it was — only the `emb` block moves above `NewServer`.)

- [ ] **Step 5: Full build + vet (needs CGO+libvips per AGENTS.md)**

Run: `go build ./... && go vet ./...`
Expected: no output (success). If libvips isn't installed, fall back to `go build ./internal/app/ ./internal/httpapi/ ./internal/pipeline/ ./internal/storage/... ./internal/config ./internal/domain` (skips the govips `enhancer` package) — but the production binary needs libvips anyway, so prefer installing it.

- [ ] **Step 6: Add a route-level 403 test for the new POST route**

The non-expert rejection happens in `RoleGuard` middleware before the handler runs (spec §3.7). Mirror the existing `TestQuestionPatch_RoleGuardRejectsUser` in `internal/httpapi/server_test.go`. Append:

```go
// TestQuestionPost_RoleGuardRejectsUser verifies the POST route is gated by
// RoleGuard("expert"): a user-role caller gets 403 at the middleware layer
// before the handler runs. Mirrors the route wiring in registerRoutes.
func TestQuestionPost_RoleGuardRejectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("user_id", "u1"); c.Next() })
	r.POST("/api/v1/questions", RoleGuard("expert"), func(c *gin.Context) {
		t.Error("handler must not run for user role")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(`{"question":"q","choices":["a","b"],"answers":["a"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("user post: got %d want 403", w.Code)
	}
}
```

(`net/http`, `net/http/httptest`, `strings`, `testing`, `gin` are already imported in `server_test.go`.)

- [ ] **Step 7: Run the httpapi package tests**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS — including the new POST 403 test and existing PATCH 403 test.

- [ ] **Step 8: Run the short test suite (no Docker)**

Run: `go test -short ./...`
Expected: PASS — all existing + new unit tests green. (Integration tests under `./internal/storage/postgres/` and `./internal/pipeline/` self-skip under `-short`.)

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/server_test.go internal/app/wire.go
git commit -m "feat(httpapi): wire embedder through NewServer; register expert POST /questions"
```

---

## Task 7: CORS config — struct + defaults + env overrides + validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config.yaml`
- Modify: `internal/config/config_test.go`

Add a `CORSConfig` sub-struct under `ServerConfig`, YAML defaults, two env overrides (`COEUS_CORS_ALLOWED_ORIGINS`, `COEUS_CORS_ALLOW_CREDENTIALS`), and startup validation rejecting `"*"` + `allow_credentials:true`.

- [ ] **Step 1: Write the failing validation test FIRST**

Open `internal/config/config_test.go` and read its existing style (table-driven or flat). Append a CORS validation test that matches the file's conventions. If the file is empty or has no `Validate` tests yet, add:

```go
func TestValidate_RejectsWildcardWithCredentials(t *testing.T) {
	cfg := &Config{
		Postgres: PostgresConfig{DSN: "x"},
		JWT:      JWTConfig{Secret: "x"},
		AI: AIConfig{
			Vision:   VisionConfig{APIKey: "x"},
			Reviewer: ReviewerConfig{APIKey: "x"},
		},
		Server: ServerConfig{CORS: CORSConfig{
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: wildcard origin + allow_credentials must error")
	}
}

func TestValidate_AllowsSpecificOriginWithCredentials(t *testing.T) {
	cfg := &Config{
		Postgres: PostgresConfig{DSN: "x"},
		JWT:      JWTConfig{Secret: "x"},
		AI: AIConfig{
			Vision:   VisionConfig{APIKey: "x"},
			Reviewer: ReviewerConfig{APIKey: "x"},
		},
		Server: ServerConfig{CORS: CORSConfig{
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowCredentials: true,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("specific origin + credentials should be valid: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: build failure — `unknown field 'CORS' in struct literal of type ServerConfig` (struct field doesn't exist yet) or PASS of the second case if the field compiles. Either way the wildcard case won't error yet.

- [ ] **Step 3: Add the `CORSConfig` struct + `ServerConfig.CORS` field**

In `internal/config/config.go`, change the `ServerConfig` struct (currently lines 27-32) and add the new struct. Replace:

```go
type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}
```

with:

```go
type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	CORS            CORSConfig    `yaml:"cors"`
}

// CORSConfig configures the gin-contrib/cors middleware (spec §4.2).
// Only AllowedOrigins and AllowCredentials get env overrides; the rest are
// stable enough to live in config.yaml.
type CORSConfig struct {
	AllowedOrigins   []string      `yaml:"allowed_origins"`
	AllowedMethods   []string      `yaml:"allowed_methods"`
	AllowedHeaders   []string      `yaml:"allowed_headers"`
	ExposeHeaders    []string      `yaml:"expose_headers"`
	AllowCredentials bool          `yaml:"allow_credentials"`
	MaxAge           time.Duration `yaml:"max_age"`
}
```

- [ ] **Step 4: Add env overrides to `applyEnvOverrides`**

In `internal/config/config.go`, inside `applyEnvOverrides` (before the final `return nil`), add:

```go
	if v := os.Getenv("COEUS_CORS_ALLOWED_ORIGINS"); v != "" {
		parts := strings.Split(v, ",")
		origins := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				origins = append(origins, t)
			}
		}
		cfg.Server.CORS.AllowedOrigins = origins
	}
	if v := os.Getenv("COEUS_CORS_ALLOW_CREDENTIALS"); v != "" {
		cfg.Server.CORS.AllowCredentials = (v == "true" || v == "1")
	}
```

(`strings` is already imported in config.go.)

- [ ] **Step 5: Add startup validation to `Validate`**

In `internal/config/config.go`, inside `Validate` (before the final `return nil`), add:

```go
	if c.Server.CORS.AllowCredentials {
		for _, o := range c.Server.CORS.AllowedOrigins {
			if o == "*" {
				return fmt.Errorf("server.cors: allow_credentials cannot be combined with wildcard origin \"*\" (set COEUS_CORS_ALLOWED_ORIGINS to specific origins)")
			}
		}
	}
```

- [ ] **Step 6: Add YAML defaults**

In `internal/config/config.yaml`, change the `server:` block (currently lines 1-5):

```yaml
server:
  addr: ":8080"
  read_timeout: 15s
  write_timeout: 120s
  shutdown_timeout: 30s
```

to:

```yaml
server:
  addr: ":8080"
  read_timeout: 15s
  write_timeout: 120s
  shutdown_timeout: 30s
  cors:
    allowed_origins: ["*"]
    allowed_methods: ["GET", "POST", "PATCH", "DELETE", "OPTIONS"]
    allowed_headers: ["Authorization", "Content-Type", "X-Request-Id"]
    expose_headers: ["X-Request-Id"]
    allow_credentials: false
    max_age: 12h
```

- [ ] **Step 7: Run the config tests — verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — new CORS validation tests + any existing config tests green.

- [ ] **Step 8: Vet the config package**

Run: `go vet ./internal/config/`
Expected: no output (success).

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/config.yaml internal/config/config_test.go
git commit -m "feat(config): add CORSConfig with yaml defaults, env overrides, wildcard+credentials validation"
```

---

## Task 8: CORS dependency + middleware wiring + tests

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get`)
- Modify: `internal/httpapi/server.go`
- Modify: `internal/app/wire.go`
- Create: `internal/httpapi/cors_test.go`

Pull in `gin-contrib/cors`, mount it in `NewServer` after `Recover`/`RequestLog` and before `registerRoutes` (which mounts `AuthMiddleware`), so OPTIONS preflight returns 204 with CORS headers rather than 401. `NewServer` gains a `corsCfg config.CORSConfig` param.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/gin-contrib/cors`
Expected: `go.mod` gains `github.com/gin-contrib/cors` and `go.sum` is updated. (Network access required.)

- [ ] **Step 2: Write the failing CORS middleware test FIRST**

Create `internal/httpapi/cors_test.go`. This builds a standalone gin engine mirroring `NewServer`'s middleware order (cors between the global middleware and the auth-protected group) — following the convention in `server_test.go`, which mirrors route wiring rather than constructing the real `NewServer` (that would need a DB pool). A 401 "sentinel" middleware proves cors short-circuits preflight before auth:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
)

// newCORSTestEngine mirrors NewServer's middleware order (cors mounted before the
// auth-protected group) without needing a DB pool. The sentinel enforces that any
// non-preflight request reaching /api/v1 without a token is rejected 401 — proving
// the cors preflight short-circuits BEFORE auth would run.
func newCORSTestEngine(cfg config.CORSConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     cfg.AllowedMethods,
		AllowHeaders:     cfg.AllowedHeaders,
		ExposeHeaders:    cfg.ExposeHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           cfg.MaxAge,
	}))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api := r.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized"}})
	})
	api.POST("/questions", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return r
}

func TestCORS_PreflightReturns204Not401(t *testing.T) {
	r := newCORSTestEngine(config.CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-Id"},
		ExposeHeaders:  []string{"X-Request-Id"},
		MaxAge:         12 * time.Hour,
	})
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/questions", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: got %d want 204 (must not reach auth sentinel -> 401)", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin: got %q want https://app.example.com", got)
	}
}

func TestCORS_HealthzEchoesOriginOnSimpleRequest(t *testing.T) {
	r := newCORSTestEngine(config.CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET"},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("healthz: got %d want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin on simple request: got %q want https://app.example.com", got)
	}
}
```

> These tests validate the cors *configuration shape* + ordering invariant independent of `NewServer` (following the `server_test.go` convention of mirroring route wiring rather than constructing the real `NewServer`, which would need a DB pool). They serve as a regression guard after Step 4 wires `NewServer`.

- [ ] **Step 3: Run the test to verify it fails (cors middleware not yet imported/wired in the test, or import resolves once `go get` ran)**

Run: `go test ./internal/httpapi/ -run TestCORS -v`
Expected: the test file compiles (cors dep is present from Step 1) and the tests PASS already, because the test engine mounts cors directly. These tests validate the cors *configuration shape* + ordering invariant independent of `NewServer`. (They will keep passing after Step 4 wires `NewServer`, serving as a regression guard.)

- [ ] **Step 4: Wire the cors middleware into `NewServer`**

In `internal/httpapi/server.go`, add the cors import:

```go
import (
	"net/http"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

Change `NewServer` to accept `corsCfg config.CORSConfig` and mount the middleware after `Recover`/`RequestLog`, before `registerRoutes`. The constructor becomes:

```go
func NewServer(
	userRepo storage.UserRepo,
	sessionRepo storage.SessionRepo,
	imageRepo storage.ImageRepo,
	questionRepo storage.QuestionRepo,
	jobQueue storage.JobQueue,
	jwtMgr *auth.JWTManager,
	pool *pgxpool.Pool,
	uploadCfg config.UploadConfig,
	embedder pipeline.AIEmbedder,
	corsCfg config.CORSConfig,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())
	r.Use(cors.New(cors.Config{
		AllowOrigins:     corsCfg.AllowedOrigins,
		AllowMethods:     corsCfg.AllowedMethods,
		AllowHeaders:     corsCfg.AllowedHeaders,
		ExposeHeaders:    corsCfg.ExposeHeaders,
		AllowCredentials: corsCfg.AllowCredentials,
		MaxAge:           corsCfg.MaxAge,
	}))

	s := &Server{
		router: r, userRepo: userRepo, sessionRepo: sessionRepo,
		imageRepo: imageRepo, questionRepo: questionRepo, jobQueue: jobQueue,
		jwtMgr: jwtMgr, pool: pool, uploadCfg: uploadCfg, embedder: embedder,
	}
	s.registerRoutes()
	return s
}
```

- [ ] **Step 5: Pass the CORS config through `wire.go`**

In `internal/app/wire.go`, add `cfg.Server.CORS` to the `NewServer` call:

```go
	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload, emb, cfg.Server.CORS,
	)
```

- [ ] **Step 6: Full build + vet (needs CGO+libvips)**

Run: `go build ./... && go vet ./...`
Expected: no output (success).

- [ ] **Step 7: Run the full short test suite**

Run: `go test -short ./...`
Expected: PASS — all unit tests green, including the new CORS tests.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/httpapi/server.go internal/app/wire.go internal/httpapi/cors_test.go
git commit -m "feat(cors): add gin-contrib/cors middleware wired before auth so OPTIONS preflight returns 204"
```

---

## Final Verification

After all 8 tasks:

- [ ] **Full build + vet:** `go build ./... && go vet ./...` — clean. (Needs CGO+libvips.)
- [ ] **Short test suite:** `go test -short ./...` — all green.
- [ ] **Integration (optional, needs Docker):** `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s` — existing integration tests pass (the refactor is behavior-preserving; migration 0003 applies cleanly).
- [ ] **Manual smoke (optional):** with env vars set and Postgres running, `go run ./cmd/coeus`, then:
  - `curl -i -X OPTIONS http://localhost:8080/api/v1/questions -H "Origin: https://app.example.com" -H "Access-Control-Request-Method: POST"` → `204` + `Access-Control-Allow-Origin`.
  - `POST /api/v1/questions` with an expert JWT → `201` with `status: "verified"` and `image_id: ""`.

---

## Spec Coverage Cross-Check

| Spec requirement | Task(s) |
|---|---|
| §3.2 handler flow (bind→validate→normalize/hash→dedup→embed→create→refetch→201) | Task 5 |
| §3.3 domain extraction (NormalizeQuestion/HashQuestion, no punctuation stripping) | Tasks 1, 2 |
| §3.4 `manual-entry` tag migration | Task 3 |
| §3.5 `CreateQuestionRequest` DTO | Task 4 |
| §3.6 constructor gains embedder (nil-safe); NewServer gains embedder; wire.go | Tasks 5, 6 |
| §3.6a `Create` INSERT persists verified_at/verified_by (backward-compatible) | Task 3 |
| §3.6a route `questions.POST("", RoleGuard("expert"), ...)` | Task 6 |
| §3.7 error handling (400 validation, 409 inline duplicate, embed swallowed, 500) | Task 5 |
| §3.8 invariants (manual-entry always, ai-generated never, status=verified, number=0, best-effort embed, image_id="") | Task 5 (verified by tests) |
| §4.1 `go get gin-contrib/cors` | Task 8 |
| §4.2 CORSConfig struct + YAML defaults | Task 7 |
| §4.3 env overrides (COEUS_CORS_ALLOWED_ORIGINS, COEUS_CORS_ALLOW_CREDENTIALS) | Task 7 |
| §4.4 startup validation (`*` + credentials rejected) | Task 7 |
| §4.5 middleware wired after Recover/RequestLog, before registerRoutes; ExposeHeaders included | Task 8 |
| §6 domain helper unit tests | Task 1 |
| §6 QuestionHandler.Create handler unit tests (201/409/embed-fail/non-expert via RoleGuard/validation) | Task 5 |
| §6 CORS config validation unit test | Task 7 |
| §6 CORS middleware httptest (preflight 204, origin echo) | Task 8 |
| §6 existing pipeline tests pass unchanged | Task 2 (Step 5) |
