# Coeus Plan 2: Sessions + Image Upload + Pipeline + Workers

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add session lifecycle API, image upload with async job creation, and the full pipeline/worker machinery (tested with fakes — real AI clients deferred to Plan 3).

**Architecture:** Plan 2 adds three layers: (1) HTTP handlers for sessions and image upload, (2) pipeline orchestration behind 4 narrow interface ports with full TDD coverage, (3) a worker pool with Postgres LISTEN/NOTIFY + reaper reclaim. SessionWindow middleware gates upload paths on session expiry. The pipeline and workers are tested with in-memory fakes and Testcontainers integration; real AI client implementations go into Plan 3.

**Tech Stack:** Go 1.26, Gin, PGX v5 (pgxpool + pgx.Conn), pgvector, crypto/sha256, encoding/json, image (stdlib), Testcontainers

---

## File Structure

```
internal/
  pipeline/                 # NEW package
    ports.go                # 4 AI interfaces + ExtractResult/VerifyResult types
    pipeline.go             # Pipeline struct + Run(ctx, job) 10-step orchestration
    pipeline_test.go        # 10 table-driven tests with in-memory fakes
    worker.go               # WorkerPool: LISTEN/NOTIFY + Claim + reaper
    worker_test.go          # Unit tests + Testcontainers integration
  httpapi/
    middleware.go           # MODIFY: add SessionWindow
    middleware_test.go      # NEW: SessionWindow tests
    server.go               # MODIFY: new deps + session/image routes
    dto/                    # NEW package
      requests.go           # CreateSessionRequest
      responses.go          # Session/Image response types
    handlers/
      sessions.go           # NEW: Create/List/Get/Close
      sessions_test.go      # NEW
      images.go             # NEW: Upload/List
      images_test.go        # NEW
  storage/
    ports.go                # MODIFY: add CountBySession, FindByImageID
    postgres/
      image_repo.go         # MODIFY: implement CountBySession
      image_repo_test.go    # MODIFY: add CountBySession test
      job_queue.go          # MODIFY: implement FindByImageID
      job_queue_test.go     # MODIFY: add FindByImageID test
  app/
    wire.go                 # MODIFY: create repos, pass to server
```

---

## Task 1: Pipeline Ports

**Files:**
- Create: `internal/pipeline/ports.go`

- [ ] **Step 1: Create the ports file with all interfaces and types**

```go
package pipeline

import "context"

// ImageEnhancer improves image quality before extraction (upscaler, denoiser, etc.).
// Implementations live in Plan 3 (real AI client); tests use fakes.
type ImageEnhancer interface {
	Enhance(ctx context.Context, original []byte, mime string) ([]byte, error)
}

// AIExtractor reads questions and answers from an enhanced exam image.
// Returns an ExtractResult — never a raw error for content-level failures
// (those are reported via ExtractResult.Error). Transport-level failures
// return a normal Go error.
type AIExtractor interface {
	Extract(ctx context.Context, image []byte, mime string) (ExtractResult, error)
}

// AIVerifier checks extracted answers against a second model.
// Failures are best-effort: the caller proceeds with unverified questions.
type AIVerifier interface {
	Verify(ctx context.Context, questions []ExtractedQuestion) (VerifyResult, error)
}

// AIEmbedder produces a vector embedding of question text for semantic dedup.
type AIEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Answer is a single choice or correct answer extracted from the image.
type Answer struct {
	ID   string // label like "A", "1", "i" — used for choice labeling inference
	Text string // the answer text — stored value-only in the DB
}

// ExtractedQuestion is one question parsed from the exam image.
type ExtractedQuestion struct {
	Number          int
	Text            string
	Choices         []Answer
	Answers         []Answer
	MultipleCorrect bool
}

// ExtractionErrorCode identifies why extraction failed or was partial.
const (
	ExtractionCodeUnreadableImage = "unreadable_image" // image too blurry, corrupted
	ExtractionCodeNoQuestions     = "no_questions_found"
	ExtractionCodePartial         = "partial_extraction" // some questions parsed, some not
	ExtractionCodeAIUnavailable   = "ai_unavailable"     // transport / service error
)

// ExtractionError is set on ExtractResult when the AI responded but could not
// fully process the image. A nil ExtractResult.Error means full success.
type ExtractionError struct {
	Code   string
	Detail string
}

// ExtractResult is the output of AIExtractor.Extract.
type ExtractResult struct {
	Questions []ExtractedQuestion
	Error     *ExtractionError // non-nil for partial or terminal failures
}

// VerifiedQuestion is the verification result for one question in the input slice.
// Index matches the position in the []ExtractedQuestion passed to Verify.
type VerifiedQuestion struct {
	Index       int
	Confidence  float64
	Explanation string
}

// VerificationSummary aggregates per-question verification results.
type VerificationSummary struct {
	Results []VerifiedQuestion
}

// VerifyResult wraps the verification summary.
type VerifyResult struct {
	Summary VerificationSummary
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/pipeline/`
Expected: no output (success)

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/ports.go
git commit -m "feat(pipeline): add AI port interfaces and result types"
```

---

## Task 2: Storage Additions

Two new methods needed by the HTTP layer: `ImageRepo.CountBySession` (for session detail view) and `JobQueue.FindByImageID` (for image list with job status).

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/image_repo.go`
- Modify: `internal/storage/postgres/image_repo_test.go`
- Modify: `internal/storage/postgres/job_queue.go`
- Modify: `internal/storage/postgres/job_queue_test.go`

### Part A: ImageRepo.CountBySession

- [ ] **Step 1: Write the failing test**

Add to `internal/storage/postgres/image_repo_test.go`:

```go
func TestImageRepo_CountBySession(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "count@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	imgRepo.Create(ctx, sess.ID, []byte("a"), "image/jpeg", 1, 1)
	imgRepo.Create(ctx, sess.ID, []byte("b"), "image/jpeg", 1, 1)
	imgRepo.Create(ctx, sess.ID, []byte("c"), "image/jpeg", 1, 1)

	count, err := imgRepo.CountBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("CountBySession: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestImageRepo_CountBySession -v`
Expected: compile error — `imgRepo.CountBySession undefined`

- [ ] **Step 3: Add CountBySession to the ImageRepo interface**

In `internal/storage/ports.go`, replace the entire `ImageRepo` interface block with:

```go
// ImageRepo manages uploaded image records.
type ImageRepo interface {
	Create(ctx context.Context, sessionID string, original []byte, mime string, width, height int) (string, error)
	FindByID(ctx context.Context, id string) (*domain.Image, error)
	ListBySession(ctx context.Context, sessionID string) ([]*domain.Image, error)
	UpdateEnhanced(ctx context.Context, id string, enhanced []byte) error
	UpdateVerificationReport(ctx context.Context, id string, report []byte) error
	UpdateExtractionError(ctx context.Context, id string, errJSON []byte) error
	CleanBytes(ctx context.Context, id string) error
	CountBySession(ctx context.Context, sessionID string) (int, error)
}
```

- [ ] **Step 4: Implement CountBySession in postgres ImageRepo**

Add at the end of `internal/storage/postgres/image_repo.go` (after `CleanBytes`):

```go
func (r *ImageRepo) CountBySession(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM images WHERE session_id = $1`, sessionID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count images by session: %w", err)
	}
	return count, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestImageRepo_CountBySession -v`
Expected: `PASS` (after Testcontainers spins up)

### Part B: JobQueue.FindByImageID

- [ ] **Step 6: Write the failing test**

Add to `internal/storage/postgres/job_queue_test.go`:

```go
func TestJobQueue_FindByImageID(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "findjb@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jobID, _ := jq.Enqueue(ctx, imgID, sess.ID)

	job, err := jq.FindByImageID(ctx, imgID)
	if err != nil {
		t.Fatalf("FindByImageID: %v", err)
	}
	if job == nil || job.ID != jobID {
		t.Fatalf("expected job %s, got %v", jobID, job)
	}
	if job.Status != domain.JobStatusPending {
		t.Errorf("status = %q, want pending", job.Status)
	}

	// Not found returns nil, nil
	job2, err := jq.FindByImageID(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("FindByImageID miss: %v", err)
	}
	if job2 != nil {
		t.Error("expected nil for non-existent image")
	}
}
```

- [ ] **Step 7: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue_FindByImageID -v`
Expected: compile error — `jq.FindByImageID undefined`

- [ ] **Step 8: Add FindByImageID to the JobQueue interface**

In `internal/storage/ports.go`, replace the entire `JobQueue` interface block with:

```go
// JobQueue manages the Postgres-backed job queue.
type JobQueue interface {
	Enqueue(ctx context.Context, imageID, sessionID string) (string, error)
	Claim(ctx context.Context) (*domain.Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id, errMsg string) error
	ReaperReclaim(ctx context.Context, staleThreshold time.Duration) (int, error)
	FindByImageID(ctx context.Context, imageID string) (*domain.Job, error)
}
```

- [ ] **Step 9: Implement FindByImageID in postgres JobQueue**

