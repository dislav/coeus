# Free-Response Question Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add free-response (text-answer, no choices) question support to Coeus via a pipeline-inferred `question_type` discriminator (`multiple_choice` | `free_response`), persisted and surfaced through the full stack.

**Architecture:** The type is a deterministic function of extracted choices (`len(choices)==0` → `free_response`), computed once in Go at the `ExtractedQuestion → domain.Question` mapping boundary inside the pipeline, then persisted as an expert-editable `question_type` column. This mirrors the existing `InferChoiceLabeling` / `choice_labeling` precedent exactly — no AI JSON contract changes. The three skill `SKILL.md` files get behavioral (prose) updates only.

**Tech Stack:** Go 1.26.3, Gin, PostgreSQL 16 + pgvector, CGO + libvips (govips), `jackc/pgx/v5`, `invopop/jsonschema` (AI DTO schemas).

**Spec:** `docs/superpowers/specs/2026-07-05-free-response-questions-design.md`

## Global Constraints

- **Go 1.26.3+** (see `go.mod`); CGO + libvips required for builds — use `CGO_ENABLED=1`.
- **Build:** `go build ./...`
- **Vet:** `go vet ./...` (no linter is configured).
- **Unit tests (no external deps):** `go test -short ./...`
- **Integration tests (Docker must be running):** `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s`
- **No CI exists** — all verification is manual via `go test`.
- **`*.md` and `docs/` are gitignored** — use `git add -f` for this plan file and for any markdown.
- **Migrations are embedded** (`//go:embed`) and auto-applied on boot by `RunMigrations` — no separate migrate step. New schema = new numbered file.
- **Skills are embedded** (`//go:embed` in `skills/skills.go`) — editing `SKILL.md` changes runtime behavior with no Go wiring changes.
- **No comments in code** unless replicating an existing comment pattern.
- **Error values:** use existing `domain.ErrValidation`, `domain.ErrNotFound` — do not create new error types.
- **CRITICAL RISK:** the three SELECT constants + three scan functions in `question_repo.go` are positional. Any column-order mismatch silently corrupts reads. Task 2 inserts `question_type` in the same logical position (immediately after `choice_labeling`) in all six places, and the round-trip integration test is the guard.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `internal/domain/question.go` | `QuestionType*` constants, `Type` field on `Question`/`QuestionUpdate`, `InferQuestionType` | 1 |
| `internal/domain/question_test.go` | `TestInferQuestionType` table-driven test | 1 |
| `internal/storage/postgres/migrations/0005_add_question_type.sql` | **new** — `question_type` column + CHECK + backfill | 2 |
| `internal/storage/postgres/question_repo.go` | 3 SELECT constants, 3 scan functions, `Create` INSERT, `UpdateByExpert` SET | 2 |
| `internal/storage/postgres/question_repo_test.go` | `Type` round-trip + `UpdateByExpert` type persistence | 2 |
| `internal/pipeline/pipeline.go` | inference line at mapping boundary + error placeholder fix | 3 |
| `internal/pipeline/pipeline_test.go` | FR/MC inference, error placeholder, verifier-no-reclassify tests | 3 |
| `internal/httpapi/dto/question.go` | `Type` on response DTOs; `AnswerRef.ID` → `omitempty` | 4 |
| `internal/httpapi/dto/question_test.go` | omitempty serialization test | 4 |
| `internal/httpapi/handlers/questions.go` | thread `Type` into 3 response builders | 4 |
| `internal/httpapi/dto/requests.go` | `Type` on request DTOs (required); relax `Choices` binding | 5 |
| `internal/httpapi/handlers/questions.go` | type-conditional validation in `Create`+`Update`; nil→empty normalization; thread `Type` | 5 |
| `internal/httpapi/handlers/questions_test.go` | update all existing bodies + validation matrix tests | 5 |
| `skills/extract-questions-from-image/SKILL.md` | "Recognizing Free-Response Questions" subsection + FR example | 6 |
| `skills/verify-extracted-questions/SKILL.md` | free-response confidence tiers + answer-format guidance | 6 |

**Not touched:** `ports.go` types (`ExtractedQuestion`/`VerifiedQuestion`/`Answer` get no `Type`), `internal/ai/extractor/schema.go`, `internal/ai/verifier/schema.go`, `UpdateFromVerification`, `resolveVerifiedAnswers`, auth, config, `wire.go`, worker pool, reaper, migrations 0001–0004.

---

### Task 1: Domain model — `question_type` constants, `Type` field, `InferQuestionType`

**Files:**
- Modify: `internal/domain/question.go`
- Test: `internal/domain/question_test.go`

**Interfaces:**
- Consumes: nothing (pure domain layer).
- Produces: `domain.QuestionTypeMultipleChoice`, `domain.QuestionTypeFreeResponse` (string constants); `domain.Question.Type` / `domain.QuestionUpdate.Type` (`string` field); `domain.InferQuestionType(choices []string) string`.

- [ ] **Step 1: Write the failing test**

Append to `internal/domain/question_test.go`:

```go
func TestInferQuestionType(t *testing.T) {
	cases := []struct {
		name    string
		choices []string
		want    string
	}{
		{"nil choices", nil, QuestionTypeFreeResponse},
		{"empty choices", []string{}, QuestionTypeFreeResponse},
		{"single choice", []string{"A"}, QuestionTypeMultipleChoice},
		{"many choices", []string{"A", "B", "C"}, QuestionTypeMultipleChoice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InferQuestionType(tc.choices); got != tc.want {
				t.Errorf("InferQuestionType(%v) = %q, want %q", tc.choices, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestInferQuestionType -v`
Expected: FAIL / compile error (`InferQuestionType undefined` and `QuestionTypeFreeResponse undefined`).

- [ ] **Step 3: Add `QuestionType*` constants**

In `internal/domain/question.go`, immediately after the existing `ChoiceLabeling*` constant block (lines 19–22), add:

```go
// QuestionType values — the MC/FR discriminator (spec §3.1).
const (
	QuestionTypeMultipleChoice = "multiple_choice"
	QuestionTypeFreeResponse   = "free_response"
)
```

- [ ] **Step 4: Add `Type` field to `Question`**

In the `Question` struct, insert `Type string` immediately after `ChoiceLabeling` (currently line 46). The struct becomes:

