# Coeus — Questions + Expert HTTP Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the HTTP surface for the `questions` endpoints (role-split list/get/patch) and the `expert` image-access endpoints, plus wire the image-byte cleanup into the expert PATCH transaction — closing Gaps 1–5 of the approved design.

**Architecture:** Thin Gin handlers over the existing `storage.*Repo` ports, following the exact pattern already used by `handlers/sessions.go` and `handlers/images.go` (constructor-injected repos, `errorResponse` for domain-error mapping, inline pagination). DTOs live in `internal/httpapi/dto`. The image-byte cleanup (spec §3.5) is folded into the existing `UpdateByExpert` transaction so it is atomic with the status flip — no new repo method, no post-commit step.

**Tech Stack:** Go 1.26.3, Gin, PGX v5, pgvector. Builds require `CGO_ENABLED=1` + libvips. No Makefile, no CI.

**Spec (authoritative):** `docs/superpowers/specs/2026-06-18-coeus-image-question-analysis-service-design.md` — sections 3.5, 4.4, 4.5, 4.6, 4.7.

---

## Key Decisions (read before starting)

These are necessary consequences of the spec + existing code, not architecture changes. They are locked here so each task is unambiguous.

1. **SessionWindow on `GET /questions?session_id=` -> inline handler check, NOT the middleware.**
   `SessionWindow` (`internal/httpapi/middleware.go`) reads `c.Param("id")` — a **path** param — and assumes the route is `/sessions/:id/...`. On `GET /api/v1/questions` the session id is a **query** param, so the middleware cannot be reused without making it dual-purpose. Instead the `user`-role branch of `QuestionHandler.List` performs the same three checks inline (ownership -> 404; `status != open` -> 410; `expires_at` past -> 410), mirroring the inline ownership check already used in `handlers/sessions.go` `Get`/`Close`. This keeps `SessionWindow` focused on path-param routes. (For `GET /questions/:id` user branch, the scope asks for **ownership only** — no window check — so only the list does the window check.)

2. **Expert response needs `image_id` + `has_verification_report`, which `domain.Question` does not carry.**
   Spec §4.6 expert shape requires `image_id` and `has_verification_report`, but `domain.Question` has neither and no existing repo method returns them. Because dedup means **one canonical question <-> many `session_questions` <-> many images**, "the" `image_id` is picked deterministically as the **first `session_questions` row by `sq.id`** (the earliest extraction). `has_verification_report` is `images.verification_report IS NOT NULL` for that same representative image. Implemented with correlated subqueries (avoids `DISTINCT ON` vs `ORDER BY` conflicts and keeps the existing `ORDER BY q.created_at` queue ordering). This is **additive** data plumbing — existing repo methods and their tests are untouched.

3. **Cleanup-in-tx extends `UpdateByExpert` internally — no interface signature change.**
   Spec §3.5 requires the byte cleanup to run **inside** the PATCH transaction, after the status flip. `UpdateByExpert` already begins/commits its own tx, so the cleanup (lookup linked `image_id`s -> per-image unresolved count -> `UPDATE images SET original=NULL, enhanced=NULL`) is appended inside that tx before `Commit`. The existing `CountUnresolvedForImage` uses `r.pool` (not the tx), so the count SQL is inlined tx-scoped. The `storage.QuestionRepo` interface signature stays the same -> all fakes/tests keep compiling.

4. **PATCH response = re-fetched expert-shape question (200).** Spec §4.4 does not specify a PATCH response body. Returning the updated expert view is the most useful for the moderation UI and reuses `FindExpertByID`. (`204 No Content` would also be spec-compliant; we choose 200 + body.)

5. **PATCH `confidence` omitted -> defaults to `1.0`.** The expert confirms the answer, so a missing `confidence` means full confidence. `*float64` distinguishes omitted from `0`.