Add at the end of `internal/storage/postgres/job_queue.go` (after `ReaperReclaim`):

```go
func (q *JobQueue) FindByImageID(ctx context.Context, imageID string) (*domain.Job, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, image_id, session_id, status, attempts,
		       COALESCE(last_error, ''),
		       to_char(queued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       COALESCE(to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
		       COALESCE(to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
		FROM jobs WHERE image_id = $1 ORDER BY queued_at DESC LIMIT 1
	`, imageID)

	var job domain.Job
	err := row.Scan(&job.ID, &job.ImageID, &job.SessionID, &job.Status,
		&job.Attempts, &job.LastError, &job.QueuedAt, &job.StartedAt, &job.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find job by image: %w", err)
	}
	return &job, nil
}
```

- [ ] **Step 10: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue_FindByImageID -v`
Expected: `PASS`

- [ ] **Step 11: Run full storage test suite to confirm no regressions**

Run: `go test ./internal/storage/postgres/ -v`
Expected: all tests `PASS`

- [ ] **Step 12: Commit**

```bash
git add internal/storage/ports.go internal/storage/postgres/image_repo.go internal/storage/postgres/image_repo_test.go internal/storage/postgres/job_queue.go internal/storage/postgres/job_queue_test.go
git commit -m "feat(storage): add CountBySession and FindByImageID methods"
```

---

## Task 3: Pipeline Orchestration

The Pipeline struct orchestrates the full 10-step per-job workflow: load image → enhance → extract (with retries) → normalize/hash/embed/dedup per question → create or link → verify → complete job. Panic recovery calls Fail.

**Files:**
- Create: `internal/pipeline/pipeline.go`

- [ ] **Step 1: Create the pipeline implementation**

```go
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// Pipeline orchestrates the per-job extraction → dedup → verify workflow.
type Pipeline struct {
	images    storage.ImageRepo
	questions storage.QuestionRepo
	jobs      storage.JobQueue
	enhancer  ImageEnhancer
	extractor AIExtractor
	verifier  AIVerifier
	embedder  AIEmbedder
	cfg       config.PipelineConfig
	log       *slog.Logger
}

func NewPipeline(
	images storage.ImageRepo,
	questions storage.QuestionRepo,
	jobs storage.JobQueue,
	enhancer ImageEnhancer,
	extractor AIExtractor,
	verifier AIVerifier,
	embedder AIEmbedder,
	cfg config.PipelineConfig,
	log *slog.Logger,
) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		images: images, questions: questions, jobs: jobs,
		enhancer: enhancer, extractor: extractor, verifier: verifier, embedder: embedder,
		cfg: cfg, log: log,
	}
}

// Run executes the pipeline for a single job. It owns the job lifecycle:
// calls Complete on success, Fail on infra errors. On shutdown (ctx canceled)
// it returns the error WITHOUT completing or failing — the reaper reclaims.
func (p *Pipeline) Run(ctx context.Context, job *domain.Job) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("pipeline panic: %v", r)
			p.log.Error("pipeline panic", "job", job.ID, "error", retErr)
			_ = p.jobs.Fail(context.Background(), job.ID, retErr.Error())
		}
	}()

	if err := p.execute(ctx, job); err != nil {
		if ctx.Err() == nil {
			_ = p.jobs.Fail(ctx, job.ID, err.Error())
		}
		return err
	}

	if err := p.jobs.Complete(ctx, job.ID); err != nil {
		if ctx.Err() == nil {
			_ = p.jobs.Fail(ctx, job.ID, "complete failed: "+err.Error())
		}
		return err
	}
	return nil
}

// execute runs the 10-step workflow. Returns nil on success (including
// extraction-failure cases where a placeholder question was created).
// Returns non-nil only for infrastructure errors or shutdown.
func (p *Pipeline) execute(ctx context.Context, job *domain.Job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Step 1: Load image bytes
	img, err := p.images.FindByID(ctx, job.ImageID)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}

	// Step 2: Enhance (best-effort — fall back to original on failure)
	decodeBytes := img.Original
	if enhanced, err := p.enhancer.Enhance(ctx, img.Original, img.Mime); err != nil {
		p.log.Warn("enhance failed, using original", "image", img.ID, "error", err)
	} else if len(enhanced) > 0 {
		if err := p.images.UpdateEnhanced(ctx, img.ID, enhanced); err != nil {
			return fmt.Errorf("persist enhanced: %w", err)
		}
		decodeBytes = enhanced
	}

	// Step 3: Extract with retries
	result, err := p.extractWithRetries(ctx, decodeBytes, img.Mime)
	if err != nil {
		if ctx.Err() != nil {
			return err // shutdown — reaper reclaims
		}
		// AI unavailable after all retries — create placeholder, complete job
		p.handleExtractionFailure(ctx, job, img, ExtractionCodeAIUnavailable, err.Error())
		return nil
	}

	// Terminal extraction failure (no questions after all retries)
	if len(result.Questions) == 0 {
		code := "extraction_failed"
		detail := "no questions extracted"
		if result.Error != nil {
			code = result.Error.Code
			detail = result.Error.Detail
		}
		p.handleExtractionFailure(ctx, job, img, code, detail)
		return nil
	}

	// Step 4: Process each extracted question
	//   4a normalize, 4b hash, 4c exact dedup, 4d embed, 4e semantic dedup, 4f create/link
	type newQuestion struct {
		id  string
		ext ExtractedQuestion
	}
	var newQs []newQuestion
	for _, eq := range result.Questions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		norm := normalizeQuestion(eq.Text)
		hash := sha256String(norm)

		// 4c: Exact dedup
		existing, err := p.questions.FindExact(ctx, hash)
		if err != nil {
			return fmt.Errorf("exact dedup: %w", err)
		}
		if existing != nil {
			if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, existing.ID, eq.Number, 0); err != nil {
				return fmt.Errorf("link exact dedup: %w", err)
			}
			continue
		}

		// 4d: Embed (best-effort)
		var embedding []float32
		if emb, err := p.embedder.Embed(ctx, eq.Text); err != nil {
			p.log.Warn("embed failed, skipping semantic dedup", "image", img.ID, "error", err)
		} else {
			embedding = emb
		}

		// 4e: Semantic dedup (only if embedding succeeded)
		if embedding != nil {
			existing, err = p.questions.FindSemantic(ctx, embedding, p.cfg.SemanticThreshold)
			if err != nil {
				return fmt.Errorf("semantic dedup: %w", err)
			}
			if existing != nil {
				if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, existing.ID, eq.Number, 0); err != nil {
					return fmt.Errorf("link semantic dedup: %w", err)
				}
				continue
			}
		}

		// 4f: Create new canonical question
		q := &domain.Question{
			Number:          eq.Number,
			Text:            eq.Text,
			TextNorm:        norm,
			TextHash:        hash,
			MultipleCorrect: eq.MultipleCorrect,
			Choices:         answerTexts(eq.Choices),
			Answers:         answerTexts(eq.Answers),
			ChoiceLabeling:  domain.InferChoiceLabeling(answerIDs(eq.Choices)),
			Status:          domain.QuestionStatusModeration,
			Embedding:       embedding,
			Tags:            []string{"ai-generated"},
		}
		id, err := p.questions.Create(ctx, q)
		if err != nil {
			return fmt.Errorf("create question: %w", err)
		}
		if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, id, eq.Number, 0); err != nil {
			return fmt.Errorf("link new question: %w", err)
		}
		newQs = append(newQs, newQuestion{id: id, ext: eq})
	}

	// Step 5: Verify new questions (best-effort — skip if all deduped)
	if len(newQs) > 0 {
		toVerify := make([]ExtractedQuestion, len(newQs))
		for i, nq := range newQs {
			toVerify[i] = nq.ext
		}
		vr, err := p.verifier.Verify(ctx, toVerify)
		if err != nil {
			p.log.Warn("verify failed, questions stay in moderation", "image", img.ID, "error", err)
		} else {
			for _, vq := range vr.Summary.Results {
				if vq.Index >= 0 && vq.Index < len(newQs) {
					nq := newQs[vq.Index]
					if err := p.questions.UpdateFromVerification(ctx, nq.id, vq.Confidence, vq.Explanation); err != nil {
						p.log.Warn("update from verification", "question", nq.id, "error", err)
					}
				}
			}
		}
	}

	return nil
}

// extractWithRetries calls Extract up to ExtractMaxAttempts times.
// Retries on unreadable_image, no_questions_found, and transport errors.
// partial_extraction and unknown codes are terminal (no retry).
func (p *Pipeline) extractWithRetries(ctx context.Context, image []byte, mime string) (ExtractResult, error) {
	var result ExtractResult
	var lastErr error
	for attempt := 1; attempt <= p.cfg.ExtractMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result, lastErr = p.extractor.Extract(ctx, image, mime)
		if lastErr == nil && result.Error == nil {
			return result, nil
		}
		if lastErr == nil && result.Error != nil {
			switch result.Error.Code {
			case ExtractionCodePartial:
				return result, nil
			case ExtractionCodeUnreadableImage, ExtractionCodeNoQuestions:
				p.log.Warn("extract retryable failure", "attempt", attempt, "code", result.Error.Code)
				continue
			default:
				return result, nil
			}
		}
		p.log.Warn("extract error", "attempt", attempt, "error", lastErr)
	}
	if lastErr != nil {
		return result, lastErr
	}
	return result, nil
}

// handleExtractionFailure stores the error on the image and creates an
// error-placeholder question so the client can see what went wrong.
func (p *Pipeline) handleExtractionFailure(ctx context.Context, job *domain.Job, img *domain.Image, code, detail string) {
	errJSON, _ := json.Marshal(map[string]string{"code": code, "detail": detail})
	if err := p.images.UpdateExtractionError(ctx, img.ID, errJSON); err != nil {
		p.log.Error("store extraction error", "image", img.ID, "error", err)
	}

	hash := sha256String("error:" + img.ID)
	q := &domain.Question{
		Number:   0,
		Text:     "Extraction failed: " + code,
		TextNorm: "extraction failed " + code,
		TextHash: hash,
		Status:   domain.QuestionStatusError,
		Tags:     []string{"extraction-failed"},
	}
	id, err := p.questions.Create(ctx, q)
	if err != nil {
		p.log.Error("create error placeholder", "image", img.ID, "error", err)
		return
	}
	if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, id, 0, 0); err != nil {
		p.log.Error("link error placeholder", "image", img.ID, "error", err)
	}
}

func normalizeQuestion(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func sha256String(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func answerTexts(answers []Answer) []string {
	out := make([]string, len(answers))
	for i, a := range answers {
		out[i] = a.Text
	}
	return out
}

func answerIDs(answers []Answer) []string {
	out := make([]string, len(answers))
	for i, a := range answers {
		out[i] = a.ID
	}
	return out
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/pipeline/`
Expected: no output (success)

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/pipeline.go
git commit -m "feat(pipeline): implement 10-step pipeline orchestration"
```

---

## Task 4: Pipeline Unit Tests

10 table-driven tests covering the full pipeline lifecycle using in-memory fakes for all 4 AI ports and all 3 storage interfaces.

**Files:**
- Create: `internal/pipeline/pipeline_test.go`

- [ ] **Step 1: Create the test file with fakes and test cases**

```go
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// quietLogger discards all output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Fakes ---