```go
type Question struct {
	ID              string
	Number          int
	Text            string
	TextNorm        string
	TextHash        string
	Choices         []string
	Answers         []string // value-only, shuffle-safe
	ChoiceLabeling  string
	Type            string
	Confidence      float64
	Explanation     string
	Embedding       []float32
	Status          string
	VerifiedAt      *string // ISO timestamp, nil if not verified
	VerifiedBy      *string // user UUID, nil if not verified
	Tags            []string
}
```

- [ ] **Step 5: Add `Type` field to `QuestionUpdate`**

In the `QuestionUpdate` struct, insert `Type string` after `Confidence` (currently line 34). The struct becomes:

```go
type QuestionUpdate struct {
	Status      string
	Choices     []string
	Answers     []string
	Explanation string
	Tags        []string
	Confidence  float64
	Type        string
}
```

- [ ] **Step 6: Add `InferQuestionType` function**

In `internal/domain/question.go`, immediately after `InferChoiceLabeling` (after line 74), add:

```go
// InferQuestionType classifies a question from its extracted choices.
// Empty (or nil) choices means free-response; any non-empty set means
// multiple-choice. Applied once at extraction time; the result is persisted.
func InferQuestionType(choices []string) string {
	if len(choices) == 0 {
		return QuestionTypeFreeResponse
	}
	return QuestionTypeMultipleChoice
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/domain/ -short -v`
Expected: PASS (all domain tests, including `TestInferQuestionType`).

- [ ] **Step 8: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors. (Other packages compile because the new `Type` field has a zero value `""`; existing struct literals using keyed fields are unaffected.)

- [ ] **Step 9: Commit**

```bash
git add internal/domain/question.go internal/domain/question_test.go
git commit -m "feat(domain): add QuestionType discriminator and InferQuestionType"
```

---

### Task 2: Storage — migration 0005 + positional repo edits

This is the **highest-risk task**: the six repo edits are positional. The round-trip integration test (Step 8) is the guard against column-order drift. Insert `question_type` immediately after `choice_labeling` in **every** position so SELECT, scan, and struct field order stay aligned.

**Requires Docker running** for integration tests.

**Files:**
- Create: `internal/storage/postgres/migrations/0005_add_question_type.sql`
- Modify: `internal/storage/postgres/question_repo.go`
- Test: `internal/storage/postgres/question_repo_test.go`

**Interfaces:**
- Consumes: `domain.Question.Type`, `domain.QuestionUpdate.Type` (from Task 1).
- Produces: persisted `question_type` column readable through `FindByID`, `FindExpertByID`, `FindForUserByID`, `ListForSession`, `ListForModerationExpert`; writable through `Create` and `UpdateByExpert`.

- [ ] **Step 1: Create the migration**

Create `internal/storage/postgres/migrations/0005_add_question_type.sql`:

```sql
-- 0005_add_question_type.sql
-- Explicit MC/FR discriminator. Inferred once at extraction time from
-- len(choices); mirrors the choice_labeling precedent. Editable by experts.
ALTER TABLE questions
    ADD COLUMN question_type text NOT NULL DEFAULT 'multiple_choice'
    CHECK (question_type IN ('multiple_choice', 'free_response'));

-- Backfill: existing non-error rows with empty choices are free-response.
-- Error rows (status = 'error') are failure placeholders, not FR questions;
-- they keep the default 'multiple_choice'.
UPDATE questions
SET question_type = 'free_response'
WHERE choices = '[]'::jsonb AND status <> 'error';
```

- [ ] **Step 2: Verify the migration is picked up by the embed**

Run: `go build ./internal/storage/postgres/`
Expected: no errors. (The `//go:embed migrations/*.sql` glob in `migrations.go` auto-includes the new file.)

- [ ] **Step 3: Edit `questionSelectBase` — add `q.question_type`**

In `internal/storage/postgres/question_repo.go`, find the `questionSelectBase` constant (line 409). Add `q.question_type,` on the same line as `q.choice_labeling,`:

```go
const questionSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling, q.question_type,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status
	FROM questions q`
```

- [ ] **Step 4: Edit `questionExpertSelectBase` — add `q.question_type`**

Find `questionExpertSelectBase` (line 422). Add `q.question_type,` after `q.choice_labeling,`:

```go
const questionExpertSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling, q.question_type,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status,
	       (SELECT sq.image_id FROM session_questions sq
	           WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1) AS image_id,
	       COALESCE((SELECT im.verification_report IS NOT NULL
	          FROM session_questions sq JOIN images im ON im.id = sq.image_id
	          WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1), false) AS has_verification_report
	FROM questions q`
```

- [ ] **Step 5: Edit `questionWithSessionSelectBase` — add `q.question_type`**

Find `questionWithSessionSelectBase` (line 439). Add `q.question_type,` after `q.choice_labeling,`:

```go
const questionWithSessionSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling, q.question_type,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status,
	       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
	FROM session_questions sq
	JOIN questions q ON q.id = sq.question_id`
```

- [ ] **Step 6: Edit `scanQuestion` — add `&q.Type`**

Find `scanQuestion` (line 450). Add `&q.Type,` immediately after `&q.ChoiceLabeling,` in the `row.Scan(...)` call:

```go
	err := row.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&choices, &answers, &q.ChoiceLabeling, &q.Type,
		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
	)