6. **User-shape `number` = `session_questions.extracted_number` (the position on the user's exam); expert-shape `number` = `questions.number` (canonical).** Different semantics, both surfaced as `"number"` per spec §4.6.

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/httpapi/dto/question.go` | **Create** | `AnswerRef`, `DeriveAnswerRefs` (+ `indexToLabel`/`indexToLetter`), `UserQuestionResponse`, `ExpertQuestionResponse`, `QuestionListResponse`. Pure, no I/O. |
| `internal/httpapi/dto/question_test.go` | **Create** | Unit tests for `DeriveAnswerRefs` (letter/number labeling, duplicate first-index-wins, missing value). |
| `internal/storage/ports.go` | **Edit** | Add `QuestionExpertView` struct + `FindExpertByID` / `ListForModerationExpert` / `FindForUserByID` on `QuestionRepo`. Additive. |
| `internal/storage/postgres/question_repo.go` | **Edit** | Implement the 3 new methods; later extend `UpdateByExpert` with the in-tx cleanup. |
| `internal/httpapi/handlers/questions.go` | **Create** | `QuestionHandler`: `List` (role-split), `Get` (role-split), `Update` (expert). |
| `internal/httpapi/handlers/questions_test.go` | **Create** | `httptest` tests with fake repos: role gating, ownership, moderation queue default+filter, PATCH success. |
| `internal/httpapi/handlers/expert.go` | **Create** | `ExpertHandler`: `GetImage` (serve bytes), `GetVerificationReport`. |
| `internal/httpapi/handlers/expert_test.go` | **Create** | `httptest` tests: 404 when bytes cleaned, verification-report null case. |
| `internal/httpapi/server.go` | **Edit** | Add `questionRepo` to `NewServer`; register question + expert image routes with correct middleware. |
| `internal/httpapi/server_test.go` | **Create** | Server-level `httptest` for the PATCH `RoleGuard` 403 path (expert-only route). |
| `internal/app/wire.go` | **Edit** | Pass `questionRepo` into `httpapi.NewServer`. |
| `internal/storage/postgres/question_expert_test.go` | **Create** (integration) | PATCH -> cleanup nulls bytes when last unresolved question flips; sibling still unresolved -> bytes retained. Needs Docker. |

---

## Conventions used in every task

- Build: `CGO_ENABLED=1 go build ./...`
- Vet: `go vet ./...`
- Unit tests (no Docker): `go test -short ./...`
- Single package tests: `go test ./internal/httpapi/handlers/ -v` (etc.)
- All **existing** `go test -short ./...` tests must remain green after every task.
- `*.md` and `docs/` are gitignored — the plan file won't show in `git status`. That's expected. Use `git add -f` only if you want to track docs.

---

## Task 1: Question DTOs + answer-id derivation helper + unit tests

**Why first:** Pure code with no dependencies. Every later handler task imports these types. Establishes the read-time `answers[*].id` derivation (spec §4.6) with full test coverage before any handler exists.

**Files:**
- Create: `internal/httpapi/dto/question.go`
- Create: `internal/httpapi/dto/question_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/dto/question_test.go`:

```go
package dto

import (
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestDeriveAnswerRefs_LetterLabeling(t *testing.T) {
	choices := []string{"Fe(OH)2", "Cs2O", "HBr", "Na2CO3", "H2SO4"}
	answers := []string{"HBr", "H2SO4"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{
		{ID: "C", Value: "HBr"},   // index 2 -> C
		{ID: "E", Value: "H2SO4"}, // index 4 -> E
	}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_NumberLabeling(t *testing.T) {
	choices := []string{"A", "B", "C", "D"}
	answers := []string{"A", "C"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingNumber)
	want := []AnswerRef{
		{ID: "1", Value: "A"}, // index 0 -> 1
		{ID: "3", Value: "C"}, // index 2 -> 3
	}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_DuplicateChoiceFirstIndexWins(t *testing.T) {
	// Same text appears twice; first occurrence's index is used (v1 edge case, spec §11).
	choices := []string{"X", "X", "Y"}
	answers := []string{"X"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "A", Value: "X"}} // first X is index 0 -> A
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_MissingValueEmptyID(t *testing.T) {
	choices := []string{"A", "B"}
	answers := []string{"Z"} // not in choices
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "", Value: "Z"}}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_EmptyChoiceText(t *testing.T) {
	// An empty answer string that equals the first (empty) choice matches at
	// index 0 -> "A". Guards against panics/mis-handling of empty strings.
	choices := []string{"", "X"}
	answers := []string{""}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "A", Value: ""}} // "" == choices[0] -> index 0 -> A
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_Empty(t *testing.T) {
	got := DeriveAnswerRefs([]string{"A"}, nil, domain.ChoiceLabelingLetter)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

func TestIndexToLetter_SpreadsheetStyle(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for i, want := range cases {
		if got := indexToLetter(i); got != want {
			t.Errorf("indexToLetter(%d) = %q, want %q", i, got, want)
		}
	}
}

func assertRefsEqual(t *testing.T, got, want []AnswerRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %#v want %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %#v want %#v", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/dto/ -v`
Expected: FAIL / build error — `DeriveAnswerRefs`, `AnswerRef`, `indexToLetter` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/httpapi/dto/question.go`:

```go
package dto

import (
	"strconv"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// AnswerRef is a user-facing answer carrying a display id derived at read time.
type AnswerRef struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// UserQuestionResponse is the user-facing question shape (spec §4.6).
// No explanation; answers carry derived display ids.
type UserQuestionResponse struct {
	ID              string      `json:"id"`
	Number          int         `json:"number"` // session_questions.extracted_number
	Question        string      `json:"question"`
	MultipleCorrect bool        `json:"multiple_correct"`
	Choices         []string    `json:"choices"`
	Answers         []AnswerRef `json:"answers"`
	Status          string      `json:"status"`
	Confidence      float64     `json:"confidence"`
}

// ExpertQuestionResponse is the expert-facing question shape (spec §4.6).
// Full fields incl. explanation, tags, image link, and report presence.
type ExpertQuestionResponse struct {
	ID                    string   `json:"id"`
	Number                int      `json:"number"` // questions.number (canonical)
	Question              string   `json:"question"`
	MultipleCorrect       bool     `json:"multiple_correct"`
	Choices               []string `json:"choices"`
	Answers               []string `json:"answers"` // value-only, as stored
	ChoiceLabeling        string   `json:"choice_labeling"`
	Confidence            float64  `json:"confidence"`
	Explanation           string   `json:"explanation"`
	Tags                  []string `json:"tags"`
	Status                string   `json:"status"`
	ImageID               string   `json:"image_id"`
	HasVerificationReport bool     `json:"has_verification_report"`
	VerifiedAt            *string  `json:"verified_at"`
	VerifiedBy            *string  `json:"verified_by"`
}

// QuestionListResponse wraps a paginated question list (matches SessionListResponse).
// Data is []any because user and expert items have different shapes.
type QuestionListResponse struct {
	Data    []any `json:"data"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
}

// DeriveAnswerRefs maps stored value-only answers to user-facing {id,value} pairs
// using the question's choices and choice_labeling (spec §4.6). The id is the
// index of the value's FIRST occurrence in choices, rendered as a letter
// (0->A, 25->Z, 26->AA, ...) for "letter" labeling, or one-based number
// (0->1) for "number" labeling. A value not present in choices gets an empty id.
// First-index wins on duplicate choice texts (v1 edge case).
func DeriveAnswerRefs(choices, answers []string, labeling string) []AnswerRef {
	out := make([]AnswerRef, 0, len(answers))
	for _, v := range answers {
		out = append(out, AnswerRef{ID: idForValue(choices, v, labeling), Value: v})
	}
	return out
}

// idForValue returns the display id for a single answer value.
func idForValue(choices []string, value, labeling string) string {
	idx := -1
	for i, c := range choices {
		if c == value {
			idx = i
			break // first occurrence wins
		}
	}
	if idx < 0 {
		return ""
	}
	return indexToLabel(idx, labeling)
}

func indexToLabel(i int, labeling string) string {
	if labeling == domain.ChoiceLabelingNumber {
		return strconv.Itoa(i + 1)
	}
	return indexToLetter(i)
}

// indexToLetter renders a 0-based index as a spreadsheet-style column label
// (0->A, 25->Z, 26->AA, ...). Used for "letter" choice labeling.
func indexToLetter(i int) string {
	n := i + 1
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/dto/ -v`
Expected: PASS — all `TestDeriveAnswerRefs_*` and `TestIndexToLetter_*`.

- [ ] **Step 5: Build + vet the whole tree**

Run: `CGO_ENABLED=1 go build ./... && go vet ./...`
Expected: no errors. Existing tests unaffected (pure additive file).

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/dto/question.go internal/httpapi/dto/question_test.go
git commit -m "feat(dto): add question response shapes + answer-id derivation"
```

## Task 2: Repo read-side projections for the questions HTTP surface

**Why now:** The handlers (Tasks 3–4) need `image_id` + `has_verification_report` for the expert shape and an ownership-checked single-question lookup for the user GET. These are additive methods on `QuestionRepo`; existing methods/tests stay intact. (The in-tx cleanup is a separate later edit to `UpdateByExpert`.)

**Files:**
- Edit: `internal/storage/ports.go` (add `QuestionExpertView` + 3 methods to the `QuestionRepo` interface)
- Edit: `internal/storage/postgres/question_repo.go` (implement them)

- [ ] **Step 1: Extend the `QuestionRepo` interface**

In `internal/storage/ports.go`, add the view struct near `QuestionWithSession` and the three methods to the `QuestionRepo` interface.

Add the struct (place it right after the existing `QuestionWithSession` declaration):

```go
// QuestionExpertView is a question joined with a single representative image link,
// for the expert moderation UI. The ImageID is the first session_questions row
// by id (deterministic representative); HasVerificationReport reflects that image.
type QuestionExpertView struct {
	*domain.Question
	ImageID               string
	HasVerificationReport bool
}
```

Add these three methods to the `QuestionRepo` interface (after `UpdateByExpert`):

```go
	// Read-side projections for the HTTP surface.
	FindExpertByID(ctx context.Context, id string) (*QuestionExpertView, error)
	ListForModerationExpert(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*QuestionExpertView, error)
	FindForUserByID(ctx context.Context, questionID, userID string) (*QuestionWithSession, error)
```

- [ ] **Step 2: Implement the three methods**

In `internal/storage/postgres/question_repo.go`, add a shared expert-column prefix and the methods. (`domain.ErrNotFound`, `pgx.ErrNoRows`, `errors`, `fmt`, `json` are already imported in this file.)

Add the column list + join prefix near `questionSelectBase`:

```go
// questionExpertSelectBase is questionSelectBase's column list plus the
// representative image_id and verification_report-presence flag, expressed as
// correlated subqueries so the queue can keep its ORDER BY q.created_at
// (DISTINCT ON would force ORDER BY q.id first).
questionExpertSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.multiple_correct, q.choices, q.answers, q.choice_labeling,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status,
	       (SELECT sq.image_id FROM session_questions sq
	           WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1) AS image_id,
	       (SELECT im.verification_report IS NOT NULL
	          FROM session_questions sq JOIN images im ON im.id = sq.image_id
	          WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1) AS has_verification_report
	FROM questions q`
```

Add the implementation functions (append after `ListForModeration`):

```go
func (r *QuestionRepo) FindExpertByID(ctx context.Context, id string) (*storage.QuestionExpertView, error) {
	row := r.pool.QueryRow(ctx, questionExpertSelectBase+` WHERE q.id = $1`, id)
	ev, err := scanQuestionExpert(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find expert question: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find expert question: %w", err)
	}
	ev.Tags, _ = r.getTags(ctx, ev.ID)
	return ev, nil
}

func (r *QuestionRepo) ListForModerationExpert(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*storage.QuestionExpertView, error) {
	query := questionExpertSelectBase
	args := []interface{}{}
	idx := 1
	query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
	args = append(args, statusFilter)
	idx++
	if tagFilter != "" {
		query += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM question_tags qt
			JOIN tags t ON t.id = qt.tag_id
			WHERE qt.question_id = q.id AND t.name = $%d)`, idx)
		args = append(args, tagFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY q.created_at LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list moderation expert: %w", err)
	}
	defer rows.Close()

	results := make([]*storage.QuestionExpertView, 0)
	for rows.Next() {
		ev, err := scanQuestionExpert(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		ev.Tags, _ = r.getTags(ctx, ev.ID)
		results = append(results, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list moderation expert: %w", err)
	}
	return results, nil
}

// FindForUserByID returns the question only if it is linked to a session owned
// by userID (enforces ownership at the repo level; 404 otherwise). It reuses
// QuestionWithSession and picks the earliest-linked session deterministically.
func (r *QuestionRepo) FindForUserByID(ctx context.Context, questionID, userID string) (*storage.QuestionWithSession, error) {
	query := `
		SELECT q.id, q.number, q.question, q.multiple_correct, q.choices, q.answers,
		       q.choice_labeling, q.confidence, q.status,
		       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
		FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		JOIN sessions s ON s.id = sq.session_id
		WHERE sq.question_id = $1 AND s.user_id = $2
		ORDER BY sq.id
		LIMIT 1`
	row := r.pool.QueryRow(ctx, query, questionID, userID)
	qws, err := scanQuestionWithSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user question: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user question: %w", err)
	}
	return qws, nil
}
```

Now add the scan helper for the expert view. It mirrors the existing scan pattern (choices & answers scanned as raw JSON then unmarshaled). The two trailing columns are `image_id` (nullable text) and `has_verification_report` (bool):

```go
// scanQuestionExpert scans the 15 base question columns + image_id + has_report.
// Accepts anything with a Scan(...) method (pgx.Row and pgx.Rows both qualify).
func scanQuestionExpert(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionExpertView, error) {
	var (
		q                domain.Question
		choices, answers []byte
		verifiedAt, verBy *string
	)
	var imageID *string
	var hasReport bool
	if err := row.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&q.MultipleCorrect, &choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verBy, &q.Status,
		&imageID, &hasReport,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(choices, &q.Choices); err != nil {
		return nil, fmt.Errorf("unmarshal choices: %w", err)
	}
	if err := json.Unmarshal(answers, &q.Answers); err != nil {
		return nil, fmt.Errorf("unmarshal answers: %w", err)
	}
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verBy
	ev := &storage.QuestionExpertView{Question: &q, HasVerificationReport: hasReport}
	if imageID != nil {
		ev.ImageID = *imageID
	}
	return ev, nil
}
```

> **Note on `scanQuestionWithSession`:** `ListForUser` already scans these exact 13 columns in its row loop. If a shared `scanQuestionWithSession` helper does not already exist, extract that loop body into:
> ```go
> func scanQuestionWithSession(row interface{ Scan(...any) error }) (*storage.QuestionWithSession, error)
> ```
> so both `ListForUser` and `FindForUserByID` use it. The column count (13) and order must match the SELECT above. Grep first: `rg "func scanQuestionWithSession" internal/storage/postgres/question_repo.go`.

- [ ] **Step 3: Build + vet**

Run: `CGO_ENABLED=1 go build ./... && go vet ./...`
Expected: clean build. The `storage` package import is already present in `question_repo.go` (`storage.QuestionWithSession` is used by `ListForUser`).

- [ ] **Step 4: Run existing tests (must stay green)**

Run: `go test -short ./...`
Expected: all pass. (The new methods are untested until the integration test in Task 7 — they need a DB. The handler tests in Tasks 3–4 exercise them via fakes.)

- [ ] **Step 5: Commit**

```bash
git add internal/storage/ports.go internal/storage/postgres/question_repo.go
git commit -m "feat(storage): add expert/user question read projections"
```

## Task 3: Questions handler (Gap 1) + httptest tests

**Files:**
- Create: `internal/httpapi/handlers/questions.go`
- Create: `internal/httpapi/handlers/questions_test.go`

- [ ] **Step 1: Write the failing handler tests**

Create `internal/httpapi/handlers/questions_test.go`. These use tiny in-package fakes that satisfy `storage.QuestionRepo` and `storage.SessionRepo`. (Only the methods each test exercises need real bodies.)

```go
package handlers

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
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// --- minimal fakes ---

type fakeQuestionRepo struct {
	expertByID     func(id string) (*storage.QuestionExpertView, error)
	listModeration func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error)
	listForUser    func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
	forUserByID    func(qid, uid string) (*storage.QuestionWithSession, error)
	updateByExpert func(id string, answers, choices []string, explanation string, conf float64, tags []string, expertID string) error
	updateCalled   bool
	updateArgs     struct {
		id, expertID      string
		answers, choices  []string
		explanation       string
		conf              float64
		tags              []string
	}
}

func (f *fakeQuestionRepo) Create(context.Context, *domain.Question) (string, error) { return "", nil }
func (f *fakeQuestionRepo) FindByID(context.Context, string) (*domain.Question, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeQuestionRepo) FindExact(context.Context, string) (*domain.Question, error) { return nil, nil }
func (f *fakeQuestionRepo) FindSemantic(context.Context, []float32, float64) (*domain.Question, error) {
	return nil, nil
}
func (f *fakeQuestionRepo) UpdateFromVerification(context.Context, string, float64, string) error {
	return nil
}
func (f *fakeQuestionRepo) ListForUser(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
	return f.listForUser(ctx, sid, st, l, o)
}
func (f *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
func (f *fakeQuestionRepo) ListForModerationExpert(ctx context.Context, st, tag string, l, o int) ([]*storage.QuestionExpertView, error) {
	return f.listModeration(ctx, st, tag, l, o)
}
func (f *fakeQuestionRepo) UpdateByExpert(ctx context.Context, id string, ans, ch []string, expl string, c float64, tags []string, eid string) error {
	f.updateCalled = true
	f.updateArgs.id, f.updateArgs.expertID = id, eid
	f.updateArgs.answers, f.updateArgs.choices = ans, ch
	f.updateArgs.explanation, f.updateArgs.conf, f.updateArgs.tags = expl, c, tags
	if f.updateByExpert != nil {
		return f.updateByExpert(ctx, id, ans, ch, expl, c, tags, eid)
	}
	return nil
}
func (f *fakeQuestionRepo) CountUnresolvedForImage(context.Context, string) (int, error) { return 0, nil }
func (f *fakeQuestionRepo) LinkToSession(context.Context, string, string, string, int, float64) error {
	return nil
}
func (f *fakeQuestionRepo) FindExpertByID(ctx context.Context, id string) (*storage.QuestionExpertView, error) {
	return f.expertByID(ctx, id)
}
func (f *fakeQuestionRepo) FindForUserByID(ctx context.Context, qid, uid string) (*storage.QuestionWithSession, error) {
	return f.forUserByID(ctx, qid, uid)
}

type fakeSessionRepo struct {
	byID func(id string) (*domain.Session, error)
}

func (f *fakeSessionRepo) Create(context.Context, string, int, int) (*domain.Session, error) { return nil, nil }
func (f *fakeSessionRepo) ListByUser(context.Context, string, int, int) ([]*domain.Session, error) {
	return nil, nil
}
func (f *fakeSessionRepo) Close(context.Context, string) error { return nil }
func (f *fakeSessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	return f.byID(ctx, id)
}

// --- helpers ---

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

func doReq(t *testing.T, r http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- tests ---

func TestList_UserRoleRequiresSessionID(t *testing.T) {
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, &fakeSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing session_id: got %d want 400", w.Code)
	}
}

func TestList_UserRoleExpiredSession410(t *testing.T) {
	s := &fakeSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "u1", Status: "open", ExpiresAt: "2000-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusGone {
		t.Fatalf("expired session: got %d want 410", w.Code)
	}
}

func TestList_UserRoleNotOwner404(t *testing.T) {
	s := &fakeSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "other", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("not owner: got %d want 404", w.Code)
	}
}

func TestList_ExpertModerationDefaultAndFilter(t *testing.T) {
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			if status != "moderation" {
				t.Errorf("default status: got %q want moderation", status)
			}
			return []*storage.QuestionExpertView{{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0]["image_id"] != "img1" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}

	// tag filter path
	var gotTag string
	q.listModeration = func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
		gotTag = tag
		return nil, nil
	}
	_ = doReq(t, r, "GET", "/api/v1/questions?tag=chemistry", "")
	if gotTag != "chemistry" {
		t.Fatalf("tag filter: got %q want chemistry", gotTag)
	}
}

func TestList_UserRoleForwardsStatusParam(t *testing.T) {
	var gotStatus string
	q := &fakeQuestionRepo{
		listForUser: func(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
			gotStatus = st
			return nil, nil
		},
	}
	s := &fakeSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "u1", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", q, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1&status=verified", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotStatus != "verified" {
		t.Fatalf("status forwarded: got %q want %q", gotStatus, "verified")
	}
}

func TestGet_UserNotOwner404(t *testing.T) {
	q := &fakeQuestionRepo{forUserByID: func(string, string) (*storage.QuestionWithSession, error) {
		return nil, domain.ErrNotFound
	}}
	r := newQuestionRouter("user", "u1", q, &fakeSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestGet_UserShapeHasDerivedAnswerIDs(t *testing.T) {
	q := &fakeQuestionRepo{forUserByID: func(string, string) (*storage.QuestionWithSession, error) {
		return &storage.QuestionWithSession{
			Question:        &domain.Question{ID: "q1", Text: "q", Choices: []string{"A", "B", "C"}, Answers: []string{"C"}, ChoiceLabeling: "letter", Status: "moderation", Confidence: 0.5},
			ExtractedNumber: 2,
		}, nil
	}}
	r := newQuestionRouter("user", "u1", q, &fakeSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["number"].(float64) != 2 {
		t.Errorf("number: want 2 (extracted), got %v", body["number"])
	}
	ans := body["answers"].([]any)[0].(map[string]any)
	if ans["id"] != "C" || ans["value"] != "C" {
		t.Errorf("derived answer id wrong: %v", ans)
	}
	if _, hasExpl := body["explanation"]; hasExpl {
		t.Errorf("user shape must not expose explanation")
	}
}

func TestUpdate_ExpertSuccessCallsRepo(t *testing.T) {
	q := &fakeQuestionRepo{
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: "verified"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if !q.updateCalled {
		t.Fatal("UpdateByExpert was not called")
	}
	if q.updateArgs.expertID != "e1" || q.updateArgs.id != "q1" || q.updateArgs.answers[0] != "X" {
		t.Errorf("unexpected args: %+v", q.updateArgs)
	}
	if q.updateArgs.conf != 1.0 {
		t.Errorf("default confidence: got %v want 1.0", q.updateArgs.conf)
	}
}

func TestUpdate_ExpertInvalidStatus400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"moderation"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestUpdate_ExpertNotFound404(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, []string, []string, string, float64, []string, string) error {
			return domain.ErrNotFound
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestUpdate_ExpertRepoError500(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, []string, []string, string, float64, []string, string) error {
			return errors.New("boom")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/handlers/ -run Question -v`
Expected: FAIL — `NewQuestionHandler`, `QuestionHandler.List/Get/Update` undefined.

- [ ] **Step 3: Implement the handler**

Create `internal/httpapi/handlers/questions.go`:

```go
package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

const (
	roleExpert              = "expert"
	defaultPerPage   = 20
	maxPerPage       = 100
)

// QuestionHandler serves the role-split /api/v1/questions endpoints (spec §4.4).
type QuestionHandler struct {
	questions storage.QuestionRepo
	sessions  storage.SessionRepo
}

func NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo) *QuestionHandler {
	return &QuestionHandler{questions: questions, sessions: sessions}
}

func parsePaging(c *gin.Context) (page, perPage, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ = strconv.Atoi(c.DefaultQuery("per_page", strconv.Itoa(defaultPerPage)))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > maxPerPage {
		perPage = defaultPerPage
	}
	return page, perPage, (page - 1) * perPage
}

// List — GET /api/v1/questions. Behavior splits by role.
func (h *QuestionHandler) List(c *gin.Context) {
	role := c.GetString("role")
	page, perPage, offset := parsePaging(c)

	if role == roleExpert {
		status := c.DefaultQuery("status", domain.QuestionStatusModeration)
		if status != domain.QuestionStatusModeration && status != domain.QuestionStatusError {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		tag := c.Query("tag")
		items, err := h.questions.ListForModerationExpert(c.Request.Context(), status, tag, perPage, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(err))
			return
		}
		data := make([]any, 0, len(items))
		for _, q := range items {
			data = append(data, toExpertResponse(q))
		}
		c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
		return
	}

	// user role
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	userID := c.GetString("user_id")

	// Inline SessionWindow-equivalent check (session_id is a query param here,
	// so the path-param SessionWindow middleware cannot be reused — see plan
	// Decision #1). Not-found and wrong-owner both 404.
	sess, err := h.sessions.FindByID(c.Request.Context(), sessionID)
	if err != nil || sess.UserID != userID {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	if sess.Status != domain.SessionStatusOpen {
		c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil || time.Now().After(expiresAt) {
		c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
		return
	}

	items, err := h.questions.ListForUser(c.Request.Context(), sessionID, c.Query("status"), perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	data := make([]any, 0, len(items))
	for _, q := range items {
		data = append(data, toUserResponse(q))
	}
	c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
}

// Get — GET /api/v1/questions/:id. Behavior splits by role.
func (h *QuestionHandler) Get(c *gin.Context) {
	id := c.Param("id")
	role := c.GetString("role")

	if role == roleExpert {
		ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		c.JSON(http.StatusOK, toExpertResponse(ev))
		return
	}

	// user: ownership checked at repo level (404 if not linked to caller's session)
	userID := c.GetString("user_id")
	qws, err := h.questions.FindForUserByID(c.Request.Context(), id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	c.JSON(http.StatusOK, toUserResponse(qws))
}

// Update — PATCH /api/v1/questions/:id. Expert only (RoleGuard enforces 403).
func (h *QuestionHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Status      string   `json:"status" binding:"required"`
		Answers     []string `json:"answers"`
		Choices     []string `json:"choices"`
		Explanation string   `json:"explanation"`
		Tags        []string `json:"tags"`
		Confidence  *float64 `json:"confidence"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	if req.Status != domain.QuestionStatusVerified {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	confidence := 1.0 // expert confirms -> full confidence when omitted
	if req.Confidence != nil {
		confidence = *req.Confidence
	}

	expertID := c.GetString("user_id")
	if err := h.questions.UpdateByExpert(c.Request.Context(), id, req.Answers, req.Choices, req.Explanation, confidence, req.Tags, expertID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		slog.Error("update question by expert failed",
			"question_id", id,
			"expert_id", expertID,
			"request_id", c.GetString("request_id"),
			"err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	// Re-fetch the updated expert view for the response (Decision #4).
	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"id": id, "status": domain.QuestionStatusVerified})
		return
	}
	c.JSON(http.StatusOK, toExpertResponse(ev))
}

func toUserResponse(q *storage.QuestionWithSession) dto.UserQuestionResponse {
	qq := q.Question
	return dto.UserQuestionResponse{
		ID:              qq.ID,
		Number:          q.ExtractedNumber, // user sees their exam position
		Question:        qq.Text,
		MultipleCorrect: qq.MultipleCorrect,
		Choices:         qq.Choices,
		Answers:         dto.DeriveAnswerRefs(qq.Choices, qq.Answers, qq.ChoiceLabeling),
		Status:          qq.Status,
		Confidence:      qq.Confidence,
	}
}

func toExpertResponse(ev *storage.QuestionExpertView) dto.ExpertQuestionResponse {
	q := ev.Question
	resp := dto.ExpertQuestionResponse{
		ID:                    q.ID,
		Number:                q.Number, // canonical
		Question:              q.Text,
		MultipleCorrect:       q.MultipleCorrect,
		Choices:               q.Choices,
		Answers:               q.Answers, // value-only
		ChoiceLabeling:        q.ChoiceLabeling,
		Confidence:            q.Confidence,
		Explanation:           q.Explanation,
		Tags:                  q.Tags,
		Status:                q.Status,
		ImageID:               ev.ImageID,
		HasVerificationReport: ev.HasVerificationReport,
		VerifiedAt:            q.VerifiedAt,
		VerifiedBy:            q.VerifiedBy,
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}
```

> **Role constant check:** the handler uses a local `roleExpert = "expert"` constant rather than importing a `domain` role const. `domain.QuestionStatusModeration`/`QuestionStatusError`/`QuestionStatusVerified` and `domain.SessionStatusOpen` already exist (confirmed in `domain/question.go` / `domain/session.go`). If `domain` exports a role constant, prefer it; otherwise the local const matches the JWT claim string set by `AuthMiddleware`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/handlers/ -run Question -v`
Expected: all PASS.

- [ ] **Step 5: Build + vet + full short suite**

Run: `CGO_ENABLED=1 go build ./... && go vet ./... && go test -short ./...`
Expected: green (existing handler tests for auth/sessions/images still pass).

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/handlers/questions.go internal/httpapi/handlers/questions_test.go
git commit -m "feat(httpapi): add questions handler (role-split list/get/patch)"
```

## Task 4: Expert image handler (Gap 2) + httptest tests

**Files:**
- Create: `internal/httpapi/handlers/expert.go`
- Create: `internal/httpapi/handlers/expert_test.go`

- [ ] **Step 1: Write the failing handler tests**

Create `internal/httpapi/handlers/expert_test.go` (reuses `doReq` from `questions_test.go` — same package):

```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type fakeImageRepo struct {
	byID func(id string) (*domain.Image, error)
}

func (f *fakeImageRepo) Create(context.Context, string, []byte, string, int, int) (string, error) {
	return "", nil
}
func (f *fakeImageRepo) ListBySession(context.Context, string) ([]*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepo) UpdateEnhanced(context.Context, string, []byte) error          { return nil }
func (f *fakeImageRepo) UpdateVerificationReport(context.Context, string, []byte) error { return nil }
func (f *fakeImageRepo) UpdateExtractionError(context.Context, string, []byte) error    { return nil }
func (f *fakeImageRepo) CleanBytes(context.Context, string) error                       { return nil }
func (f *fakeImageRepo) CountBySession(context.Context, string) (int, error)            { return 0, nil }
func (f *fakeImageRepo) FindByID(ctx context.Context, id string) (*domain.Image, error) {
	return f.byID(ctx, id)
}

func newExpertRouter(imgs storage.ImageRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewExpertHandler(imgs)
	r.GET("/api/v1/images/:id", h.GetImage)
	r.GET("/api/v1/images/:id/verification-report", h.GetVerificationReport)
	return r
}

func TestGetImage_ServesOriginalBytes(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", Original: []byte("PNGDATA"), Mime: "image/png"}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	if w.Body.String() != "PNGDATA" {
		t.Errorf("body: got %q want PNGDATA", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("content-type: got %q want image/png", w.Header().Get("Content-Type"))
	}
}

func TestGetImage_BytesCleaned404(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", Original: nil, Mime: "image/png"}, nil // cleaned
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cleaned bytes: got %d want 404", w.Code)
	}
}

func TestGetImage_NotFound(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return nil, domain.ErrNotFound
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestGetVerificationReport_Present(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", VerificationReport: []byte(`{"flag":true}`)}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1/verification-report", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	if w.Body.String() != `{"flag":true}` {
		t.Errorf("body: got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q want application/json", ct)
	}
}

func TestGetVerificationReport_NullWhenAbsent(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", VerificationReport: nil}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1/verification-report", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	// Robust null check: unmarshal into interface{} and assert nil, instead of
	// matching the literal "null" byte string (tolerates whitespace/formatting).
	var v interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body not valid JSON %q: %v", w.Body.String(), err)
	}
	if v != nil {
		t.Errorf("body: got %v want null", v)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q want application/json", ct)
	}
}

func TestGetVerificationReport_ImageNotFound404(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return nil, domain.ErrNotFound
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/missing/verification-report", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/handlers/ -run Expert -v`
Expected: FAIL — `NewExpertHandler`, `ExpertHandler.GetImage/GetVerificationReport` undefined.

- [ ] **Step 3: Implement the handler**

Create `internal/httpapi/handlers/expert.go`:

```go
package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// ExpertHandler serves expert-only image access endpoints (spec §4.5).
type ExpertHandler struct {
	images storage.ImageRepo
}

func NewExpertHandler(images storage.ImageRepo) *ExpertHandler {
	return &ExpertHandler{images: images}
}

// GetImage serves the original image bytes with the stored MIME type.
// Returns 404 if the image is missing OR if its bytes were already cleaned
// (original IS NULL) per spec §3.5 / §4.7.
func (h *ExpertHandler) GetImage(c *gin.Context) {
	id := c.Param("id")
	img, err := h.images.FindByID(c.Request.Context(), id)
	if err != nil || img.Original == nil {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	c.Data(http.StatusOK, img.Mime, img.Original)
}

// GetVerificationReport returns the raw verification_report JSON for the image.
// 200 with body "null" when the image exists but has no report; 404 if the
// image itself is missing.
func (h *ExpertHandler) GetVerificationReport(c *gin.Context) {
	id := c.Param("id")
	img, err := h.images.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	if img.VerificationReport == nil {
		c.JSON(http.StatusOK, nil) // renders "null"
		return
	}
	c.Data(http.StatusOK, "application/json", img.VerificationReport)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/handlers/ -run Expert -v`
Expected: all PASS.

- [ ] **Step 5: Build + vet + full short suite**

Run: `CGO_ENABLED=1 go build ./... && go vet ./... && go test -short ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/handlers/expert.go internal/httpapi/handlers/expert_test.go
git commit -m "feat(httpapi): add expert image handler (serve bytes + report)"
```

## Task 5: Server route wiring (Gap 3) + wire.go

**Files:**
- Edit: `internal/httpapi/server.go`
- Edit: `internal/app/wire.go`
- Create: `internal/httpapi/server_test.go`

- [ ] **Step 1: Add `questionRepo` to `NewServer`**

In `internal/httpapi/server.go`:

(a) Add a `questionRepo storage.QuestionRepo` field to the `Server` struct (after `imageRepo`).

(b) Add `questionRepo storage.QuestionRepo,` as the 4th parameter of `NewServer` (after `imageRepo`), and assign it in the struct literal (`questionRepo: questionRepo,`).

(c) Register the new routes inside `registerRoutes`, after the existing `sessions` block (still inside the `apiGroup.Use(AuthMiddleware(...))` scope):

```go
		// Questions — both roles; behavior splits inside the handler.
		// PATCH is expert-only via per-route RoleGuard (spec §4.4).
		questionHandler := handlers.NewQuestionHandler(s.questionRepo, s.sessionRepo)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.PATCH("/:id", RoleGuard("expert"), questionHandler.Update)
		}

		// Expert image access — expert only (spec §4.5).
		expertHandler := handlers.NewExpertHandler(s.imageRepo)
		expertImages := apiGroup.Group("/images", RoleGuard("expert"))
		{
			expertImages.GET("/:id", expertHandler.GetImage)
			expertImages.GET("/:id/verification-report", expertHandler.GetVerificationReport)
		}
```

> `RoleGuard` is already defined in `middleware.go` and returns 403 (`domain.ErrForbidden`) for the wrong role — exactly spec §4.7. `apiGroup` already carries `AuthMiddleware`, so all these routes are authenticated. There is no existing `/api/v1/images/...` route (image uploads live at `/sessions/:id/images`), so no Gin route conflict.

- [ ] **Step 2: Pass `questionRepo` from wire.go**

In `internal/app/wire.go`, update the `NewServer` call (currently `httpapi.NewServer(userRepo, sessionRepo, imageRepo, jobQueue, jwtMgr, pool, cfg.Upload)`) to insert `questionRepo` in the 4th position:

```go
	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)
```

- [ ] **Step 3: Add server-level RoleGuard 403 test (PATCH expert-only)**

The handler-package test cannot exercise `RoleGuard` (it lives in package `httpapi`), so the user-role 403 path is covered here at the server level. Create `internal/httpapi/server_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestQuestionPatch_RoleGuardRejectsUser verifies the PATCH route is gated by
// RoleGuard("expert"): a user-role caller gets 403 at the middleware layer
// before the handler runs. Mirrors the route wiring in registerRoutes (Step 1).
func TestQuestionPatch_RoleGuardRejectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Simulate AuthMiddleware having authenticated a "user"-role caller.
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("user_id", "u1"); c.Next() })
	r.PATCH("/api/v1/questions/:id", RoleGuard("expert"), func(c *gin.Context) {
		t.Error("handler must not run for user role")
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/questions/q1", strings.NewReader(`{"status":"verified"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("user patch: got %d want 403", w.Code)
	}
}
```

- [ ] **Step 4: Build + vet**

Run: `CGO_ENABLED=1 go build ./... && go vet ./...`
Expected: clean. `questionRepo` already exists in `Build` (it's constructed and passed to the pipeline).

- [ ] **Step 5: Run full short suite**

Run: `go test -short ./...`
Expected: green, including the new `TestQuestionPatch_RoleGuardRejectsUser`. No behavior change yet (new routes point at the new handlers).

- [ ] **Step 6: Smoke-check route registration (manual, optional)**

If a local Postgres + `COEUS_*` env is available, run `go run ./cmd/coeus` and (with a valid expert JWT):
- `GET /api/v1/questions` -> 200 (empty list or moderation queue)
- `PATCH /api/v1/questions/<bad-id>` with `{"status":"verified"}` -> 404
- `GET /api/v1/images/<bad-id>` -> 404

Skip if unavailable — the handler tests cover the logic.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/server_test.go internal/app/wire.go
git commit -m "feat(httpapi): wire questions + expert image routes"
```

---

## Task 6: Image-byte cleanup inside the PATCH transaction (Gap 5)

**Why a separate task:** The handler already calls `UpdateByExpert` (Task 3); this task makes that single call atomically null the image bytes when the last unresolved question for an image flips to `verified`. No interface change -> handler tests stay green.

**Files:**
- Edit: `internal/storage/postgres/question_repo.go` (extend `UpdateByExpert`)

- [ ] **Step 1: Extend `UpdateByExpert` with in-tx cleanup**

In `internal/storage/postgres/question_repo.go`, locate `UpdateByExpert`. It currently does: `Begin` -> `UPDATE ... SET status='verified'` -> `DELETE question_tags` -> relink tags -> `Commit`.

Insert the cleanup block **after the tag relink loop and before `tx.Commit`**. The final function should read:

```go
func (r *QuestionRepo) UpdateByExpert(ctx context.Context, id string, answers, choices []string, explanation string, confidence float64, tags []string, expertID string) error {
	choicesJSON, _ := json.Marshal(choices)
	answersJSON, _ := json.Marshal(answers)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update by expert: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE questions
		SET answers = $1, choices = $2, explanation = $3, confidence = $4,
		    status = 'verified', verified_at = now(), verified_by = $5, updated_at = now()
		WHERE id = $6
	`, answersJSON, choicesJSON, explanation, confidence, expertID, id)
	if err != nil {
		return fmt.Errorf("update by expert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update by expert: %w", domain.ErrNotFound)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, tagName := range tags {
		if err := r.linkTagTx(ctx, tx, id, tagName); err != nil {
			return fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}

	// --- Image-byte cleanup (spec §3.5), same transaction as the status flip ---
	if err := cleanupImageBytesTx(ctx, tx, id); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update by expert: %w", err)
	}
	return nil
}

// cleanupImageBytesTx nulls out original+enhanced bytes for every image linked
// to the patched question that no longer has any sibling question in
// 'moderation' or 'error'. Runs inside the caller's tx so it is atomic with
// the status flip. (CountUnresolvedForImage uses r.pool and can't be reused
// here; the count SQL is inlined tx-scoped.)
func cleanupImageBytesTx(ctx context.Context, tx pgx.Tx, questionID string) error {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT sq.image_id FROM session_questions sq WHERE sq.question_id = $1`,
		questionID)
	if err != nil {
		return fmt.Errorf("select linked images: %w", err)
	}
	var imageIDs []string
	for rows.Next() {
		var imgID string
		if err := rows.Scan(&imgID); err != nil {
			rows.Close()
			return fmt.Errorf("scan image id: %w", err)
		}
		imageIDs = append(imageIDs, imgID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate linked images: %w", err)
	}

	for _, imgID := range imageIDs {
		var unresolved int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM session_questions sq
			JOIN questions q ON q.id = sq.question_id
			WHERE sq.image_id = $1 AND q.status IN ('moderation', 'error')
		`, imgID).Scan(&unresolved); err != nil {
			return fmt.Errorf("count unresolved for image %s: %w", imgID, err)
		}
		if unresolved == 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE images SET original = NULL, enhanced = NULL WHERE id = $1`, imgID); err != nil {
				return fmt.Errorf("clean image bytes %s: %w", imgID, err)
			}
		}
	}
	return nil
}
```

> **Imports:** `pgx` (`github.com/jackc/pgx/v5`) and `context` are already imported in this file (it uses `pgx.ErrNoRows`, `context.Context`). Confirm `context` is in the import list; if not, add it. `pgx.Tx` is the tx handle type — already available via the pgx import.

- [ ] **Step 2: Build + vet**

Run: `CGO_ENABLED=1 go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 3: Run existing repo + handler tests (must stay green)**

Run: `go test -short ./...`
Expected: green. (`UpdateByExpert`'s signature is unchanged; handler fakes still satisfy the interface. The cleanup only adds in-tx SQL.)

- [ ] **Step 4: Commit**

```bash
git add internal/storage/postgres/question_repo.go
git commit -m "feat(storage): clean image bytes in UpdateByExpert transaction"
```

## Task 7: Integration test for cleanup + expert read projections (needs Docker)

**Why last:** Requires a real Postgres (Testcontainers `pgvector/pgvector:pg16`), per AGENTS.md. Covers the two things unit tests can't: the in-tx byte cleanup actually nulls `original`/`enhanced`, and the expert view's `image_id`/`has_verification_report` come back correctly.

**Files:**
- Create: `internal/storage/postgres/question_expert_test.go`

- [ ] **Step 1: Write the integration test**

Use `setupTestDB(t)` from `internal/storage/postgres/testhelpers_test.go` (per AGENTS.md). The tests self-skip under `-short`.

```go
package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestUpdateByExpert_CleansImageBytesWhenLastResolved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)

	// One session + one image.
	sess, err := sessions.Create(ctx, "user-1", 3600, 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	imgID, err := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}

	// Two questions linked to the same image, both moderation. Unique, non-empty
	// TextHash/TextNorm are required to satisfy the question_hash UNIQUE constraint.
	q1, err := questions.Create(ctx, &domain.Question{Text: "Q1", TextHash: "q1-hash", TextNorm: "q1", Status: domain.QuestionStatusModeration, Choices: []string{"a"}})
	if err != nil {
		t.Fatalf("create q1: %v", err)
	}
	q2, err := questions.Create(ctx, &domain.Question{Text: "Q2", TextHash: "q2-hash", TextNorm: "q2", Status: domain.QuestionStatusModeration, Choices: []string{"b"}})
	if err != nil {
		t.Fatalf("create q2: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, q1, 1, 0.9); err != nil {
		t.Fatalf("link q1: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, q2, 2, 0.9); err != nil {
		t.Fatalf("link q2: %v", err)
	}

	// Resolve q1 -> bytes MUST remain (q2 still moderation).
	if err := questions.UpdateByExpert(ctx, q1, []string{"a"}, []string{"a"}, "", 1.0, nil, "expert-1"); err != nil {
		t.Fatalf("update q1: %v", err)
	}
	if img, _ := imgs.FindByID(ctx, imgID); img.Original == nil {
		t.Fatal("bytes cleaned too early: q2 still moderation")
	}

	// Resolve q2 -> bytes MUST now be NULL (no unresolved siblings).
	if err := questions.UpdateByExpert(ctx, q2, []string{"b"}, []string{"b"}, "", 1.0, nil, "expert-1"); err != nil {
		t.Fatalf("update q2: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original != nil || img.Enhanced != nil {
		t.Fatalf("expected cleaned bytes, got original=%v enhanced=%v", img.Original, img.Enhanced)
	}
	// Metadata retained.
	if img.Mime != "image/png" {
		t.Errorf("mime should be retained, got %q", img.Mime)
	}
}

func TestUpdateByExpert_CleansImageBytesWhenErrorSiblingResolved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)

	sess, err := sessions.Create(ctx, "user-1", 3600, 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	imgID, err := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}

	// Two questions on the same image: one 'moderation', one 'error'. Both count
	// as "unresolved" for the cleanup decision (spec §3.5: status IN moderation|error).
	qMod, err := questions.Create(ctx, &domain.Question{Text: "M", TextHash: "mod-hash", TextNorm: "mod", Status: domain.QuestionStatusModeration, Choices: []string{"a"}})
	if err != nil {
		t.Fatalf("create moderation question: %v", err)
	}
	qErr, err := questions.Create(ctx, &domain.Question{Text: "E", TextHash: "err-hash", TextNorm: "err", Status: domain.QuestionStatusError, Choices: []string{"a"}})
	if err != nil {
		t.Fatalf("create error question: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, qMod, 1, 0.9); err != nil {
		t.Fatalf("link moderation question: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, qErr, 2, 0.9); err != nil {
		t.Fatalf("link error question: %v", err)
	}

	// Step 1: resolve the 'moderation' question -> bytes MUST remain
	//         (the 'error' sibling is still unresolved).
	if err := questions.UpdateByExpert(ctx, qMod, []string{"a"}, []string{"a"}, "", 1.0, nil, "expert-1"); err != nil {
		t.Fatalf("update moderation question: %v", err)
	}
	if img, _ := imgs.FindByID(ctx, imgID); img.Original == nil {
		t.Fatal("bytes cleaned too early: error sibling still unresolved")
	}

	// Step 2: resolve the 'error' question -> bytes MUST now be NULL.
	if err := questions.UpdateByExpert(ctx, qErr, []string{"a"}, []string{"a"}, "", 1.0, nil, "expert-1"); err != nil {
		t.Fatalf("update error question: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original != nil || img.Enhanced != nil {
		t.Fatalf("expected cleaned bytes, got original=%v enhanced=%v", img.Original, img.Enhanced)
	}
}

func TestFindExpertByID_ReturnsImageLinkAndReportFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)

	sess, _ := sessions.Create(ctx, "user-1", 3600, 0)
	imgID, _ := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	_ = imgs.UpdateVerificationReport(ctx, imgID, []byte(`{"flag":true}`))

	qID, _ := questions.Create(ctx, &domain.Question{Text: "Q", TextHash: "qe-hash", TextNorm: "qe", Status: domain.QuestionStatusModeration, Choices: []string{"a"}})
	_ = questions.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9)

	ev, err := questions.FindExpertByID(ctx, qID)
	if err != nil {
		t.Fatalf("find expert: %v", err)
	}
	if ev.ImageID != imgID {
		t.Errorf("image_id: got %q want %q", ev.ImageID, imgID)
	}
	if !ev.HasVerificationReport {
		t.Errorf("has_verification_report: want true")
	}
}
```

> **Adjust to real signatures:** `setupTestDB` returns a `*pgxpool.Pool` per AGENTS.md. Confirm `sessions.Create` / `imgs.Create` / `questions.Create` argument order matches the current signatures (they are: `Create(ctx, *domain.Question) (string,error)`, `Create(ctx, sessionID, original, mime, w, h) (string,error)`, `Create(ctx, userID, durationSec, bufferSec) (*domain.Session,error)`). `question_hash` is NOT NULL + UNIQUE and `Create` does NOT auto-generate `TextHash`/`TextNorm` (it inserts them verbatim from the struct), so every `domain.Question` in these tests must set distinct, non-empty `TextHash` and `TextNorm` (already done above) — otherwise the second `Create` violates the constraint. Set `Choices` non-empty so the row is valid; add any other NOT NULL fields the migrations require (check `0002`/`0003` migrations if the insert fails).

- [ ] **Step 2: Run the integration test (Docker must be running)**

Run: `go test ./internal/storage/postgres/ -run 'UpdateByExpert_Cleans|FindExpertByID' -v -timeout 180s`
Expected: both PASS. (First run pulls the pgvector image.)

- [ ] **Step 3: Run the full integration suite to ensure nothing regressed**

Run: `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s`
Expected: green.

- [ ] **Step 4: Commit**

```bash
git add internal/storage/postgres/question_expert_test.go
git commit -m "test(storage): integration test for in-tx image cleanup + expert view"
```

---

## Final verification (after all tasks)

Run the full gate that AGENTS.md implies:

```bash
CGO_ENABLED=1 go build ./... \
  && go vet ./... \
  && go test -short ./...
```

Expected: clean build, no vet warnings, all `-short` tests green.

If Docker is running, also run the integration gate:

```bash
go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s
```

### Spec coverage checklist (Gap 1–5)

| Scope item | Implemented in |
|---|---|
| Gap 1 — `GET /questions` user (session_id required, ownership, window -> 410) | Task 3 (`List` user branch) |
| Gap 1 — `GET /questions` expert (moderation queue, default+tag filter) | Task 3 (`List` expert branch) |
| Gap 1 — `GET /questions/:id` user (ownership -> 404) + expert (any) | Task 3 (`Get`) |
| Gap 1 — `PATCH /questions/:id` expert only (user -> 403) | Task 3 (`Update` handler body) + Task 5 (`RoleGuard` route + server-level 403 test) |
| Gap 2 — `GET /images/:id` (serve bytes, 404 if cleaned) | Task 4 (`GetImage`) |
| Gap 2 — `GET /images/:id/verification-report` (JSON / null / 404) | Task 4 (`GetVerificationReport`) |
| Gap 3 — route wiring + middleware chain | Task 5 |
| Gap 4 — user + expert response shapes, `answers[*].id` derivation (first-index-wins, missing value) | Task 1 |
| Gap 5 — image-byte cleanup inside PATCH tx | Task 6 (`cleanupImageBytesTx`) |
| Documented SessionWindow-on-query-param decision | Decision #1 + Task 3 inline check |

### Existing tests that must remain green

- `internal/httpapi/handlers/` auth/sessions/images tests (Tasks 3–5 add new tests, don't touch existing).
- `internal/storage/postgres/` existing repo tests (Task 2 + 6 are additive; `UpdateByExpert` signature unchanged).
- `internal/pipeline/` tests (unaffected — they use the four AI ports, not the HTTP/repos changed here).
- `internal/httpapi/dto/` (Task 1 only adds files).

---

## Notes for the executor

- **`context` import in `question_repo.go`:** Task 6's `cleanupImageBytesTx` takes a `context.Context`. The file already imports `context` (every method uses it), so no import change is expected — but `go build` will flag it if missing.
- **`scanQuestionWithSession`:** Task 2 reuses it for `FindForUserByID`. If it doesn't exist as a standalone helper, extract the existing `ListForUser` row-scan body into it (DRY). The column order in `FindForUserByID`'s SELECT intentionally matches `ListForUser`'s.
- **Expert role string:** `AuthMiddleware` sets `c.Set("role", claims.Role)`. The handler compares against the literal `"expert"` (local const). If a user somehow has neither role, the user branch runs (returns 400/404 as appropriate) — acceptable for v1.
- **No re-design:** every decision above is a direct consequence of the approved spec + existing code shapes. Do not introduce websockets, caching, role constants in domain, or new middleware abstractions.