type fakeEnhancer struct {
	enhanced []byte
	err      error
	called   bool
}

func (f *fakeEnhancer) Enhance(_ context.Context, data []byte, _ string) ([]byte, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	if f.enhanced != nil {
		return f.enhanced, nil
	}
	return data, nil
}

type fakeExtractor struct {
	result ExtractResult
	err    error
	calls  int
}

func (f *fakeExtractor) Extract(ctx context.Context, _ []byte, _ string) (ExtractResult, error) {
	f.calls++
	if ctx.Err() != nil {
		return ExtractResult{}, ctx.Err()
	}
	return f.result, f.err
}

type fakeVerifier struct {
	result VerifyResult
	err    error
	called bool
}

func (f *fakeVerifier) Verify(_ context.Context, _ []ExtractedQuestion) (VerifyResult, error) {
	f.called = true
	return f.result, f.err
}

type fakeEmbedder struct {
	embedding []float32
	err       error
	called    bool
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	f.called = true
	return f.embedding, f.err
}

// --- Fake repos (full interface implementations) ---

type fakeImageRepo struct {
	images     map[string]*domain.Image
	enhanced   map[string][]byte
	extractErr map[string][]byte
	nextID     int
}

func newFakeImageRepo(img *domain.Image) *fakeImageRepo {
	r := &fakeImageRepo{
		images:     make(map[string]*domain.Image),
		enhanced:   make(map[string][]byte),
		extractErr: make(map[string][]byte),
	}
	if img != nil {
		r.images[img.ID] = img
	}
	return r
}

func (r *fakeImageRepo) Create(_ context.Context, sessionID string, original []byte, mime string, w, h int) (string, error) {
	r.nextID++
	id := fmt.Sprintf("img-%d", r.nextID)
	r.images[id] = &domain.Image{ID: id, SessionID: sessionID, Original: original, Mime: mime, Width: w, Height: h}
	return id, nil
}
func (r *fakeImageRepo) FindByID(_ context.Context, id string) (*domain.Image, error) {
	img, ok := r.images[id]
	if !ok {
		return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
	}
	return img, nil
}
func (r *fakeImageRepo) ListBySession(_ context.Context, sessionID string) ([]*domain.Image, error) {
	var out []*domain.Image
	for _, img := range r.images {
		if img.SessionID == sessionID {
			out = append(out, img)
		}
	}
	return out, nil
}
func (r *fakeImageRepo) UpdateEnhanced(_ context.Context, id string, enhanced []byte) error {
	r.enhanced[id] = enhanced
	if img, ok := r.images[id]; ok {
		img.Enhanced = enhanced
	}
	return nil
}
func (r *fakeImageRepo) UpdateVerificationReport(context.Context, string, []byte) error { return nil }
func (r *fakeImageRepo) UpdateExtractionError(_ context.Context, id string, errJSON []byte) error {
	r.extractErr[id] = errJSON
	return nil
}
func (r *fakeImageRepo) CleanBytes(context.Context, string) error { return nil }
func (r *fakeImageRepo) CountBySession(_ context.Context, sessionID string) (int, error) {
	c := 0
	for _, img := range r.images {
		if img.SessionID == sessionID {
			c++
		}
	}
	return c, nil
}

type fakeQuestionRepo struct {
	byHash          map[string]*domain.Question // hash → question
	semantMatch     *domain.Question            // returned by FindSemantic if non-nil
	created         []*domain.Question
	updatedFromVer  []struct{ id string; conf float64; expl string }
	links           []struct{ sessionID, imageID, questionID string; num int; conf float64 }
	findExactCalls  int
	findSemantCalls int
	nextID          int
}

func newFakeQuestionRepo() *fakeQuestionRepo {
	return &fakeQuestionRepo{byHash: make(map[string]*domain.Question)}
}

func (r *fakeQuestionRepo) Create(_ context.Context, q *domain.Question) (string, error) {
	r.nextID++
	id := fmt.Sprintf("q-%d", r.nextID)
	q.ID = id
	r.created = append(r.created, q)
	r.byHash[q.TextHash] = q
	return id, nil
}
func (r *fakeQuestionRepo) FindByID(_ context.Context, id string) (*domain.Question, error) {
	for _, q := range r.byHash {
		if q.ID == id {
			return q, nil
		}
	}
	return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
}
func (r *fakeQuestionRepo) FindExact(_ context.Context, hash string) (*domain.Question, error) {
	r.findExactCalls++
	return r.byHash[hash], nil
}
func (r *fakeQuestionRepo) FindSemantic(_ context.Context, _ []float32, _ float64) (*domain.Question, error) {
	r.findSemantCalls++
	return r.semantMatch, nil
}
func (r *fakeQuestionRepo) UpdateFromVerification(_ context.Context, id string, c float64, e string) error {
	r.updatedFromVer = append(r.updatedFromVer, struct{ id string; conf float64; expl string }{id, c, e})
	return nil
}
func (r *fakeQuestionRepo) ListForUser(context.Context, string, string, int, int) ([]*storage.QuestionWithSession, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) UpdateByExpert(context.Context, string, []string, []string, string, float64, []string, string) error {
	return nil
}
func (r *fakeQuestionRepo) CountUnresolvedForImage(context.Context, string) (int, error) { return 0, nil }
func (r *fakeQuestionRepo) LinkToSession(_ context.Context, sessionID, imageID, questionID string, num int, conf float64) error {
	r.links = append(r.links, struct{ sessionID, imageID, questionID string; num int; conf float64 }{sessionID, imageID, questionID, num, conf})
	return nil
}

type fakeJobQueue struct {
	completed []string
	failed    []struct{ id, msg string }
}

func newFakeJobQueue() *fakeJobQueue { return &fakeJobQueue{} }

func (q *fakeJobQueue) Enqueue(context.Context, string, string) (string, error) { return "job-1", nil }
func (q *fakeJobQueue) Claim(context.Context) (*domain.Job, error)              { return nil, nil }
func (q *fakeJobQueue) Complete(_ context.Context, id string) error {
	q.completed = append(q.completed, id)
	return nil
}
func (q *fakeJobQueue) Fail(_ context.Context, id, msg string) error {
	q.failed = append(q.failed, struct{ id, msg string }{id, msg})
	return nil
}
func (q *fakeJobQueue) ReaperReclaim(context.Context, time.Duration) (int, error) { return 0, nil }
func (q *fakeJobQueue) FindByImageID(context.Context, string) (*domain.Job, error) {
	return nil, nil
}

// --- Helpers ---