```

- [ ] **Step 7: Edit `scanQuestionExpert` — add `&q.Type`**

Find `scanQuestionExpert` (line 471). Add `&q.Type,` immediately after `&q.ChoiceLabeling,`:

```go
	if err := row.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&choices, &answers, &q.ChoiceLabeling, &q.Type,
		&q.Confidence, &q.Explanation, &verifiedAt, &verBy, &q.Status,
		&imageID, &hasReport,
	); err != nil {
```

- [ ] **Step 8: Edit `scanQuestionWithSession` — add `&qws.Type`**

Find `scanQuestionWithSession` (line 502). Add `&qws.Type,` immediately after `&qws.ChoiceLabeling,`. Also update the comment on line 500 from "17 columns (13 question + 4 link fields)" to "18 columns (14 question + 4 link fields)":

```go
// scanQuestionWithSession scans the 18 columns (14 question + 4 link fields)
// used by ListForSession and FindForUserByID. Accepts both pgx.Row and pgx.Rows.
func scanQuestionWithSession(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionWithSession, error) {
	qws := &storage.QuestionWithSession{Question: &domain.Question{}}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	if err := row.Scan(
		&qws.ID, &qws.Number, &qws.Text, &qws.TextNorm, &qws.TextHash,
		&choices, &answers, &qws.ChoiceLabeling, &qws.Type,
		&qws.Confidence, &qws.Explanation, &verifiedAt, &verifiedBy, &qws.Status,
		&qws.SessionID, &qws.ImageID, &qws.ExtractedNumber, &qws.ExtractedConfidence,
	); err != nil {
		return nil, fmt.Errorf("scan question with session: %w", err)
	}
```

- [ ] **Step 9: Edit `Create` INSERT — add `question_type` column + `q.Type` arg + empty-default normalization**

The migration's `CHECK` constraint rejects `''`, but the Go zero value for `string` is `""`. Because this INSERT explicitly lists `question_type`, the column's `DEFAULT 'multiple_choice'` does **not** trigger — so callers that construct `domain.Question{}` without setting `Type` (all 15 existing test fixtures, and any future caller that forgets) would hit a CHECK violation. Normalize empty → `multiple_choice` at the top of `Create`, mirroring the column default. This is defense-in-depth: in production the pipeline (via `InferQuestionType`) and the handler (via required binding) always set a non-empty `Type`.

Find the `Create` function (line 26). Add the normalization immediately after the function signature, before the `choicesJSON` marshaling:

```go
func (r *QuestionRepo) Create(ctx context.Context, q *domain.Question) (string, error) {
	if q.Type == "" {
		q.Type = domain.QuestionTypeMultipleChoice
	}
	choicesJSON, _ := json.Marshal(q.Choices)
```

Then update the INSERT — add `question_type` after `choice_labeling` in the column list, renumber VALUES to `$1..$14`, and add `q.Type` after `q.ChoiceLabeling` in the args:

```go
	err := r.pool.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    choices, answers, choice_labeling, question_type, confidence,
		    explanation, embedding, status, verified_at, verified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		choicesJSON, answersJSON, q.ChoiceLabeling, q.Type,
		q.Confidence, q.Explanation, embedding, q.Status,
		q.VerifiedAt, q.VerifiedBy,
	).Scan(&id)
```

- [ ] **Step 10: Edit `UpdateByExpert` UPDATE — add `question_type = $8`**

Find the `UpdateByExpert` UPDATE (line 253). Add `question_type = $8` to the SET clause and `upd.Type` to the end of the args slice (`$7` stays `id`, `$8` is the new type):

```go
	tag, err := tx.Exec(ctx, `
		UPDATE questions
		SET answers = $1, choices = $2, explanation = $3, confidence = $4,
		    status = $5,
		    verified_at = CASE WHEN $5 = 'verified' THEN now() ELSE NULL END,
		    verified_by = CASE WHEN $5 = 'verified' THEN $6::uuid ELSE NULL END,
		    question_type = $8,
		    updated_at = now()
		WHERE id = $7
	`, answersJSON, choicesJSON, upd.Explanation, upd.Confidence, upd.Status, expertID, id, upd.Type)
```

- [ ] **Step 11: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 12: Write the round-trip integration test**

Append to `internal/storage/postgres/question_repo_test.go`:

```go
func TestQuestionRepo_TypeRoundTrip(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		typ     string
		choices []string
	}{
		{"multiple_choice", domain.QuestionTypeMultipleChoice, []string{"a", "b"}},
		{"free_response", domain.QuestionTypeFreeResponse, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &domain.Question{
				Number: 1, Text: tc.name, TextNorm: tc.name,
				TextHash: "type-rt-" + tc.name,
				Choices: tc.choices, Answers: []string{"ans"},
				ChoiceLabeling: "letter", Type: tc.typ, Confidence: 0.9,
				Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
			}
			id, err := repo.Create(ctx, q)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			// scanQuestion path (FindByID)
			got, err := repo.FindByID(ctx, id)
			if err != nil {
				t.Fatalf("FindByID: %v", err)
			}
			if got.Type != tc.typ {
				t.Errorf("FindByID Type = %q, want %q", got.Type, tc.typ)
			}

			// scanQuestionExpert path (FindExpertByID)
			ev, err := repo.FindExpertByID(ctx, id)
			if err != nil {
				t.Fatalf("FindExpertByID: %v", err)
			}
			if ev.Type != tc.typ {
				t.Errorf("FindExpertByID Type = %q, want %q", ev.Type, tc.typ)
			}
		})
	}
}