func testPipeline(enh ImageEnhancer, ext AIExtractor, ver AIVerifier, emb AIEmbedder) (*Pipeline, *fakeImageRepo, *fakeQuestionRepo, *fakeJobQueue) {
	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	qRepo := newFakeQuestionRepo()
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, enh, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())
	return p, imgRepo, qRepo, jq
}

func sampleQuestions() []ExtractedQuestion {
	return []ExtractedQuestion{
		{Number: 1, Text: "What is 2+2?", Choices: []Answer{{"A", "3"}, {"B", "4"}}, Answers: []Answer{{"B", "4"}}},
		{Number: 2, Text: "Capital of France?", Choices: []Answer{{"A", "London"}, {"B", "Paris"}}, Answers: []Answer{{"B", "Paris"}}},
	}
}

func job() *domain.Job {
	return &domain.Job{ID: "job-1", ImageID: "img-1", SessionID: "sess-1", Status: domain.JobStatusProcessing}
}

// --- Test cases ---

func TestPipeline_HappyPath(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()}}
	ver := &fakeVerifier{result: VerifyResult{Summary: VerificationSummary{Results: []VerifiedQuestion{
		{Index: 0, Confidence: 0.95, Explanation: "correct"},
		{Index: 1, Confidence: 0.90, Explanation: "correct"},
	}}}}
	emb := &fakeEmbedder{embedding: []float32{0.1, 0.2}}
	p, _, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, emb)

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(jq.completed) != 1 {
		t.Errorf("expected 1 complete, got %d", len(jq.completed))
	}
	if len(qRepo.created) != 2 {
		t.Errorf("expected 2 created, got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 2 {
		t.Errorf("expected 2 links, got %d", len(qRepo.links))
	}
	if len(qRepo.updatedFromVer) != 2 {
		t.Errorf("expected 2 verifications, got %d", len(qRepo.updatedFromVer))
	}
	if !ver.called {
		t.Error("verifier not called")
	}
}

func TestPipeline_ExactDedupSkipsVerify(t *testing.T) {
	// Pre-seed an exact-dedup match for question 1's hash
	qRepo := newFakeQuestionRepo()
	norm := normalizeQuestion("What is 2+2?")
	hash := sha256String(norm)
	existing := &domain.Question{ID: "existing-1", TextHash: hash}
	qRepo.byHash[hash] = existing

	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}} // only q1
	ver := &fakeVerifier{}
	emb := &fakeEmbedder{embedding: []float32{0.1}}

	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, &fakeEnhancer{}, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 0 {
		t.Errorf("expected 0 created (all deduped), got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 1 {
		t.Errorf("expected 1 link to existing, got %d", len(qRepo.links))
	}
	if ver.called {
		t.Error("verifier should NOT be called when all deduped")
	}
}

func TestPipeline_SemanticDedup(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	ver := &fakeVerifier{}
	emb := &fakeEmbedder{embedding: []float32{0.1}}

	qRepo := newFakeQuestionRepo()
	qRepo.semantMatch = &domain.Question{ID: "semant-existing"} // semantic dedup returns this
	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, &fakeEnhancer{}, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if qRepo.findSemantCalls == 0 {
		t.Error("FindSemantic should have been called")
	}
	if len(qRepo.created) != 0 {
		t.Errorf("expected 0 created (semantic match links existing), got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 1 {
		t.Errorf("expected 1 link to semantic match, got %d", len(qRepo.links))
	}
	if qRepo.links[0].questionID != "semant-existing" {
		t.Errorf("should link to semant-existing, got %s", qRepo.links[0].questionID)
	}
	if ver.called {
		t.Error("verifier should NOT be called when all deduped")
	}
}

func TestPipeline_UnreadableThreeAttemptsPlaceholder(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Error: &ExtractionError{Code: ExtractionCodeUnreadableImage, Detail: "blurry"}}}
	p, imgRepo, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ext.calls != 3 {
		t.Errorf("expected 3 extract attempts, got %d", ext.calls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("expected 1 error placeholder, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Status != domain.QuestionStatusError {
		t.Errorf("placeholder status = %q, want error", qRepo.created[0].Status)
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete (placeholder captures error), got %d completes", len(jq.completed))
	}
	if imgRepo.extractErr["img-1"] == nil {
		t.Error("extraction error should be stored on image")
	}
}

func TestPipeline_PartialExtractionProceeds(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{
		Questions: sampleQuestions()[:1],
		Error:     &ExtractionError{Code: ExtractionCodePartial, Detail: "1 of 2 parsed"},
	}}
	ver := &fakeVerifier{result: VerifyResult{Summary: VerificationSummary{Results: []VerifiedQuestion{{Index: 0, Confidence: 0.8}}}}}
	p, _, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ext.calls != 1 {
		t.Errorf("partial should not retry, got %d calls", ext.calls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("expected 1 question from partial, got %d", len(qRepo.created))
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete, got %d", len(jq.completed))
	}
}

func TestPipeline_VerifierFailureBestEffort(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	ver := &fakeVerifier{err: errors.New("deepseek down")}
	p, _, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run should not fail on verifier error, got: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("question should be created despite verify failure, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Status != domain.QuestionStatusModeration {
		t.Errorf("question should stay moderation, got %q", qRepo.created[0].Status)
	}
	if len(qRepo.updatedFromVer) != 0 {
		t.Errorf("no UpdateFromVerification should be called, got %d", len(qRepo.updatedFromVer))
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete, got %d", len(jq.completed))
	}
}

func TestPipeline_ShutdownDuringExtract(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()}}
	p, _, _, jq := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := p.Run(ctx, job())
	if err == nil {
		t.Fatal("expected error on shutdown, got nil")
	}
	if len(jq.completed) != 0 {
		t.Errorf("job should NOT be completed on shutdown, got %d", len(jq.completed))
	}
	if len(jq.failed) != 0 {
		t.Errorf("job should NOT be failed on shutdown (reaper reclaims), got %d", len(jq.failed))
	}
}

func TestPipeline_EmbedderFailure(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	emb := &fakeEmbedder{err: errors.New("embed service down")}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, emb)

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !emb.called {
		t.Error("embedder should have been called")
	}
	if qRepo.findSemantCalls != 0 {
		t.Errorf("FindSemantic should be skipped when embed fails, got %d calls", qRepo.findSemantCalls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("question should be created without embedding, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Embedding != nil {
		t.Error("created question should have nil embedding")
	}
}

func TestPipeline_AnswersValueOnly(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
		{Number: 1, Text: "Pick the capital", Choices: []Answer{{"A", "Rome"}, {"B", "Madrid"}}, Answers: []Answer{{"A", "Rome"}}},
	}}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	_ = p.Run(context.Background(), job())
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	q := qRepo.created[0]
	if len(q.Answers) != 1 || q.Answers[0] != "Rome" {
		t.Errorf("answers should be value-only [Rome], got %v", q.Answers)
	}
	if len(q.Choices) != 2 || q.Choices[0] != "Rome" || q.Choices[1] != "Madrid" {
		t.Errorf("choices should be value-only [Rome Madrid], got %v", q.Choices)
	}
}

func TestPipeline_AIGeneratedTag(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	_ = p.Run(context.Background(), job())
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	q := qRepo.created[0]
	found := false
	for _, tag := range q.Tags {
		if tag == "ai-generated" {
			found = true
		}
	}
	if !found {
		t.Errorf("new question should have ai-generated tag, tags=%v", q.Tags)
	}
}
```

- [ ] **Step 2: Run the tests to verify they pass**

Run: `go test ./internal/pipeline/ -v`
Expected: all 10 tests `PASS`

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/pipeline_test.go
git commit -m "test(pipeline): add 10 table-driven pipeline test cases"
```

---

## Task 5: Worker Pool

The WorkerPool starts N worker goroutines, each with a dedicated PGX connection for `LISTEN jobs_new`. Workers claim jobs via `FOR UPDATE SKIP LOCKED`, run the pipeline, and block on NOTIFY between jobs (with a 5s poll fallback). A reaper goroutine reclaims stale processing jobs on startup + interval.

**Files:**
- Create: `internal/pipeline/worker.go`

- [ ] **Step 1: Create the worker pool implementation**

```go
package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

const (
	workerPollInterval = 5 * time.Second // NOTIFY wait timeout + poll fallback
	workerClaimBackoff = 1 * time.Second // pause after a claim error
)

// WorkerPool runs N workers consuming jobs from the queue.
type WorkerPool struct {
	jobs       storage.JobQueue
	pipeline   *Pipeline
	workersCfg config.WorkersConfig
	pipelineCfg config.PipelineConfig
	dsn        string
	log        *slog.Logger

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func NewWorkerPool(
	jobs storage.JobQueue,
	pipeline *Pipeline,
	workersCfg config.WorkersConfig,
	pipelineCfg config.PipelineConfig,
	dsn string,
	log *slog.Logger,
) *WorkerPool {
	if log == nil {
		log = slog.Default()
	}
	return &WorkerPool{
		jobs: jobs, pipeline: pipeline,
		workersCfg: workersCfg, pipelineCfg: pipelineCfg,
		dsn: dsn, log: log,
	}
}

// Start launches the reaper and N worker goroutines. It is safe to call once.
func (wp *WorkerPool) Start(ctx context.Context) {
	ctx, wp.cancel = context.WithCancel(ctx)

	// Startup reaper — reclaim any stale jobs from a previous crash
	wp.runReaperOnce(ctx)

	// Reaper ticker goroutine
	wp.wg.Add(1)
	go wp.reaperLoop(ctx)

	// Worker goroutines
	for i := 0; i < wp.workersCfg.Count; i++ {
		wp.wg.Add(1)
		go wp.worker(ctx, i)
	}
}

// Stop signals all goroutines to stop and waits for them to finish.
func (wp *WorkerPool) Stop() {
	if wp.cancel != nil {
		wp.cancel()
	}
	wp.wg.Wait()
}

func (wp *WorkerPool) runReaperOnce(ctx context.Context) {
	n, err := wp.jobs.ReaperReclaim(ctx, wp.pipelineCfg.StaleThreshold)
	if err != nil {
		wp.log.Error("startup reaper", "error", err)
		return
	}
	if n > 0 {
		wp.log.Info("startup reaper reclaimed stale jobs", "count", n)
	}
}

func (wp *WorkerPool) reaperLoop(ctx context.Context) {
	defer wp.wg.Done()

	// Handle zero interval gracefully
	interval := wp.pipelineCfg.ReaperInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := wp.jobs.ReaperReclaim(ctx, wp.pipelineCfg.StaleThreshold)
			if err != nil {
				wp.log.Error("reaper", "error", err)
				continue
			}
			if n > 0 {
				wp.log.Info("reaper reclaimed stale jobs", "count", n)
			}
		}
	}
}

// worker is one consumer goroutine. It has a dedicated pgx.Conn for LISTEN
// jobs_new and claims jobs via the shared pool's Claim method.
func (wp *WorkerPool) worker(ctx context.Context, id int) {
	defer wp.wg.Done()

	conn, err := pgx.Connect(ctx, wp.dsn)
	if err != nil {
		wp.log.Error("worker connect failed", "worker", id, "error", err)
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN jobs_new"); err != nil {
		wp.log.Error("worker listen failed", "worker", id, "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := wp.jobs.Claim(ctx)
		if err != nil {
			wp.log.Error("claim", "worker", id, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(workerClaimBackoff):
			}
			continue
		}
		if job != nil {
			wp.runJob(ctx, job, id)
			continue
		}

		// No job — wait for NOTIFY or poll timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, workerPollInterval)
		_, _ = conn.WaitForNotification(waitCtx)
		waitCancel()
	}
}

func (wp *WorkerPool) runJob(ctx context.Context, job *domain.Job, workerID int) {
	wp.log.Info("processing job", "worker", workerID, "job", job.ID, "image", job.ImageID)
	err := wp.pipeline.Run(ctx, job)
	if err != nil {
		wp.log.Error("pipeline returned error", "worker", workerID, "job", job.ID, "error", err)
		return
	}
	wp.log.Info("job done", "worker", workerID, "job", job.ID)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/pipeline/`
Expected: no output (success)

- [ ] **Step 3: Commit**

```bash
git add internal/pipeline/worker.go
git commit -m "feat(pipeline): add worker pool with LISTEN/NOTIFY and reaper"
```

---

## Task 6: Worker Pool Tests

Unit tests (no DB required) for Stop safety, plus Testcontainers integration tests for the NOTIFY → process → done flow.

**Files:**
- Create: `internal/pipeline/worker_test.go`

- [ ] **Step 1: Create the worker test file**

```go
package pipeline

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	pgstore "github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

// --- Unit tests (no DB) ---

func TestWorkerPool_StopWithoutStart(t *testing.T) {
	wp := NewWorkerPool(nil, nil,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		"unused", slog.Default())
	// Stop on an unstarted pool should be a safe no-op
	wp.Stop()
}

func TestWorkerPool_StartAndStop(t *testing.T) {
	jq := newFakeJobQueue()
	p := NewPipeline(
		newFakeImageRepo(nil), newFakeQuestionRepo(), jq,
		&fakeEnhancer{}, &fakeExtractor{}, &fakeVerifier{}, &fakeEmbedder{},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		quietLogger(),
	)
	wp := NewWorkerPool(jq, p,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		"postgres://unused", quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Workers will fail to connect (bad DSN) but should not crash
	wp.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	wp.Stop()
}

// --- Integration tests (Testcontainers) ---

func setupPipelineTestDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	container, err := tcpg.Run(ctx,
		"pgvector/pgvector:pg16",
		tcpg.WithDatabase("coeus_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
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
		container.Terminate(ctx)
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pgstore.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pool, connStr
}

func TestWorkerPool_IntegrationProcessesJob(t *testing.T) {
	pool, dsn := setupPipelineTestDB(t)

	ctx := context.Background()
	userRepo := pgstore.NewUserRepo(pool)
	sessRepo := pgstore.NewSessionRepo(pool)
	imgRepo := pgstore.NewImageRepo(pool)
	qRepo := pgstore.NewQuestionRepo(pool)
	jq := pgstore.NewJobQueue(pool)

	user, _ := userRepo.Create(ctx, "worker@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 100, 100)

	// Pipeline with fakes that always succeed
	pipeline := NewPipeline(imgRepo, qRepo, jq,
		&fakeEnhancer{enhanced: []byte("enhanced")},
		&fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
			{Number: 1, Text: "Test question?", Answers: []Answer{{"A", "yes"}}},
		}}},
		&fakeVerifier{result: VerifyResult{Summary: VerificationSummary{Results: []VerifiedQuestion{
			{Index: 0, Confidence: 0.9, Explanation: "ok"},
		}}}},
		&fakeEmbedder{embedding: []float32{0.1}},
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92,
			ReaperInterval: 2 * time.Second, StaleThreshold: time.Minute},
		quietLogger(),
	)

	wp := NewWorkerPool(jq, pipeline,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: 2 * time.Second, StaleThreshold: time.Minute},
		dsn, quietLogger())

	wpCtx, cancel := context.WithCancel(context.Background())
	wp.Start(wpCtx)
	defer func() {
		cancel()
		wp.Stop()
	}()

	// Enqueue a job — NOTIFY should wake a worker
	jobID, err := jq.Enqueue(ctx, imgID, sess.ID)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Poll for job completion (max 10s)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := jq.FindByImageID(ctx, imgID)
		if job != nil && job.Status == domain.JobStatusDone {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	job, _ := jq.FindByImageID(ctx, imgID)
	if job == nil || job.Status != domain.JobStatusDone {
		t.Fatalf("job %s did not complete, status=%v", jobID, job)
	}
}

func TestWorkerPool_IntegrationReaperReclaims(t *testing.T) {
	pool, _ := setupPipelineTestDB(t)

	ctx := context.Background()
	userRepo := pgstore.NewUserRepo(pool)
	sessRepo := pgstore.NewSessionRepo(pool)
	imgRepo := pgstore.NewImageRepo(pool)
	jq := pgstore.NewJobQueue(pool)

	user, _ := userRepo.Create(ctx, "reaper2@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 100, 100)

	// Enqueue and manually claim (marks as processing with stale_threshold=0)
	jq.Enqueue(ctx, imgID, sess.ID)
	claimed, _ := jq.Claim(ctx)
	if claimed == nil {
		t.Fatal("expected to claim a job")
	}

	// Reaper with 0 threshold reclaims immediately
	n, err := jq.ReaperReclaim(ctx, 0)
	if err != nil {
		t.Fatalf("reaper: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", n)
	}

	// Job should be claimable again
	job, _ := jq.Claim(ctx)
	if job == nil {
		t.Fatal("expected job after reclaim")
	}
}
```

- [ ] **Step 2: Run the unit tests (short mode)**

Run: `go test ./internal/pipeline/ -run TestWorkerPool -short -v`
Expected: 2 unit tests `PASS`, integration tests skipped

- [ ] **Step 3: Run all pipeline tests including integration**

Run: `go test ./internal/pipeline/ -v -timeout 120s`
Expected: all tests `PASS` (integration tests start Testcontainers — may take 30-60s)

- [ ] **Step 4: Commit**

```bash
git add internal/pipeline/worker_test.go
git commit -m "test(pipeline): add worker pool unit and integration tests"
```

---

## Task 7: Session DTOs

Request/response types for the sessions and images API, shared across handlers.

**Files:**
- Create: `internal/httpapi/dto/requests.go`
- Create: `internal/httpapi/dto/responses.go`

- [ ] **Step 1: Create the requests file**

```go
// internal/httpapi/dto/requests.go
package dto

// CreateSessionRequest is the body of POST /api/v1/sessions.
type CreateSessionRequest struct {
	DurationSeconds int `json:"duration_seconds" binding:"required,min=1"`
	BufferSeconds   int `json:"buffer_seconds" binding:"min=0"`
}
```