func TestQuestionRepo_UpdateByExpertType(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, err := userRepo.Create(ctx, "type-upd@test.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "type-upd-hash",
		Choices: []string{"a", "b"}, Answers: []string{"a"}, ChoiceLabeling: "letter",
		Type: domain.QuestionTypeMultipleChoice, Confidence: 0.9,
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	id, err := repo.Create(ctx, q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := domain.QuestionUpdate{
		Status:      domain.QuestionStatusVerified,
		Choices:     []string{},
		Answers:     []string{"42"},
		Explanation: "now free-response",
		Tags:        []string{},
		Confidence:  0.95,
		Type:        domain.QuestionTypeFreeResponse,
	}
	if err := repo.UpdateByExpert(ctx, id, upd, user.ID); err != nil {
		t.Fatalf("UpdateByExpert: %v", err)
	}

	ev, err := repo.FindExpertByID(ctx, id)
	if err != nil {
		t.Fatalf("FindExpertByID after update: %v", err)
	}
	if ev.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type after update = %q, want free_response", ev.Type)
	}
}
```

- [ ] **Step 13: Run the integration tests (Docker required)**

Run: `go test ./internal/storage/postgres/ -run 'TestQuestionRepo_Type' -v -timeout 180s`
Expected: PASS for both `TestQuestionRepo_TypeRoundTrip` (subtests `multiple_choice` and `free_response`) and `TestQuestionRepo_UpdateByExpertType`.

> If any subtest fails with a scan error or wrong `Type` value, a SELECT constant and its scan function are out of alignment — re-check Steps 3–8 that `q.question_type` and `&q.Type`/`&qws.Type` appear in the same position in every pair.

- [ ] **Step 14: Run the full storage integration suite to confirm no regressions**

Run: `go test ./internal/storage/postgres/ -timeout 180s`
Expected: PASS (existing tests still pass — existing fixture questions don't set `Type`, so the `Create` normalization from Step 9 defaults them to `multiple_choice`, which is correct since they all have non-empty choices).

- [ ] **Step 15: Commit**

```bash
git add internal/storage/postgres/migrations/0005_add_question_type.sql \
        internal/storage/postgres/question_repo.go \
        internal/storage/postgres/question_repo_test.go
git commit -m "feat(storage): add question_type column and repo round-trip"
```

---

### Task 3: Pipeline — inference at mapping boundary + error placeholder fix

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Test: `internal/pipeline/pipeline_test.go`

**Interfaces:**
- Consumes: `domain.InferQuestionType`, `domain.QuestionTypeMultipleChoice` (from Task 1).
- Produces: every `domain.Question` created by the pipeline has a non-empty `Type`.

- [ ] **Step 1: Add the inference line to the extraction mapping**

In `internal/pipeline/pipeline.go`, find the `q := &domain.Question{...}` literal inside the per-question loop (line 198). Add the `Type` field after `ChoiceLabeling`:

```go
		q := &domain.Question{
			Number:          eq.Number,
			Text:            eq.Text,
			TextNorm:        norm,
			TextHash:        hash,
			Choices:         answerTexts(eq.Choices),
			Answers:         answerTexts(eq.Answers),
			ChoiceLabeling:  domain.InferChoiceLabeling(answerIDs(eq.Choices)),
			Type:            domain.InferQuestionType(answerTexts(eq.Choices)),
			Status:          domain.QuestionStatusModeration,
			Embedding:       embedding,
			Tags:            append([]string{"ai-generated"}, eq.Tags...),
		}
```

- [ ] **Step 2: Fix the error placeholder type**

In `internal/pipeline/pipeline.go`, find the `handleExtractionFailure` placeholder literal (line 306). Add `Type: domain.QuestionTypeMultipleChoice,` so failure placeholders are never classified as free-response:

```go
	q := &domain.Question{
		Number:   0,
		Text:     "Extraction failed: " + code,
		TextNorm: "extraction failed " + code,
		TextHash: hash,
		Type:     domain.QuestionTypeMultipleChoice,
		Status:   domain.QuestionStatusError,
		Tags:     []string{"extraction-failed"},
	}
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 4: Write the pipeline tests**

Append to `internal/pipeline/pipeline_test.go`:

```go
func TestPipeline_FreeResponseInference(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
		{Number: 1, Text: "v = ___ м/с", Choices: nil, Answers: nil},
	}}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	if err := p.Run(context.Background(), job()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response", qRepo.created[0].Type)
	}
}

func TestPipeline_MultipleChoiceInference(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	if err := p.Run(context.Background(), job()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Type != domain.QuestionTypeMultipleChoice {
		t.Errorf("Type = %q, want multiple_choice", qRepo.created[0].Type)
	}
}

func TestPipeline_ErrorPlaceholderIsMultipleChoice(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	if err := p.Run(context.Background(), job()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 placeholder, got %d", len(qRepo.created))
	}
	q := qRepo.created[0]
	if q.Type != domain.QuestionTypeMultipleChoice {
		t.Errorf("placeholder Type = %q, want multiple_choice", q.Type)
	}
	if q.Status != domain.QuestionStatusError {
		t.Errorf("placeholder Status = %q, want error", q.Status)
	}
}

func TestPipeline_VerifyDoesNotReclassifyType(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
		{Number: 1, Text: "2+2?", Choices: nil, Answers: nil},
	}}}
	ver := &fakeVerifier{result: VerifyResult{
		Summary: VerificationSummary{Results: []VerifiedQuestion{
			{Index: 0, Answers: []Answer{{Text: "4"}}, Confidence: 0.9, Explanation: "ok"},
		}},
	}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, ver, &fakeEmbedder{embedding: []float32{0.1}})

	if err := p.Run(context.Background(), job()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response (verifier must not reclassify)", qRepo.created[0].Type)
	}
	if len(qRepo.updatedFromVer) != 1 {
		t.Fatalf("expected 1 verification update, got %d", len(qRepo.updatedFromVer))
	}
}
```

- [ ] **Step 5: Run pipeline tests**

Run: `go test ./internal/pipeline/ -run 'TestPipeline_(FreeResponse|MultipleChoice|ErrorPlaceholder|VerifyDoesNotReclassify)' -v`
Expected: PASS for all four.

- [ ] **Step 6: Run the full pipeline suite to confirm no regressions**

Run: `go test ./internal/pipeline/ -timeout 180s`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/pipeline/pipeline.go internal/pipeline/pipeline_test.go
git commit -m "feat(pipeline): infer question_type at extraction; fix error placeholder type"
```

---

### Task 4: Response DTOs + builders — `Type` field + `AnswerRef.ID` omitempty

This task is **non-breaking**: adding a `Type` field to responses and making `AnswerRef.ID` omitempty do not change existing test outcomes (MC answers always find a match, so their `id` is non-empty and still emitted).

**Files:**
- Modify: `internal/httpapi/dto/question.go`
- Modify: `internal/httpapi/handlers/questions.go`
- Test: `internal/httpapi/dto/question_test.go`

**Interfaces:**
- Consumes: `domain.Question.Type` (from Task 1).
- Produces: `Type` key in `UserQuestionResponse` / `ExpertQuestionResponse` JSON; `id` key omitted from `AnswerRef` when empty.

- [ ] **Step 1: Make `AnswerRef.ID` omitempty**

In `internal/httpapi/dto/question.go`, find `AnswerRef` (line 10). Change the `ID` json tag:

```go
type AnswerRef struct {
	ID    string `json:"id,omitempty"`
	Value string `json:"value"`
}
```

- [ ] **Step 2: Add `Type` to `UserQuestionResponse`**

In `internal/httpapi/dto/question.go`, add `Type string json:"type"` to `UserQuestionResponse` (after `Question`, before `MultipleCorrect`):

```go
type UserQuestionResponse struct {
	ID              string      `json:"id"`
	Number          int         `json:"number"`
	Question        string      `json:"question"`
	Type            string      `json:"type"`
	MultipleCorrect bool        `json:"multiple_correct"`
	Choices         []string    `json:"choices"`
	Answers         []AnswerRef `json:"answers"`
	Status          string      `json:"status"`
	Confidence      float64     `json:"confidence"`
}
```

- [ ] **Step 3: Add `Type` to `ExpertQuestionResponse`**

In `internal/httpapi/dto/question.go`, add `Type string json:"type"` to `ExpertQuestionResponse` immediately after `ChoiceLabeling`:

```go
type ExpertQuestionResponse struct {
	ID                    string   `json:"id"`
	Number                int      `json:"number"`
	Question              string   `json:"question"`
	MultipleCorrect       bool     `json:"multiple_correct"`
	Choices               []string `json:"choices"`
	Answers               []string `json:"answers"`
	ChoiceLabeling        string   `json:"choice_labeling"`
	Type                  string   `json:"type"`
	Confidence            float64  `json:"confidence"`
	Explanation           string   `json:"explanation"`
	Tags                  []string `json:"tags"`
	Status                string   `json:"status"`
	ImageID               string   `json:"image_id"`
	HasVerificationReport bool     `json:"has_verification_report"`
	VerifiedAt            *string  `json:"verified_at"`
	VerifiedBy            *string  `json:"verified_by"`
}
```

- [ ] **Step 4: Thread `Type` into `toUserResponse`**

In `internal/httpapi/handlers/questions.go`, find `toUserResponse` (line 349). Add `Type: qq.Type,`:

```go
func toUserResponse(q *storage.QuestionWithSession) dto.UserQuestionResponse {
	qq := q.Question
	return dto.UserQuestionResponse{
		ID:              qq.ID,
		Number:          q.ExtractedNumber,
		Question:        qq.Text,
		Type:            qq.Type,
		MultipleCorrect: qq.MultipleCorrect(),
		Choices:         qq.Choices,
		Answers:         dto.DeriveAnswerRefs(qq.Choices, qq.Answers, qq.ChoiceLabeling),
		Status:          qq.Status,
		Confidence:      qq.Confidence,
	}
}
```

- [ ] **Step 5: Thread `Type` into `toExpertResponse`**

In `internal/httpapi/handlers/questions.go`, find `toExpertResponse` (line 363). Add `Type: q.Type,` after `ChoiceLabeling`:

```go
	resp := dto.ExpertQuestionResponse{
		ID:                    q.ID,
		Number:                q.Number,
		Question:              q.Text,
		MultipleCorrect:       q.MultipleCorrect(),
		Choices:               q.Choices,
		Answers:               q.Answers,
		ChoiceLabeling:        q.ChoiceLabeling,
		Type:                  q.Type,
		Confidence:            q.Confidence,
		Explanation:           q.Explanation,
		Tags:                  q.Tags,
		Status:                q.Status,
		ImageID:               ev.ImageID,
		HasVerificationReport: ev.HasVerificationReport,
		VerifiedAt:            q.VerifiedAt,
		VerifiedBy:            q.VerifiedBy,
	}
```

- [ ] **Step 6: Thread `Type` into `toExpertResponseFromSession`**

In `internal/httpapi/handlers/questions.go`, find `toExpertResponseFromSession` (line 391). Add `Type: q.Type,` after `ChoiceLabeling`:

```go
	resp := dto.ExpertQuestionResponse{
		ID:              q.ID,
		Number:          qws.ExtractedNumber,
		Question:        q.Text,
		MultipleCorrect: q.MultipleCorrect(),
		Choices:         q.Choices,
		Answers:         q.Answers,
		ChoiceLabeling:  q.ChoiceLabeling,
		Type:            q.Type,
		Confidence:      q.Confidence,
		Explanation:     q.Explanation,
		Tags:            q.Tags,
		Status:          q.Status,
		ImageID:         qws.ImageID,
		VerifiedAt:      q.VerifiedAt,
		VerifiedBy:      q.VerifiedBy,
	}
```

- [ ] **Step 7: Write the omitempty serialization test**

In `internal/httpapi/dto/question_test.go`, add the imports `"encoding/json"` and `"strings"` to the import block, then append:

```go
func TestAnswerRef_IDOmitEmptyForFreeResponse(t *testing.T) {
	// Free-response: empty choices → idForValue returns "" → id omitted.
	refs := DeriveAnswerRefs([]string{}, []string{"2 м/с²"}, domain.ChoiceLabelingLetter)
	out, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), `"id"`) {
		t.Errorf("expected no id key for FR answer, got %s", out)
	}
	if !strings.Contains(string(out), `"value":"2 м/с²"`) {
		t.Errorf("expected value in %s", out)
	}
}