- [ ] **Step 2: Create the responses file**

```go
// internal/httpapi/dto/responses.go
package dto

// SessionResponse is the minimal session shape returned in lists and on create.
type SessionResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
	Status    string `json:"status"`
}

// SessionDetailResponse is the enriched shape for GET /:id.
type SessionDetailResponse struct {
	SessionResponse
	DurationSeconds int    `json:"duration_seconds"`
	BufferSeconds   int    `json:"buffer_seconds"`
	StartedAt       string `json:"started_at"`
	ImageCount      int    `json:"image_count"`
}

// SessionListResponse wraps a paginated session list.
type SessionListResponse struct {
	Data    []SessionResponse `json:"data"`
	Page    int               `json:"page"`
	PerPage int               `json:"per_page"`
}

// ImageUploadResponse is returned on POST /:id/images (202 Accepted).
type ImageUploadResponse struct {
	ImageID string `json:"image_id"`
	JobID   string `json:"job_id"`
}

// ImageResponse is one image in a session's image list.
type ImageResponse struct {
	ID        string `json:"id"`
	Mime      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	JobStatus string `json:"job_status"`
	CreatedAt string `json:"created_at"`
}

// ImageListResponse wraps a list of images.
type ImageListResponse struct {
	Data []ImageResponse `json:"data"`
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/httpapi/dto/`
Expected: no output (success)

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi/dto/
git commit -m "feat(httpapi): add session and image DTOs"
```

---

## Task 8: SessionWindow Middleware

SessionWindow guards upload paths. It checks session ownership, open status, and expiry. Failures return 404 (for ownership/not-found — don't leak existence) or 410 Gone (for closed/expired).

**Files:**
- Modify: `internal/httpapi/middleware.go`
- Create: `internal/httpapi/middleware_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/middleware_test.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// fakeSessionRepo implements just enough for middleware tests.
type fakeSessionRepo struct {
	session *domain.Session
	err     error
}

func (f *fakeSessionRepo) Create(context.Context, string, int, int) (*domain.Session, error) {
	return nil, nil
}
func (f *fakeSessionRepo) FindByID(_ context.Context, _ string) (*domain.Session, error) {
	return f.session, f.err
}
func (f *fakeSessionRepo) ListByUser(context.Context, string, int, int) ([]*domain.Session, error) {
	return nil, nil
}
func (f *fakeSessionRepo) Close(context.Context, string) error { return nil }

func setupRouter(repo *fakeSessionRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	return r
}

func TestSessionWindow_OpenPasses(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	called := false
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		called = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestSessionWindow_Expired(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", w.Code)
	}
	var body map[string]map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"]["code"] != "session_expired" {
		t.Errorf("error code = %v, want session_expired", body["error"]["code"])
	}
}

func TestSessionWindow_Closed(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusClosed,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", w.Code)
	}
}

func TestSessionWindow_NotFound(t *testing.T) {
	repo := &fakeSessionRepo{err: domain.ErrNotFound}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSessionWindow_WrongOwnership(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "other-user", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (don't leak existence)", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestSessionWindow -v`
Expected: compile error — `undefined: SessionWindow`

- [ ] **Step 3: Implement SessionWindow middleware**

Add to the end of `internal/httpapi/middleware.go`:

```go
// SessionWindow guards upload/list paths behind session ownership + open status + expiry.
// It expects AuthMiddleware to have set "user_id" in the gin context.
// Not-found and wrong-owner both return 404 to avoid leaking session existence.
func SessionWindow(sessions storage.SessionRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		sessionID := c.Param("id")

		sess, err := sessions.FindByID(c.Request.Context(), sessionID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, apiError(domain.ErrNotFound))
			return
		}

		if sess.UserID != userID {
			c.AbortWithStatusJSON(http.StatusNotFound, apiError(domain.ErrNotFound))
			return
		}

		if sess.Status != domain.SessionStatusOpen {
			c.AbortWithStatusJSON(http.StatusGone, apiError(domain.ErrSessionExpired))
			return
		}

		expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
		if err != nil || time.Now().After(expiresAt) {
			c.AbortWithStatusJSON(http.StatusGone, apiError(domain.ErrSessionExpired))
			return
		}

		c.Set("session", sess)
		c.Next()
	}
}
```

Also add `"github.com/vlgrigoriev/coeus/internal/storage"` to the import block at the top of `middleware.go` (it does not currently import `storage`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/ -run TestSessionWindow -v`
Expected: all 5 tests `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/middleware.go internal/httpapi/middleware_test.go
git commit -m "feat(httpapi): add SessionWindow middleware"
```

---

## Task 9: Session Handlers

CRUD handlers for sessions: Create, List (paginated), Get (with image_count), Close.

**Files:**
- Create: `internal/httpapi/handlers/sessions.go`
- Create: `internal/httpapi/handlers/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/handlers/sessions_test.go`:

```go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
)

// --- Fakes ---

type fakeSessionRepo struct {
	created   *domain.Session
	list      []*domain.Session
	session   *domain.Session // returned by FindByID
	err       error
	closed    bool
}

func (f *fakeSessionRepo) Create(_ context.Context, userID string, dur, buf int) (*domain.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = &domain.Session{
		ID: "sess-new", UserID: userID, DurationSeconds: dur, BufferSeconds: buf,
		Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(time.Duration(dur+buf) * time.Second).Format(time.RFC3339),
	}
	return f.created, nil
}
func (f *fakeSessionRepo) FindByID(_ context.Context, _ string) (*domain.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}
func (f *fakeSessionRepo) ListByUser(_ context.Context, _ string, _, _ int) ([]*domain.Session, error) {
	return f.list, nil
}
func (f *fakeSessionRepo) Close(_ context.Context, _ string) error {
	f.closed = true
	return nil
}

type fakeImageRepoForSessions struct {
	count int
}

func (f *fakeImageRepoForSessions) Create(context.Context, string, []byte, string, int, int) (string, error) {
	return "", nil
}
func (f *fakeImageRepoForSessions) FindByID(context.Context, string) (*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoForSessions) ListBySession(context.Context, string) ([]*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoForSessions) UpdateEnhanced(context.Context, string, []byte) error { return nil }
func (f *fakeImageRepoForSessions) UpdateVerificationReport(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoForSessions) UpdateExtractionError(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoForSessions) CleanBytes(context.Context, string) error { return nil }
func (f *fakeImageRepoForSessions) CountBySession(_ context.Context, _ string) (int, error) {
	return f.count, nil
}

// --- Tests ---

func newSessionRouter(h *SessionHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/sessions", h.Create)
	r.GET("/sessions", h.List)
	r.GET("/sessions/:id", h.Get)
	r.POST("/sessions/:id/close", h.Close)
	return r
}

func TestSessionHandler_Create(t *testing.T) {
	repo := &fakeSessionRepo{}
	imgRepo := &fakeImageRepoForSessions{}
	h := NewSessionHandler(repo, imgRepo)
	r := newSessionRouter(h)

	body, _ := json.Marshal(dto.CreateSessionRequest{DurationSeconds: 3600, BufferSeconds: 300})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions", bytes.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	var resp dto.SessionResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != domain.SessionStatusOpen {
		t.Errorf("status = %q, want open", resp.Status)
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at should not be empty")
	}
}

func TestSessionHandler_CreateValidation(t *testing.T) {
	repo := &fakeSessionRepo{}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	// Missing duration_seconds
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions", bytes.NewReader([]byte(`{}`))))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSessionHandler_List(t *testing.T) {
	repo := &fakeSessionRepo{list: []*domain.Session{
		{ID: "s1", Status: domain.SessionStatusOpen, ExpiresAt: "2026-12-01T00:00:00Z"},
		{ID: "s2", Status: domain.SessionStatusClosed, ExpiresAt: "2026-12-02T00:00:00Z"},
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions?page=1&per_page=10", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp dto.SessionListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(resp.Data))
	}
	if resp.Page != 1 || resp.PerPage != 10 {
		t.Errorf("pagination wrong: page=%d per_page=%d", resp.Page, resp.PerPage)
	}
}

func TestSessionHandler_Get(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		DurationSeconds: 3600, BufferSeconds: 300,
		StartedAt: "2026-06-20T12:00:00Z", ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	imgRepo := &fakeImageRepoForSessions{count: 3}
	h := NewSessionHandler(repo, imgRepo)
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-1", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp dto.SessionDetailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ImageCount != 3 {
		t.Errorf("image_count = %d, want 3", resp.ImageCount)
	}
}

func TestSessionHandler_GetNotFound(t *testing.T) {
	repo := &fakeSessionRepo{err: domain.ErrNotFound}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-404", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSessionHandler_GetWrongOwnership(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "other-user", Status: domain.SessionStatusOpen,
		ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-1", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (don't leak)", w.Code)
	}
}