func TestAnswerRef_IDPresentForMultipleChoice(t *testing.T) {
	// MC: answer found in choices → non-empty id → id still emitted.
	refs := DeriveAnswerRefs([]string{"A", "B"}, []string{"A"}, domain.ChoiceLabelingLetter)
	out, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `"id":"A"`) {
		t.Errorf("expected id:A for MC answer, got %s", out)
	}
}
```

- [ ] **Step 8: Build, vet, and run DTO + handler tests**

Run: `go build ./... && go vet ./... && go test -short ./internal/httpapi/...`
Expected: PASS (existing tests unaffected; new serialization tests pass).

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/dto/question.go internal/httpapi/dto/question_test.go \
        internal/httpapi/handlers/questions.go
git commit -m "feat(httpapi): expose question type in responses; omitempty AnswerRef.ID"
```

---

### Task 5: Request DTOs + handler validation — required `Type`, type-conditional validation

This is the **breaking-change task**. Adding `binding:"required"` on `Type` and relaxing `Choices` means every existing test request body must add `"type":"multiple_choice"`, and MC tests using a single choice must grow to 2+ choices (the new MC rule requires `len(choices) >= 2`).

**Files:**
- Modify: `internal/httpapi/dto/requests.go`
- Modify: `internal/httpapi/handlers/questions.go`
- Test: `internal/httpapi/handlers/questions_test.go`

**Interfaces:**
- Consumes: `domain.QuestionTypeMultipleChoice`, `domain.QuestionTypeFreeResponse`, `domain.QuestionUpdate.Type`, `domain.Question.Type` (from Task 1).
- Produces: `POST /api/v1/questions` and `PUT /api/v1/questions/:id` accept and persist `type`; reject type/choices mismatches with 400.

- [ ] **Step 1: Add `Type` to `UpdateQuestionRequest` + relax `Choices` binding**

In `internal/httpapi/dto/requests.go`, find `UpdateQuestionRequest` (line 12). Add `Type` and change the `Choices` binding from `required,min=1,dive,required` to `dive,required`:

```go
type UpdateQuestionRequest struct {
	Status      string   `json:"status"      binding:"required,oneof=moderation verified error"`
	Type        string   `json:"type"        binding:"required,oneof=multiple_choice free_response"`
	Choices     []string `json:"choices"     binding:"dive,required"`
	Answers     []string `json:"answers"     binding:"required,min=1,dive,required"`
	Explanation string   `json:"explanation"`
	Tags        []string `json:"tags,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
}
```

- [ ] **Step 2: Add `Type` to `CreateQuestionRequest` + relax `Choices` binding**

In `internal/httpapi/dto/requests.go`, find `CreateQuestionRequest` (line 24). Add `Type` and change the `Choices` binding from `required,min=2` to `dive,required`:

```go
type CreateQuestionRequest struct {
	Question        string   `json:"question" binding:"required"`
	Type            string   `json:"type" binding:"required,oneof=multiple_choice free_response"`
	Choices         []string `json:"choices" binding:"dive,required"`
	Answers         []string `json:"answers" binding:"required,min=1"`
	ChoiceLabeling  string   `json:"choice_labeling"`
	Explanation     string   `json:"explanation"`
	Tags            []string `json:"tags"`
	Confidence      *float64 `json:"confidence"`
}
```

- [ ] **Step 3: Replace `Update` validation with type-conditional logic**

In `internal/httpapi/handlers/questions.go`, find the `Update` handler. Replace the `answersSubsetOfChoices` block (lines 169–174) with the type-conditional switch. The old code:

```go
	// Struct-level rule: answers must be a subset of choices, matched by exact,
	// case-sensitive Go string equality (spec §3.2.3, decision #7).
	if !answersSubsetOfChoices(req.Answers, req.Choices) {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
```

becomes:

```go
	// Structural rules are type-conditional (spec §3.5.4). Binding guarantees:
	//   - req.Type is one of {multiple_choice, free_response}
	//   - every present choice is non-empty
	//   - len(req.Answers) >= 1
	switch req.Type {
	case domain.QuestionTypeMultipleChoice:
		if len(req.Choices) < 2 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		if !answersSubsetOfChoices(req.Answers, req.Choices) {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	case domain.QuestionTypeFreeResponse:
		if len(req.Choices) != 0 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	}
```

- [ ] **Step 4: Thread `Type` into the `Update` domain object**

In the `Update` handler, find the `domain.QuestionUpdate` literal (line 197). Add `Type: req.Type,`:

```go
	upd := domain.QuestionUpdate{
		Status:      req.Status,
		Type:        req.Type,
		Choices:     req.Choices,
		Answers:     req.Answers,
		Explanation: req.Explanation,
		Tags:        req.Tags,
		Confidence:  confidence,
	}
```

- [ ] **Step 5: Add type-conditional validation + nil normalization to `Create`**

In the `Create` handler, immediately after the `ChoiceLabeling` validation block (after line 263, before `confidence := 0.99`), insert:

```go
	// Type-conditional structural validation (spec §3.5.4).
	switch req.Type {
	case domain.QuestionTypeMultipleChoice:
		if len(req.Choices) < 2 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		if !answersSubsetOfChoices(req.Answers, req.Choices) {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	case domain.QuestionTypeFreeResponse:
		if len(req.Choices) != 0 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	}
```

Then, immediately before the `domain.Question` literal (before line 316), add the nil→empty normalization:

```go
	choices := req.Choices
	if choices == nil {
		choices = []string{}
	}
```

- [ ] **Step 6: Thread `Type` + normalized `choices` into the `Create` domain object**

In the `Create` handler, find the `domain.Question` literal (line 316). Change `Choices: req.Choices` to `Choices: choices` and add `Type: req.Type,` after `ChoiceLabeling`:

```go
	q := &domain.Question{
		Number:          0,
		Text:            req.Question,
		TextNorm:        norm,
		TextHash:        hash,
		Choices:         choices,
		Answers:         req.Answers,
		ChoiceLabeling:  choiceLabeling,
		Type:            req.Type,
		Confidence:      confidence,
		Explanation:     req.Explanation,
		Embedding:       embedding,
		Status:          domain.QuestionStatusVerified,
		VerifiedAt:      &now,
		VerifiedBy:      &expertID,
		Tags:            tags,
	}
```

- [ ] **Step 7: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 8: Update `fakeQuestionRepo.updateArgs` to capture `Type`**

In `internal/httpapi/handlers/questions_test.go`, find the `fakeQuestionRepo` struct's `updateArgs` field (around line 35). Add `typ string` to the struct:

```go
	updateArgs struct {
		id, expertID     string
		answers, choices []string
		explanation      string
		conf             float64
		tags             []string
		typ              string
	}
```

Then find the `UpdateByExpert` fake method (around line 87) and add `f.updateArgs.typ = upd.Type`:

```go
func (f *fakeQuestionRepo) UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error {
	f.updateCalled = true
	f.updateArgs.id, f.updateArgs.expertID = id, expertID
	f.updateArgs.answers, f.updateArgs.choices = upd.Answers, upd.Choices
	f.updateArgs.explanation, f.updateArgs.conf, f.updateArgs.tags = upd.Explanation, upd.Confidence, upd.Tags
	f.updateArgs.typ = upd.Type
	if f.updateByExpert != nil {
		return f.updateByExpert(id, upd, expertID)
	}
	return nil
}
```

- [ ] **Step 9: Update the `validUpdateBody()` helper**

In `internal/httpapi/handlers/questions_test.go`, find `validUpdateBody()` (line 737) and add `"type":"multiple_choice"`:

```go
	return `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"explanation":"e","tags":["t"],"confidence":0.9}`
```

- [ ] **Step 10: Update all remaining existing test request bodies**

Run this command to find every test body that needs `"type"` added or single-choice MC bodies fixed:

```bash
grep -n '"choices"' internal/httpapi/handlers/questions_test.go
```

For each line below, apply the described change. The rule: every MC body needs `"type":"multiple_choice"` and **at least 2 choices**; every FR body needs `"type":"free_response"` and `choices:[]`.

**PUT (Update) bodies — single-choice → must grow to 2 choices + add type:**

| Line | Current body fragment | Change to |
|---|---|---|
| 291 | `{"status":"verified","answers":["X"],"choices":["X"]}` | `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}` |
| 321 | `{"status":"verified","answers":["X"],"choices":["X"]}` | `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}` |
| 334 | `{"status":"verified","answers":["X"],"choices":["X"]}` | `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}` |
| 400 | `{"status":"verified","choices":["A"],"answers":["a"]}` | `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["a"]}` |
| 408 | `{"status":"verified","choices":["A"],"answers":["A"],"confidence":1.5}` | `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"confidence":1.5}` |
| 414 | `{"status":"verified","choices":["A"],"answers":["A"],"confidence":-0.5}` | `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"confidence":-0.5}` |
| 427 | `{"status":"moderation","choices":["A"],"answers":["A"],"tags":%s}` | `{"status":"moderation","type":"multiple_choice","choices":["A","B"],"answers":["A"],"tags":%s}` |
| 446 | `{"status":"verified","choices":["A"],"answers":["A"]}` | `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"]}` |
| 467 | `{"status":"moderation","choices":["A"],"answers":["A"]}` | `{"status":"moderation","type":"multiple_choice","choices":["A","B"],"answers":["A"]}` |