func TestSessionHandler_Close(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/close", nil))

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if !repo.closed {
		t.Error("Close was not called")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/handlers/ -run TestSessionHandler -v`
Expected: compile error — `undefined: SessionHandler, undefined: NewSessionHandler`

- [ ] **Step 3: Create the sessions handler**

Create `internal/httpapi/handlers/sessions.go`:

```go
package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type SessionHandler struct {
	sessions storage.SessionRepo
	images   storage.ImageRepo
}

func NewSessionHandler(sessions storage.SessionRepo, images storage.ImageRepo) *SessionHandler {
	return &SessionHandler{sessions: sessions, images: images}
}

func (h *SessionHandler) Create(c *gin.Context) {
	var req dto.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	userID := c.GetString("user_id")
	sess, err := h.sessions.Create(c.Request.Context(), userID, req.DurationSeconds, req.BufferSeconds)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusCreated, dto.SessionResponse{
		ID:        sess.ID,
		ExpiresAt: sess.ExpiresAt,
		Status:    sess.Status,
	})
}

func (h *SessionHandler) List(c *gin.Context) {
	userID := c.GetString("user_id")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	offset := (page - 1) * perPage

	sessions, err := h.sessions.ListByUser(c.Request.Context(), userID, perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	data := make([]dto.SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		data = append(data, dto.SessionResponse{
			ID:        s.ID,
			ExpiresAt: s.ExpiresAt,
			Status:    s.Status,
		})
	}

	c.JSON(http.StatusOK, dto.SessionListResponse{Data: data, Page: page, PerPage: perPage})
}

func (h *SessionHandler) Get(c *gin.Context) {
	userID := c.GetString("user_id")
	id := c.Param("id")

	sess, err := h.sessions.FindByID(c.Request.Context(), id)
	if err != nil || sess.UserID != userID {
		// Not found or wrong owner — 404 to avoid leaking existence
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}

	imageCount, _ := h.images.CountBySession(c.Request.Context(), id)

	c.JSON(http.StatusOK, dto.SessionDetailResponse{
		SessionResponse: dto.SessionResponse{
			ID:        sess.ID,
			ExpiresAt: sess.ExpiresAt,
			Status:    sess.Status,
		},
		DurationSeconds: sess.DurationSeconds,
		BufferSeconds:   sess.BufferSeconds,
		StartedAt:       sess.StartedAt,
		ImageCount:      imageCount,
	})
}

func (h *SessionHandler) Close(c *gin.Context) {
	userID := c.GetString("user_id")
	id := c.Param("id")

	sess, err := h.sessions.FindByID(c.Request.Context(), id)
	if err != nil || sess.UserID != userID {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}

	if err := h.sessions.Close(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/handlers/ -run TestSessionHandler -v`
Expected: all 7 tests `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/handlers/sessions.go internal/httpapi/handlers/sessions_test.go
git commit -m "feat(httpapi): add session CRUD handlers"
```

---

## Task 10: Image Upload Handler

POST upload (multipart/form-data, MIME + size validation, dimension decode, image+job insert) and GET list (images with job status).

**Files:**
- Create: `internal/httpapi/handlers/images.go`
- Create: `internal/httpapi/handlers/images_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/handlers/images_test.go`:

```go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
)

// --- Fakes ---

type fakeImageRepoFull struct {
	created   []byte
	mime      string
	width     int
	height    int
	returnID  string
	list      []*domain.Image
	err       error
}

func (f *fakeImageRepoFull) Create(_ context.Context, _ string, original []byte, mime string, w, h int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = original
	f.mime = mime
	f.width = w
	f.height = h
	if f.returnID == "" {
		return "img-new", nil
	}
	return f.returnID, nil
}
func (f *fakeImageRepoFull) FindByID(context.Context, string) (*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoFull) ListBySession(_ context.Context, _ string) ([]*domain.Image, error) {
	return f.list, nil
}
func (f *fakeImageRepoFull) UpdateEnhanced(context.Context, string, []byte) error  { return nil }
func (f *fakeImageRepoFull) UpdateVerificationReport(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoFull) UpdateExtractionError(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoFull) CleanBytes(context.Context, string) error            { return nil }
func (f *fakeImageRepoFull) CountBySession(context.Context, string) (int, error) { return 0, nil }

type fakeJobQueueForImages struct {
	enqueued   bool
	imageID    string
	sessionID  string
	jobByImage *domain.Job
}

func (q *fakeJobQueueForImages) Enqueue(_ context.Context, imageID, sessionID string) (string, error) {
	q.enqueued = true
	q.imageID = imageID
	q.sessionID = sessionID
	return "job-new", nil
}
func (q *fakeJobQueueForImages) Claim(context.Context) (*domain.Job, error) { return nil, nil }
func (q *fakeJobQueueForImages) Complete(context.Context, string) error     { return nil }
func (q *fakeJobQueueForImages) Fail(context.Context, string, string) error { return nil }
func (q *fakeJobQueueForImages) ReaperReclaim(context.Context, time.Duration) (int, error) {
	return 0, nil
}
func (q *fakeJobQueueForImages) FindByImageID(_ context.Context, _ string) (*domain.Job, error) {
	return q.jobByImage, nil
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func newImageRouter(h *ImageHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/sessions/:id/images", func(c *gin.Context) {
		c.Set("session", &domain.Session{ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen})
		h.Upload(c)
	})
	r.GET("/sessions/:id/images", func(c *gin.Context) {
		c.Set("session", &domain.Session{ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen})
		h.List(c)
	})
	return r
}

func TestImageHandler_Upload(t *testing.T) {
	imgRepo := &fakeImageRepoFull{}
	jq := &fakeJobQueueForImages{}
	uploadCfg := config.UploadConfig{
		MaxBytes:     10 * 1024 * 1024,
		AllowedMimes: []string{"image/png", "image/jpeg", "image/webp"},
	}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	pngBytes := validPNG(t)
	body := &bytes.Buffer{}
	w := realMultipartWriter(body, pngBytes)
	req := httptest.NewRequest("POST", "/sessions/sess-1/images", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp dto.ImageUploadResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ImageID == "" || resp.JobID == "" {
		t.Errorf("expected image_id and job_id, got %+v", resp)
	}
	if !jq.enqueued {
		t.Error("job was not enqueued")
	}
	if imgRepo.width != 4 || imgRepo.height != 4 {
		t.Errorf("dimensions = %dx%d, want 4x4", imgRepo.width, imgRepo.height)
	}
}

func TestImageHandler_UploadWrongMime(t *testing.T) {
	imgRepo := &fakeImageRepoFull{}
	jq := &fakeJobQueueForImages{}
	uploadCfg := config.UploadConfig{
		MaxBytes:     10 * 1024 * 1024,
		AllowedMimes: []string{"image/png", "image/jpeg", "image/webp"},
	}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	// Upload a text file disguised as image
	body := &bytes.Buffer{}
	w := realMultipartWriter(body, []byte("this is not an image"))
	req := httptest.NewRequest("POST", "/sessions/sess-1/images", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid MIME", rr.Code)
	}
}

func TestImageHandler_List(t *testing.T) {
	imgRepo := &fakeImageRepoFull{
		list: []*domain.Image{
			{ID: "img-1", Mime: "image/png", Width: 100, Height: 200, CreatedAt: "2026-06-20T12:00:00Z"},
		},
	}
	jq := &fakeJobQueueForImages{jobByImage: &domain.Job{ID: "job-1", Status: domain.JobStatusDone}}
	uploadCfg := config.UploadConfig{}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/sessions/sess-1/images", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp dto.ImageListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Data))
	}
	if resp.Data[0].JobStatus != domain.JobStatusDone {
		t.Errorf("job_status = %q, want done", resp.Data[0].JobStatus)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/handlers/ -run TestImageHandler -v`
Expected: compile error — `undefined: ImageHandler, undefined: NewImageHandler, undefined: realMultipartWriter`

- [ ] **Step 3: Create the images handler**

Create `internal/httpapi/handlers/images.go`:

```go
package handlers

import (
	"bytes"
	"image"
	"io"
	"net/http"
	"strings"

	_ "image/jpeg" // register jpeg for DecodeConfig
	_ "image/png"  // register png for DecodeConfig

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type ImageHandler struct {
	images    storage.ImageRepo
	jobs      storage.JobQueue
	uploadCfg config.UploadConfig
}

func NewImageHandler(images storage.ImageRepo, jobs storage.JobQueue, uploadCfg config.UploadConfig) *ImageHandler {
	return &ImageHandler{images: images, jobs: jobs, uploadCfg: uploadCfg}
}

func (h *ImageHandler) Upload(c *gin.Context) {
	sess, exists := c.Get("session")
	if !exists {
		c.JSON(http.StatusInternalServerError, errorResponse(domain.NewError("internal", "session missing")))
		return
	}
	session := sess.(*domain.Session)

	// Enforce size cap
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.uploadCfg.MaxBytes)

	file, _, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Sniff actual content type from first 512 bytes
	mime := http.DetectContentType(data)
	if !h.uploadCfg.AllowedMimesMap()[strings.ToLower(mime)] {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Decode dimensions (best-effort — webp may not decode via stdlib)
	width, height := 0, 0
	if cfg, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		width, height = cfg.Width, cfg.Height
	}

	ctx := c.Request.Context()

	imgID, err := h.images.Create(ctx, session.ID, data, mime, width, height)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	jobID, err := h.jobs.Enqueue(ctx, imgID, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusAccepted, dto.ImageUploadResponse{ImageID: imgID, JobID: jobID})
}

func (h *ImageHandler) List(c *gin.Context) {
	sess, exists := c.Get("session")
	if !exists {
		c.JSON(http.StatusInternalServerError, errorResponse(domain.NewError("internal", "session missing")))
		return
	}
	session := sess.(*domain.Session)

	ctx := c.Request.Context()

	images, err := h.images.ListBySession(ctx, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	data := make([]dto.ImageResponse, 0, len(images))
	for _, img := range images {
		jobStatus := "unknown"
		if job, _ := h.jobs.FindByImageID(ctx, img.ID); job != nil {
			jobStatus = job.Status
		}
		data = append(data, dto.ImageResponse{
			ID:        img.ID,
			Mime:      img.Mime,
			Width:     img.Width,
			Height:    img.Height,
			JobStatus: jobStatus,
			CreatedAt: img.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, dto.ImageListResponse{Data: data})
}
```

Also add the `realMultipartWriter` helper to the bottom of `internal/httpapi/handlers/images_test.go` and add `"mime/multipart"` to its imports:

```go
// realMultipartWriter creates a multipart form with an "image" field.
func realMultipartWriter(buf *bytes.Buffer, data []byte) *multipart.Writer {
	w := multipart.NewWriter(buf)
	fw, _ := w.CreateFormFile("image", "test.png")
	fw.Write(data)
	w.Close()
	return w
}
```

And update the test file imports to include `"mime/multipart"`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/httpapi/handlers/ -run TestImageHandler -v`
Expected: all 3 tests `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/handlers/images.go internal/httpapi/handlers/images_test.go
git commit -m "feat(httpapi): add image upload and list handlers"
```

---

## Task 11: Server + Composition Root Updates

Wire all new repos into the Server constructor, register session + image routes, and update the composition root.

**Files:**
- Modify: `internal/httpapi/server.go`
- Modify: `internal/app/wire.go`

- [ ] **Step 1: Update the Server struct and constructor**

In `internal/httpapi/server.go`, replace the entire file with:

```go
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type Server struct {
	router     *gin.Engine
	userRepo   storage.UserRepo
	sessionRepo storage.SessionRepo
	imageRepo  storage.ImageRepo
	jobQueue   storage.JobQueue
	jwtMgr     *auth.JWTManager
	pool       *pgxpool.Pool
	uploadCfg  config.UploadConfig
}

func NewServer(
	userRepo storage.UserRepo,
	sessionRepo storage.SessionRepo,
	imageRepo storage.ImageRepo,
	jobQueue storage.JobQueue,
	jwtMgr *auth.JWTManager,
	pool *pgxpool.Pool,
	uploadCfg config.UploadConfig,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())

	s := &Server{
		router: r, userRepo: userRepo, sessionRepo: sessionRepo,
		imageRepo: imageRepo, jobQueue: jobQueue, jwtMgr: jwtMgr,
		pool: pool, uploadCfg: uploadCfg,
	}
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

	// Sessions + Images (auth required)
	sessionHandler := handlers.NewSessionHandler(s.sessionRepo, s.imageRepo)
	imageHandler := handlers.NewImageHandler(s.imageRepo, s.jobQueue, s.uploadCfg)

	apiGroup := r.Group("/api/v1")
	apiGroup.Use(AuthMiddleware(s.jwtMgr))
	{
		sessions := apiGroup.Group("/sessions")
		{
			sessions.POST("", sessionHandler.Create)
			sessions.GET("", sessionHandler.List)
			sessions.GET("/:id", sessionHandler.Get)
			sessions.POST("/:id/close", sessionHandler.Close)

			// Image routes — SessionWindow guards ownership + expiry
			sessions.POST("/:id/images", SessionWindow(s.sessionRepo), imageHandler.Upload)
			sessions.GET("/:id/images", SessionWindow(s.sessionRepo), imageHandler.List)
		}
	}
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

- [ ] **Step 2: Update the composition root**

In `internal/app/wire.go`, replace the entire file with:

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
	Config       *config.Config
	Pool         *pgxpool.Pool
	UserRepo     *postgres.UserRepo
	SessionRepo  *postgres.SessionRepo
	ImageRepo    *postgres.ImageRepo
	QuestionRepo *postgres.QuestionRepo
	JobQueue     *postgres.JobQueue
	JWTMgr       *auth.JWTManager
	Server       *httpapi.Server
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
	sessionRepo := postgres.NewSessionRepo(pool)
	imageRepo := postgres.NewImageRepo(pool)
	questionRepo := postgres.NewQuestionRepo(pool)
	jobQueue := postgres.NewJobQueue(pool)
	jwtMgr := auth.NewJWTManager(cfg.JWT)

	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)

	return &App{
		Config: cfg, Pool: pool,
		UserRepo: userRepo, SessionRepo: sessionRepo,
		ImageRepo: imageRepo, QuestionRepo: questionRepo,
		JobQueue: jobQueue, JWTMgr: jwtMgr, Server: server,
	}, nil
}

func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
}
```

- [ ] **Step 3: Verify everything compiles**

Run: `go build ./...`
Expected: no output (success)

- [ ] **Step 4: Run all short tests**

Run: `go test -short ./... -v`
Expected: all tests `PASS` — domain tests, pipeline unit tests, handler tests, middleware tests; integration tests skipped.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/server.go internal/app/wire.go
git commit -m "feat(app): wire sessions, images, and pipeline into composition root"
```

---

## Self-Review

### Spec Coverage

| Spec Section | Requirement | Task |
|---|---|---|
| 4.2 | POST /sessions → 201 | Task 9 |
| 4.2 | GET /sessions (paginated) | Task 9 |
| 4.2 | GET /sessions/:id (with image_count) | Task 9 + Task 2 (CountBySession) |
| 4.2 | POST /sessions/:id/close → 204 | Task 9 |
| 4.2 | SessionWindow middleware (410 on expired/closed) | Task 8 |
| 4.3 | POST /sessions/:id/images (multipart, validate, 202) | Task 10 |
| 4.3 | GET /sessions/:id/images (job status) | Task 10 + Task 2 (FindByImageID) |
| 5.1 | Workers LISTEN/NOTIFY + FOR UPDATE SKIP LOCKED | Task 5 (Claim already exists, LISTEN added) |
| 5.1 | Reaper on startup + interval | Task 5 |
| 5.2 | Step 1: Load image | Task 3 (execute) |
| 5.2 | Step 2: Enhance → persist | Task 3 |
| 5.2 | Step 3: Extract (3 attempts, retry codes) | Task 3 (extractWithRetries) |
| 5.2 | Step 4: Normalize, hash, embed, dedup, create/link | Task 3 (per-question loop) |
| 5.2 | Step 5: Verify (best-effort, skip if all deduped) | Task 3 |
| 5.2 | Step 6: Job → done | Task 3 (Run → Complete) |
| 5.3 | ImageEnhancer, AIExtractor, AIVerifier, AIEmbedder ports | Task 1 |
| 5.4 | Config (all fields) | Already exists from Plan 1 |
| 6 | Project layout (pipeline/, dto/, handlers/) | All tasks |

### Placeholder Scan

- No TBD, TODO, or "add error handling" found.
- Every step with code shows the complete implementation.
- All test cases have real assertions, not "write tests for the above".

### Type Consistency

- `Answer{ID, Text}` → used identically in `pipeline.go` (answerTexts/answerIDs) and `pipeline_test.go` (sampleQuestions).
- `ExtractResult{Questions, Error}` → consistent between ports.go, pipeline.go, and test fakes.
- `VerifyResult{Summary{Results}}` → consistent between ports.go, pipeline.go (vr.Summary.Results), and test fakes.
- `dto.SessionResponse`, `dto.SessionDetailResponse`, `dto.ImageUploadResponse`, `dto.ImageResponse` → used identically in handlers and tests.
- `ImageRepo.CountBySession` signature → matches in ports.go, image_repo.go, and all test fakes.
- `JobQueue.FindByImageID` signature → matches in ports.go, job_queue.go, and all test fakes.
- `NewServer` 7-arg signature → matches between server.go and wire.go.
- `NewSessionHandler(sessions, images)`, `NewImageHandler(images, jobs, uploadCfg)` → consistent between handlers and server.go route registration.
- `SessionWindow(sessions storage.SessionRepo)` → used in server.go, tested in middleware_test.go.
- `Pipeline.NewPipeline` 9-arg constructor → consistent between pipeline.go, pipeline_test.go, and worker_test.go.
- `WorkerPool.NewWorkerPool` 6-arg constructor → consistent between worker.go and worker_test.go.