**PUT (Update) bodies — already 2 choices, just add type:**

| Line | Change |
|---|---|
| 395 | `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["C"]}` |

**POST (Create) bodies — add type (all already have ≥2 choices or are expected-400):**

| Line | Current body fragment | Change to |
|---|---|---|
| 567 | `"choices":["3","4","5"],` (multi-line body) | add `"type":"multiple_choice",` to the same JSON object |
| 630 | `{"question":"dup","choices":["a","b"],"answers":["a"]}` | `{"question":"dup","type":"multiple_choice","choices":["a","b"],"answers":["a"]}` |
| 667 | `{"question":"q","choices":["a","b"],"answers":["a"]}` | `{"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"]}` |
| 686 | `{"question":"q","choices":["a","b"],"answers":["a"]}` | `{"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"]}` |
| 699 | `{"question":"q","choices":["a","b"],"answers":["a"],"choice_labeling":"emoji"` | `{"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"],"choice_labeling":"emoji"` |
| 708 | `{"question":"q","choices":["a","b"],"answers":["a"],"confidence":...}` | `{"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"],"confidence":...}` |
| 719 | `{"question":"q","choices":["only"],"answers":["a"]}` | `{"question":"q","type":"multiple_choice","choices":["only"],"answers":["a"]}` — keep 1 choice; the 400 now comes from the handler's `len(choices) < 2` check (see note below) |

> **Line 719 note:** This test (`TestCreate_MissingRequiredFields400` or similar) expects a 400 response. The original body had 1 choice, which was rejected by the old `binding:"required,min=2"` tag. Under the new design that binding is relaxed, so the 400 must come from the handler's type-conditional `len(choices) < 2` check instead. **Do NOT grow the choices to 2** — that would make the body valid and return 201, breaking the 400 assertion. Instead, keep `"choices":["only"]` and add `"type":"multiple_choice"` so the handler rejects it. Update the test name/comment to reflect the new rejection reason (single-choice MC rejected by handler, not by binding).

- [ ] **Step 11: Run the handler tests to find any remaining bodies**

Run: `go test -short ./internal/httpapi/handlers/ -run 'TestUpdate|TestCreate' -v`
Expected: all pre-existing tests PASS. If any fail with `400` where `200`/`201` was expected, that test's body is missing `"type"` or has `< 2` choices for MC — fix per the table above.

- [ ] **Step 12: Write the validation matrix tests**

Append to `internal/httpapi/handlers/questions_test.go`:

```go
func TestUpdate_TypeConditionalValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			"mc valid",
			`{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`,
			http.StatusOK,
		},
		{
			"mc one choice",
			`{"status":"verified","type":"multiple_choice","choices":["A"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"mc answer not in choices",
			`{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["C"]}`,
			http.StatusBadRequest,
		},
		{
			"fr valid",
			`{"status":"verified","type":"free_response","choices":[],"answers":["42"]}`,
			http.StatusOK,
		},
		{
			"fr with choices",
			`{"status":"verified","type":"free_response","choices":["A"],"answers":["42"]}`,
			http.StatusBadRequest,
		},
		{
			"missing type",
			`{"status":"verified","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"invalid type",
			`{"status":"verified","type":"matching","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuestionRepo{
				updateByExpert: func(string, domain.QuestionUpdate, string) error { return nil },
				expertByID: func(string) (*storage.QuestionExpertView, error) {
					return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
				},
			}
			r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
			w := doReq(t, r, "PUT", "/api/v1/questions/q1", tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("got %d want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestUpdate_ForwardsTypeToRepo(t *testing.T) {
	var got domain.QuestionUpdate
	q := &fakeQuestionRepo{
		updateByExpert: func(_ string, upd domain.QuestionUpdate, _ string) error {
			got = upd
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1",
		`{"status":"verified","type":"free_response","choices":[],"answers":["42"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if got.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("forwarded Type = %q, want free_response", got.Type)
	}
}

func TestCreate_TypeConditionalValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			"mc valid",
			`{"question":"q","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`,
			http.StatusCreated,
		},
		{
			"mc one choice",
			`{"question":"q","type":"multiple_choice","choices":["A"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"mc answer not in choices",
			`{"question":"q","type":"multiple_choice","choices":["A","B"],"answers":["C"]}`,
			http.StatusBadRequest,
		},
		{
			"fr valid",
			`{"question":"q","type":"free_response","choices":[],"answers":["42"]}`,
			http.StatusCreated,
		},
		{
			"fr with choices",
			`{"question":"q","type":"free_response","choices":["A"],"answers":["42"]}`,
			http.StatusBadRequest,
		},
		{
			"fr answers empty (binding)",
			`{"question":"q","type":"free_response","choices":[],"answers":[]}`,
			http.StatusBadRequest,
		},
		{
			"missing type",
			`{"question":"q","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuestionRepo{
				create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
				expertByID: func(string) (*storage.QuestionExpertView, error) {
					return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
				},
			}
			r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
			w := doReq(t, r, "POST", "/api/v1/questions", tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("%s: got %d want %d: %s", tc.name, w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCreate_FreeResponseNormalizesNilChoices(t *testing.T) {
	// Omitting "choices" entirely binds nil in Go; Create must normalize to []string{}
	// so the DB stores '[]' not NULL (spec §3.5.4).
	var captured *domain.Question
	q := &fakeQuestionRepo{
		create: func(_ context.Context, qq *domain.Question) (string, error) {
			captured = qq
			return "q-new", nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	// "choices" key omitted entirely.
	w := doReq(t, r, "POST", "/api/v1/questions", `{"question":"q","type":"free_response","answers":["42"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("Create not called")
	}
	if captured.Choices == nil {
		t.Error("Choices is nil; expected normalized []string{}")
	}
	if captured.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response", captured.Type)
	}
}
```

- [ ] **Step 13: Run the full handler test suite**

Run: `go test -short ./internal/httpapi/handlers/ -v`
Expected: PASS (all pre-existing tests with updated bodies + all new validation matrix tests).

- [ ] **Step 14: Commit**

```bash
git add internal/httpapi/dto/requests.go \
        internal/httpapi/handlers/questions.go \
        internal/httpapi/handlers/questions_test.go
git commit -m "feat(httpapi): required question type with type-conditional validation"
```

---

### Task 6: Skills — behavioral `SKILL.md` updates

The skills are embedded via `//go:embed` in `skills/skills.go` and wired into the `systemPrompt` variables in `internal/ai/extractor/prompt.go` and `internal/ai/verifier/prompt.go`. Editing the `SKILL.md` files changes runtime behavior with **no Go code changes**. No JSON output schema is added.

**Files:**
- Modify: `skills/extract-questions-from-image/SKILL.md`
- Modify: `skills/verify-extracted-questions/SKILL.md`

**Interfaces:**
- Consumes: nothing (pure prose/markdown).
- Produces: improved recognition of free-response questions (extractor) and appropriate confidence tiers for them (verifier).

- [ ] **Step 1: Read both skill files to find insertion points**

Run: `grep -n '^##' skills/extract-questions-from-image/SKILL.md skills/verify-extracted-questions/SKILL.md`
This lists the section headings so the new subsections can be placed alongside the existing MC guidance.

- [ ] **Step 2: Add "Recognizing Free-Response Questions" to the extractor skill**

In `skills/extract-questions-from-image/SKILL.md`, add a new `##` (or `###`) section alongside the existing MC guidance (place it after the main extraction guidance, before any closing notes). Content:

```markdown
## Recognizing Free-Response Questions

Some questions have **no answer choices** — instead they have an **input field**
the solver must fill in. These are free-response questions. Emit them with
`choices: []`.

### Visual signals that indicate free-response (set `choices: []`)

- Underscore runs or blank lines acting as an answer field:
  `Ответ: ______`, `Answer: ____`, a trailing blank line.
- An answer prompt (`Ответ:`, `Answer:`, `=`, `?`) with **no enumerated
  choices following it**.
- Digital form placeholders: `[input]`, `[____]`, an empty text box glyph.
- A gap inside a sentence or equation the solver is meant to fill:
  `v = ___ м/с`, `The capital of France is ___`.

### Guidance

When these signals are present, **confidently emit `choices: []`**. This is a
positive, deliberate recognition — not an absence of data. Keep the surrounding
transcription `confidence` high; the missing choices are expected, not a parse
failure.

If the image shows a pre-filled answer in the input field (e.g. a worked exam),
transcribe it into `answers` as usual. If the field is blank, emit `answers: []`
and let the verifier fill it.

### Free-response example

```json
{
  "number": 7,
  "question": "Чему равно ускорение тела через 2 с?",
  "choices": [],
  "answers": []
}
```
```

- [ ] **Step 3: Add free-response confidence tiers to the verifier skill**

In `skills/verify-extracted-questions/SKILL.md`, add a new section (after the existing confidence/answer guidance). Content:

```markdown
## Free-response confidence tiers

For questions with `choices: []` (free-response), apply these confidence tiers:

- **Short objective answers** — formulas, numbers, single words, names, units
  (the common case). Solve at **confidence ≥ 0.80**.
- **Detailed / subjective answers** — algorithms, proofs, "развернутый ответ",
  multi-sentence explanations. Produce a **best-effort** answer at
  **0.50–0.79**. These cannot be graded automatically; the moderate confidence
  signals "plausible, needs human review."

### Answer format

Produce **exactly what fills the input field** — include units if implied by the
prompt (`2 м/с²`, not `2`), omit surrounding prose. The value must be directly
droppable into the blank.
```

- [ ] **Step 4: Verify both files are well-formed Markdown**

Visually inspect both `SKILL.md` files in an editor or run:
```bash
go test -short ./internal/ai/extractor/ ./internal/ai/verifier/
```
Expected: PASS (the embed compiles; no structural test exists for skill prose, but the packages must still build and their existing tests pass).

- [ ] **Step 5: Schema regression guard — confirm extractor DTO is unchanged**

Confirm the extractor DTO structs in `internal/ai/extractor/schema.go` have **no** `Type` field (the type is pipeline-inferred, never in the AI contract). Grep for struct field definitions only — `^\s*Type\s` matches a field declaration like `Type string` but not type assertions, comments, or method calls:

```bash
grep -n '^\s*Type\s' internal/ai/extractor/schema.go internal/ai/verifier/schema.go
```
Expected: **no output** (no `Type` struct field in either schema file). If any line matches, it means a `Type` field was accidentally added to the AI DTO — remove it. The type must not cross the AI boundary.

- [ ] **Step 6: Commit**

```bash
git add -f skills/extract-questions-from-image/SKILL.md skills/verify-extracted-questions/SKILL.md
git commit -m "feat(skills): free-response recognition and confidence guidance"
```

> Note: `skills/` is **not** gitignored (only `*.md` at the root and `docs/` are — see AGENTS.md), but if `git status` does not show the `SKILL.md` changes, use `git add -f`.

---

## Final verification

After all six tasks are complete, run the full verification suite:

- [ ] **Build:** `go build ./...` — no errors.
- [ ] **Vet:** `go vet ./...` — no errors.
- [ ] **Unit tests:** `go test -short ./...` — all PASS.
- [ ] **Integration tests (Docker required):** `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s` — all PASS.
- [ ] **Schema regression:** `grep -n '^\s*Type\s' internal/ai/extractor/schema.go internal/ai/verifier/schema.go` — no output.
- [ ] **End-to-end smoke (manual, if environment allows):** upload an exam image containing a free-response question; confirm the returned question has `"type":"free_response"`, `"choices":[]`, and an answer with no phantom `"id"` key.
