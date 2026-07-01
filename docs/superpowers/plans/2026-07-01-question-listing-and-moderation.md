# Question Listing & Moderation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the three coupled, approved changes to coeus's questions API: (3) derive `MultipleCorrect` from `len(answers) > 1` and drop the column; (1) unify listing behind a single `session_id`-driven scoping rule with an ownership-gap fix (404-vs-403 split); (2) rewrite moderation update from `PATCH` partial to a validating `PUT` full-replace.

**Architecture:** Each of the three sections lands as one commit because Go's compile-time interface satisfaction (`var _ storage.QuestionRepo = (*QuestionRepo)(nil)` at `internal/storage/postgres/question_repo.go:24`) and struct-field references keep the build red until every ripple of a given change is complete. Order is Section 3 → Section 1 → Section 2: Section 3 is the most mechanical and removes a field that Sections 1 & 2 would otherwise have to carry; Section 1 restructures the repo interface that Section 2 then further refines. The frontend JSON contract for `multiple_correct` is preserved — the wire field stays, now derived.

**Tech Stack:** Go 1.26, Gin, pgx/v5, pgvector, PostgreSQL. CGO + libvips (govips) required for any compile.

**Source of truth:** `docs/superpowers/specs/2026-07-01-question-listing-and-moderation-design.md`. Do not re-design; this plan only decomposes that spec into tasks.

---

## Prerequisites & Conventions

- **CGO + libvips are required to compile anything.** On this host confirm once: `pkg-config --exists vips && echo OK` (or `brew list vips`). If missing: `brew install vips pkg-config`. Without it, `go build ./...` and `go vet ./...` fail. The `Dockerfile` is the fallback.
- **No Makefile / no CI.** Use `go` directly.
- **`*.md` and `docs/` are gitignored.** This plan and the spec won't show in `git status`. Use `git add -f` only if you want to track them.
- **Verification tiers (per `AGENTS.md`):**
  - Unit, no external deps: `go test -short ./...`
  - Compile check: `go build ./...` and `go vet ./...`
  - Integration, **Docker must be running** (Testcontainers + `pgvector/pgvector:pg16`), self-skip under `-short`: `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s`
- **No build tags.** `-short` is the only gate. DB-backed tests use `setupTestDB(t)` in `internal/storage/postgres/testhelpers_test.go`.
- **Migrations are embedded** in `internal/storage/postgres/migrations/` and applied automatically on boot via `postgres.RunMigrations` (called from `internal/app/wire.go`). Adding a migration file is the only schema step; there is no separate `migrate` command.
- **TDD note:** For these coupled refactors the build is intentionally red between sub-steps within a task; verification (build+vet+test) runs once per task, then commit. Write/adjust tests as the first sub-step of each task where feasible, so the *test* defines the target behavior before the implementation edits.

---

## File Structure (what changes, by responsibility)

**Section 3 — derive `MultipleCorrect` (Task 1):**
- `internal/domain/question.go` — delete `MultipleCorrect bool` field; add `MultipleCorrect() bool` method.
- `internal/storage/postgres/migrations/0004_drop_multiple_correct.sql` — new file (DROP COLUMN).
- `internal/storage/postgres/question_repo.go` — remove column from INSERT, all SELECT lists, both `*SelectBase` constants, and all 4 `scan*` targets.
- `internal/pipeline/ports.go` — drop field from `ExtractedQuestion`.
- `internal/pipeline/pipeline.go` — drop field from `domain.Question` construction.
- `internal/ai/extractor/schema.go`, `internal/ai/verifier/schema.go` — drop field from `questionDTO`/`verifiedQuestionDTO` and the `toPipeline`/`fromPipeline` mappings.
- `internal/httpapi/dto/requests.go` — drop field from `CreateQuestionRequest`.
- `internal/httpapi/handlers/questions.go` — response builders switch field-read → method-call; `Create` stops setting it.
- Response DTOs `internal/httpapi/dto/question.go` **keep** the wire field (frontend contract unchanged).

**Section 1 — unified listing (Task 2):**
- `internal/storage/ports.go` — `QuestionRepo`: delete `ListForUser`, delete `ListForModeration`, add `ListForSession`; `ListForModerationExpert` status filter becomes conditional.
- `internal/storage/postgres/question_repo.go` — implement `ListForSession` (expand SELECT + `scanQuestionWithSession` to full question column set); delete `ListForUser` + `ListForModeration` impls; make `ListForModerationExpert` conditional; align `FindForUserByID` SELECT to the expanded scan.
- `internal/httpapi/handlers/questions.go` — rewrite `List`: shared `status` validation before role split; `session_id`-driven scoping; user ownership split (404 miss / 403 not-owner); add `toExpertResponseFromSession`.
- `internal/httpapi/handlers/questions_test.go`, `internal/pipeline/pipeline_test.go` — update both fakes to the new interface.

**Section 2 — moderation `PUT` (Task 3):**
- `internal/domain/question.go` — add `QuestionUpdate` value type.
- `internal/storage/ports.go` — `UpdateByExpert` signature changes to take `domain.QuestionUpdate`.
- `internal/storage/postgres/question_repo.go` — new signature; conditional `SET` (status/`verified_at`/`verified_by`); nil→`[]` normalization.
- `internal/httpapi/dto/requests.go` — add `UpdateQuestionRequest` DTO.
- `internal/httpapi/server.go` — route `PATCH` → `PUT`.
- `internal/httpapi/handlers/questions.go` — rewrite `Update`: binding + struct-level `answers ⊆ choices` (exact, case-sensitive) + `confidence` range + tags rules; build `domain.QuestionUpdate`.
- All 3 fakes + `internal/storage/postgres/question_expert_test.go` (4 call sites) — update to new signature.

**Integration coverage (Task 4, Docker):** `internal/storage/postgres/` additions for ownership IDOR, unified scoping, PUT full-replace, migration 0004 column-absent.

---

## Task 1: Section 3 — Derive `MultipleCorrect` (drop stored column)

**Files:**
- Modify: `internal/domain/question.go`
- Create: `internal/storage/postgres/migrations/0004_drop_multiple_correct.sql`
- Modify: `internal/storage/postgres/question_repo.go`
- Modify: `internal/pipeline/ports.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/ai/extractor/schema.go`
- Modify: `internal/ai/verifier/schema.go`
- Modify: `internal/httpapi/dto/requests.go`
- Modify: `internal/httpapi/handlers/questions.go`
- Test: `internal/domain/question_test.go` (add method test)

> The build stays red until **all** sub-steps are done. Verify once at the end (Step 1.9), then commit.

- [ ] **Step 1.1: Add the domain method test (failing first)**

Create `internal/domain/question_test.go`. If a `question_test.go` already exists in that package, append the test instead of recreating the file header.

```go
package domain

import "testing"

func TestQuestionMultipleCorrectDerived(t *testing.T) {
	if (Question{Answers: []string{"A"}}).MultipleCorrect() {
		t.Errorf("single answer: got true, want false")
	}
	if !(Question{Answers: []string{"A", "B"}}).MultipleCorrect() {
		t.Errorf("two answers: got false, want true")
	}
	if (Question{Answers: nil}).MultipleCorrect() {
		t.Errorf("nil answers: got true, want false")
	}
}
```

- [ ] **Step 1.2: Run the test to confirm it fails**

Run: `go test -run TestQuestionMultipleCorrectDerived ./internal/domain/`
Expected: compile error `q.MultipleCorrect undefined (type Question has no field or method MultipleCorrect)`.

- [ ] **Step 1.3: Domain — delete field, add method**

In `internal/domain/question.go`, delete the `MultipleCorrect bool` line from the `Question` struct (currently line 31):

```go
// REMOVE this line from the struct:
	MultipleCorrect bool
```

Then add the method immediately after the `Question` struct's closing brace (after the existing struct literal, before `InferChoiceLabeling`):

```go
// MultipleCorrect reports whether the question has more than one correct answer.
// Derived from len(Answers); there is no stored column (spec §3.3).
func (q Question) MultipleCorrect() bool { return len(q.Answers) > 1 }
```

> Note: value receiver `(q Question)` — `Question` is small-ish and already passed by value through DTO builders; matches the codebase's getter style (no `Get`/`Is` prefix). This satisfies the `golang-naming` convention for boolean getters.

- [ ] **Step 1.4: Run the domain test to confirm it passes**

Run: `go test -run TestQuestionMultipleCorrectDerived ./internal/domain/`
Expected: PASS. (Other packages still won't compile yet — that's expected; do not run the full build until Step 1.9.)

- [ ] **Step 1.5: Add migration 0004**

Create `internal/storage/postgres/migrations/0004_drop_multiple_correct.sql`:

```sql
-- 0004_drop_multiple_correct.sql
-- multiple_correct is fully derivable from len(answers) > 1.
-- Drop the redundant column; the value is recomputed in the domain layer (spec §3.3, §5).
ALTER TABLE questions DROP COLUMN IF EXISTS multiple_correct;
```

(The highest existing migration is `0003_manual_tag.sql`; `0004` is the next free number — confirmed via `ls internal/storage/postgres/migrations/`.)

- [ ] **Step 1.6: Storage repo — remove the column everywhere**

Edit `internal/storage/postgres/question_repo.go`. Make exactly these edits (line numbers are current, pre-edit anchors from `rg`):

1. **INSERT** (`Create`, ~lines 28–44): remove `multiple_correct` from the column list and value list, and remove the `q.MultipleCorrect,` argument. Before/after:

   Before:
   ```go
   		INSERT INTO questions (number, question, question_normalized, question_hash,
   		    multiple_correct, choices, answers, choice_labeling, confidence,
   		    explanation, embedding, status, verified_at, verified_by)
   		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
   		RETURNING id
   	`, q.Number, q.Text, q.TextNorm, q.TextHash,
   		q.MultipleCorrect, choicesJSON, answersJSON, q.ChoiceLabeling,
   ```
   After:
   ```go
   		INSERT INTO questions (number, question, question_normalized, question_hash,
   		    choices, answers, choice_labeling, confidence,
   		    explanation, embedding, status, verified_at, verified_by)
   		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
   		RETURNING id
   	`, q.Number, q.Text, q.TextNorm, q.TextHash,
   		choicesJSON, answersJSON, q.ChoiceLabeling,
   ```

2. **`questionSelectBase` constant** (~line 445): remove `q.multiple_correct,`:
   ```go
   const questionSelectBase = `
   	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
  	       q.choices, q.answers, q.choice_labeling,
  	       q.confidence, q.explanation,
  	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
  	       q.verified_by::text,
  	       q.status
   	FROM questions q`
   ```

3. **`questionExpertSelectBase` constant** (~line 458): remove `q.multiple_correct,` from its column list (same single-line removal; keep the two correlated subqueries and `FROM questions q` intact).

4. **Inline SELECT in `ListForUser`** (~line 123): remove `q.multiple_correct,` from the column list. (This method is replaced wholesale in Task 2, but it must compile now.)

5. **Inline SELECT in `ListForModeration`** (~line 166 and its `DISTINCT` branch): remove `q.multiple_correct,` wherever it appears. (Dead method, deleted in Task 2; must compile now.)

6. **Inline SELECT in `FindForUserByID`** (~line 261): remove `q.multiple_correct,`.

7. **All `scan*` targets** — remove the `&q.MultipleCorrect,` (or `&qws.MultipleCorrect,`) argument at each of these four scan sites (function names and line numbers verified against `internal/storage/postgres/question_repo.go`): `scanQuestion` (~470), `scanQuestionRow` (~489), `scanQuestionExpert` (~510), and `scanQuestionWithSession` (~541). Each is a single argument deletion; do **not** change the surrounding scan order. Example for `scanQuestion`:
   ```go
   err := row.Scan(
   		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
   		&choices, &answers, &q.ChoiceLabeling,
   		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
   )
   ```

> Column/arity rule: after every edit, the number of `SELECT` columns for a given query must equal the number of `Scan(...)` destinations for the scan function that reads it. The shared `questionSelectBase` (now 13 cols) feeds `scanQuestion`; `questionExpertSelectBase` (now 13 + 2 subquery cols) feeds `scanQuestionExpert`; the session inline SELECTs feed `scanQuestionWithSession`. Keep them aligned.

- [ ] **Step 1.7: Pipeline + AI schemas + HTTP — drop the field**

1. `internal/pipeline/ports.go` (~line 45): delete `MultipleCorrect bool` from `ExtractedQuestion`.
2. `internal/pipeline/pipeline.go` (~line 180): delete the `MultipleCorrect: eq.MultipleCorrect,` line from the `domain.Question{...}` construction.
3. `internal/ai/extractor/schema.go`: delete `MultipleCorrect bool` from `questionDTO` (~line 22) and delete `MultipleCorrect: q.MultipleCorrect,` from `toPipeline` (~line 82).
4. `internal/ai/verifier/schema.go`: delete `MultipleCorrect bool` from `questionDTO` (~line 19) and from `verifiedQuestionDTO` (~line 36), and delete `MultipleCorrect: q.MultipleCorrect,` from `fromPipeline` (~line 67).
5. `internal/httpapi/dto/requests.go` (~line 16): delete `MultipleCorrect bool` from `CreateQuestionRequest`.
6. `internal/httpapi/handlers/questions.go`:
   - `Create` (~line 264): delete `MultipleCorrect: req.MultipleCorrect,`.
   - `toUserResponse` (~line 299): change `MultipleCorrect: qq.MultipleCorrect,` → `MultipleCorrect: qq.MultipleCorrect(),`.
   - `toExpertResponse` (~line 313): change `MultipleCorrect: q.MultipleCorrect,` → `MultipleCorrect: q.MultipleCorrect(),`.

> Response DTOs in `internal/httpapi/dto/question.go` (`UserQuestionResponse`, `ExpertQuestionResponse`) are **unchanged** — the wire field stays, now populated by the method.

- [ ] **Step 1.8: Drop `multiple_correct` from the AI skill prompts (coherence, optional for build)**

Spec §3.3 calls for the model to stop being told to emit `multiple_correct`. **This step does not affect runtime:** Go's `json.Unmarshal` silently ignores unknown keys, so a stale model that still emits `multiple_correct` is harmless — the field is simply not read anywhere. This is prompt coherence/cleanup only; the build and tests do **not** depend on it. It may be done in the same commit as the rest of Task 1 or deferred without consequence.

Confirm the two skill directories first (the `skills/` tree is the source of truth for prompt content):
```bash
ls skills/
```
Expected: `extract-questions-from-image/`, `verify-extracted-questions/`, `skills.go`. (`skills/skills.go` does not reference the field — verified — and needs no edit.)

Edit **`skills/extract-questions-from-image/SKILL.md`**:
- Remove the `"multiple_correct": …` key from every JSON example (currently ~lines 35 and 149).
- Remove `multiple_correct` from the "Required keys for every question object" sentence (~line 51).
- Remove the `multiple_correct` field-description bullet (~line 57).
- Rewrite the "Multiple correct answers" instruction (~line 71): keep the underlying guidance (when the question text / UI allows multiple answers, select every correct choice) but stop referencing the field — the extractor now simply lists all correct choices, and `MultipleCorrect` is derived from `len(answers) > 1`.
- Remove the "Forgetting to set `multiple_correct: true`" pitfall bullet (~line 170).

Edit **`skills/verify-extracted-questions/SKILL.md`**:
- Remove the `"multiple_correct": …` key from every JSON example (currently ~lines 37, 197, 234).
- Remove the two table rows that reference `multiple_correct` (the "required keys present" row ~line 61 and the "is boolean" coercion row ~line 65).
- Rephrase the `multiple_correct`-flag mentions in the consistency/answer-check notes (~lines 114, 211, 265) to reference answer-count vs. explanation directly (e.g. "the explanation describes two answers but only one is recorded"), without naming the flag.

> These files are gitignored markdown (`*.md` is gitignored repo-wide; see Prerequisites). Use `git add -f skills/extract-questions-from-image/SKILL.md skills/verify-extracted-questions/SKILL.md` only if you want them tracked.

- [ ] **Step 1.9: Verify build + vet + unit tests**

Run:
```bash
go build ./...
go vet ./...
go test -short ./...
```
Expected: build OK, vet clean, all unit tests PASS (including the new `TestQuestionMultipleCorrectDerived`). Existing tests that constructed `ExtractedQuestion`/`domain.Question` with the field will have been fixed by removing the field literal — `go build` will flag any missed site; fix it (every remaining reference is in the list above).

- [ ] **Step 1.10: Commit**

> If you completed the optional Step 1.8 skill edits and want them tracked, also run `git add -f skills/extract-questions-from-image/SKILL.md skills/verify-extracted-questions/SKILL.md` (these are gitignored markdown). Otherwise omit them.

```bash
git add internal/domain/question.go internal/domain/question_test.go \
        internal/storage/postgres/migrations/0004_drop_multiple_correct.sql \
        internal/storage/postgres/question_repo.go \
        internal/pipeline/ports.go internal/pipeline/pipeline.go \
        internal/ai/extractor/schema.go internal/ai/verifier/schema.go \
        internal/httpapi/dto/requests.go internal/httpapi/handlers/questions.go
git commit -m "refactor(questions): derive MultipleCorrect from len(answers), drop column

Removes the redundant stored multiple_correct column (migration 0004) and the
domain field; adds Question.MultipleCorrect() derived from len(Answers) > 1.
The response DTO keeps the wire field, now populated via the method. Drops the
field from the pipeline ExtractedQuestion, AI extractor/verifier schemas, and
the manual-create request DTO."
```

---

## Task 2: Section 1 — Unified Listing & Filtering (ownership fix + 404/403 split)

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/question_repo.go`
- Modify: `internal/httpapi/handlers/questions.go`
- Modify: `internal/httpapi/handlers/questions_test.go` (fake #1)
- Modify: `internal/pipeline/pipeline_test.go` (fake #2)

> Again the build is red between sub-steps; verify once at Step 2.7.

- [ ] **Step 2.1: Write the new handler unit tests first (define target behavior)**

In `internal/httpapi/handlers/questions_test.go`, make these test changes. (These encode spec §7.2.)

**(a) Update `fakeQuestionRepo`** — replace the `listForUser` field and its method with a `listForSession` field/method, and delete the `ListForModeration` method:

Replace:
```go
	listForUser    func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
```
with:
```go
	listForSession func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
```

Replace the `ListForUser` method:
```go
func (f *fakeQuestionRepo) ListForUser(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
	return f.listForUser(sid, st, l, o)
}
func (f *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
```
with:
```go
func (f *fakeQuestionRepo) ListForSession(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
	if f.listForSession != nil {
		return f.listForSession(sid, st, l, o)
	}
	return nil, nil
}
```

**(b) Flip the user-without-session expectation from 400 → 403.** In `TestList_UserRoleRequiresSessionID` (and rename it `TestList_UserRoleWithoutSessionForbidden403`):
```go
func TestList_UserRoleWithoutSessionForbidden403(t *testing.T) {
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("user without session_id: got %d want 403", w.Code)
	}
}
```

**(c) Split the not-owner test into 403, and add a session-missing 404.** Replace `TestList_UserRoleNotOwner404` with:
```go
func TestList_UserRoleNotOwnerForbidden403(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "other", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("not owner: got %d want 403", w.Code)
	}
}

func TestList_UserRoleSessionMissing404(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return nil, domain.ErrNotFound
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("session missing: got %d want 404", w.Code)
	}
}
```

**(d) Replace the expert default-status test** — there is no longer a forced default. Rename `TestList_ExpertModerationDefaultAndFilter` to `TestList_ExpertGlobalQueueAllStatusesAndFilter` and assert the forwarded status is empty by default:
```go
func TestList_ExpertGlobalQueueAllStatusesAndFilter(t *testing.T) {
	var gotStatus, gotTag string
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			gotStatus, gotTag = status, tag
			return []*storage.QuestionExpertView{{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})

	// No status param => no filter (empty status forwarded), all statuses.
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotStatus != "" {
		t.Errorf("default status: got %q want empty (all statuses)", gotStatus)
	}

	// Explicit status filter is forwarded.
	_ = doReq(t, r, "GET", "/api/v1/questions?status=moderation", "")
	if gotStatus != "moderation" {
		t.Errorf("status filter: got %q want moderation", gotStatus)
	}

	// Tag filter is forwarded.
	_ = doReq(t, r, "GET", "/api/v1/questions?tag=chemistry", "")
	if gotTag != "chemistry" {
		t.Errorf("tag filter: got %q want chemistry", gotTag)
	}
}
```

**(e) Add shared status-validation and expert-session-scoped tests:**
```go
func TestList_InvalidStatus400BothRoles(t *testing.T) {
	// The user role resolves the session (h.sessions.FindByID) before building
	// a response, so the fake MUST supply a valid OWNED session — UserID equal
	// to the authenticated "x", status open, far-future expiry. Otherwise byID
	// is nil and the fake dereferences a nil func, panicking before the request
	// can reach the shared status validation. The expert role is exempt from the
	// ownership/expiry gates but uses the same session repo, so this session is
	// harmless for it.
	owned := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{
			UserID:    "x", // == authenticated user_id passed to newQuestionRouter
			Status:    domain.SessionStatusOpen,
			ExpiresAt: "2999-01-01T00:00:00Z",
		}, nil
	}}

	// Invalid status must be 400 for BOTH roles on the session-scoped path.
	for _, role := range []string{"user", "expert"} {
		r := newQuestionRouter(role, "x", &fakeQuestionRepo{}, owned)
		w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1&status=bogus", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: invalid status (session-scoped) got %d, want 400", role, w.Code)
		}
	}

	// Expert on the global-queue path (no session_id) hits the shared status
	// validation directly; no session repo is consulted.
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions?status=bogus", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expert invalid status (global queue): got %d want 400", w.Code)
	}
}

func TestList_ExpertSessionScopedUsesListForSession(t *testing.T) {
	called := false
	q := &fakeQuestionRepo{
		listForSession: func(sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
			called = true
			if sid != "s1" {
				t.Errorf("session id: got %q want s1", sid)
			}
			return []*storage.QuestionWithSession{{Question: &domain.Question{ID: "q1", Status: "verified"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("expert session-scoped request did not call ListForSession")
	}
}
```

- [ ] **Step 2.2: Run the new tests to confirm they fail**

Run: `go test -run 'TestList_' ./internal/httpapi/handlers/`
Expected: compile failures (interface still has `ListForUser`, no `ListForSession`; handler not rewritten). This is expected — proceed.

- [ ] **Step 2.3: Update the `QuestionRepo` interface**

In `internal/storage/ports.go`, replace the `ListForUser` + `ListForModeration` lines:

Before:
```go
	ListForUser(ctx context.Context, sessionID string, statusFilter string, limit, offset int) ([]*QuestionWithSession, error)
	ListForModeration(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*domain.Question, error)
```
After:
```go
	ListForSession(ctx context.Context, sessionID, statusFilter string, limit, offset int) ([]*QuestionWithSession, error)
```

(Leave `ListForModerationExpert` in the interface — its **signature is unchanged**; only its implementation's filter behavior changes. The spec's `[]Question` return in §3.1.5 is a typo; it stays `[]*QuestionExpertView` because the expert DTO needs `ImageID`/`HasVerificationReport`.)

- [ ] **Step 2.4: Implement the postgres repo changes**

In `internal/storage/postgres/question_repo.go`:

**(a) Add a shared SELECT base for the session-scoped read** (full question columns + 4 link fields), next to `questionSelectBase`:

```go
// questionWithSessionSelectBase is the full question column set (matches
// questionSelectBase, post-multiple_correct-removal) plus the session_questions
// link fields. Used by ListForSession and FindForUserByID via scanQuestionWithSession.
const questionWithSessionSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status,
	       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
	FROM session_questions sq
	JOIN questions q ON q.id = sq.question_id`
```

**(b) Replace `ListForUser` with `ListForSession`** (delete the old `ListForUser` impl, ~lines 121–157). New impl:

```go
// ListForSession returns the full question representation plus session link
// fields for every question linked to sessionID. statusFilter is applied only
// when non-empty (absent => all statuses). Used by both roles when a session is
// in scope (spec §3.1.3, §3.1.5).
func (r *QuestionRepo) ListForSession(ctx context.Context, sessionID, statusFilter string, limit, offset int) ([]*storage.QuestionWithSession, error) {
	query := questionWithSessionSelectBase + " WHERE sq.session_id = $1"
	args := []interface{}{sessionID}
	idx := 2
	if statusFilter != "" {
		query += fmt.Sprintf(" AND q.status = $%d", idx)
		args = append(args, statusFilter)
		idx++
	}
	query += fmt.Sprintf(" ORDER BY sq.extracted_number LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list questions for session: %w", err)
	}
	defer rows.Close()

	var results []*storage.QuestionWithSession
	for rows.Next() {
		qws, err := scanQuestionWithSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		results = append(results, qws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list questions for session: %w", err)
	}
	return results, nil
}
```

**(c) Delete `ListForModeration`** entirely (~lines 159–203). It is dead code (zero production callers; confirmed via `rg '\.ListForModeration\('`).

**(d) Make `ListForModerationExpert`'s status filter conditional** (~lines 218–257). Replace the unconditional `WHERE q.status = $1` start:

Before:
```go
	query := questionExpertSelectBase
	args := []interface{}{}
	idx := 1
	query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
	args = append(args, statusFilter)
	idx++
	if tagFilter != "" {
```
After:
```go
	query := questionExpertSelectBase
	args := []interface{}{}
	idx := 1
	if statusFilter != "" {
		query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
		args = append(args, statusFilter)
		idx++
	}
	if tagFilter != "" {
		where := " WHERE"
		if statusFilter != "" {
			where = " AND"
		}
		query += fmt.Sprintf(`%s EXISTS (SELECT 1 FROM question_tags qt
			JOIN tags t ON t.id = qt.tag_id
			WHERE qt.question_id = q.id AND t.name = $%d)`, where, idx)
		args = append(args, tagFilter)
		idx++
	}
```
(Keep the existing `ORDER BY q.created_at LIMIT … OFFSET …` tail and the `scanQuestionExpert` + `getTags` per-row loop unchanged.)

**(e) Align `FindForUserByID` to the expanded scan** (~lines 259–279). Replace its inline 13-col SELECT with the shared base:

Before:
```go
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
```
After:
```go
	query := questionWithSessionSelectBase + `
		JOIN sessions s ON s.id = sq.session_id
		WHERE sq.question_id = $1 AND s.user_id = $2
		ORDER BY sq.id
		LIMIT 1`
```

**(f) Expand `scanQuestionWithSession`** (~lines 539–556) to read all 17 columns:

Before:
```go
func scanQuestionWithSession(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionWithSession, error) {
	qws := &storage.QuestionWithSession{Question: &domain.Question{}}
	var choices, answers []byte
	if err := row.Scan(
		&qws.ID, &qws.Number, &qws.Text, &qws.MultipleCorrect,
		&choices, &answers, ...
```
After (full body):
```go
// scanQuestionWithSession scans the 13 question columns + 4 link fields used by
// ListForSession and FindForUserByID. Accepts both pgx.Row and pgx.Rows.
func scanQuestionWithSession(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionWithSession, error) {
	qws := &storage.QuestionWithSession{Question: &domain.Question{}}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	if err := row.Scan(
		&qws.ID, &qws.Number, &qws.Text, &qws.TextNorm, &qws.TextHash,
		&choices, &answers, &qws.ChoiceLabeling,
		&qws.Confidence, &qws.Explanation, &verifiedAt, &verifiedBy, &qws.Status,
		&qws.SessionID, &qws.ImageID, &qws.ExtractedNumber, &qws.ExtractedConfidence,
	); err != nil {
		return nil, fmt.Errorf("scan question with session: %w", err)
	}
	_ = json.Unmarshal(choices, &qws.Choices)
	_ = json.Unmarshal(answers, &qws.Answers)
	qws.VerifiedAt = verifiedAt
	qws.VerifiedBy = verifiedBy
	return qws, nil
}
```

- [ ] **Step 2.5: Update the second fake (`internal/pipeline/pipeline_test.go`)**

Replace these three methods (~lines 192–205):

Before:
```go
func (r *fakeQuestionRepo) ListForUser(context.Context, string, string, int, int) ([]*storage.QuestionWithSession, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
```
After:
```go
func (r *fakeQuestionRepo) ListForSession(context.Context, string, string, int, int) ([]*storage.QuestionWithSession, error) {
	return nil, nil
}
```
(Delete the `ListForModeration` stub entirely. `UpdateByExpert` is changed in Task 3 — leave its stub as-is for now; it still matches the current interface.)

- [ ] **Step 2.6: Rewrite the handler `List` method + add the expert-session mapper**

In `internal/httpapi/handlers/questions.go`, replace the entire `List` method (lines 46–106) with:

```go
// List — GET /api/v1/questions. session_id drives scoping; role drives only
// authorization and response shape (spec §3.1).
func (h *QuestionHandler) List(c *gin.Context) {
	role := c.GetString("role")
	page, perPage, offset := parsePaging(c)

	// Shared status validation runs BEFORE the role split (spec §3.1.2).
	status := c.Query("status")
	if status != "" &&
		status != domain.QuestionStatusModeration &&
		status != domain.QuestionStatusVerified &&
		status != domain.QuestionStatusError {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	sessionID := c.Query("session_id")
	userID := c.GetString("user_id")

	if sessionID != "" {
		// Session-scoped read path, shared by both roles.
		sess, err := h.sessions.FindByID(c.Request.Context(), sessionID)
		if err != nil {
			// Session genuinely missing => 404 (spec §3.1.4).
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		// Ownership: user must own the session (403 on mismatch); expert exempt.
		if role != roleExpert && sess.UserID != userID {
			c.JSON(http.StatusForbidden, errorResponse(domain.ErrForbidden))
			return
		}
		// Expiry gate applies to the user role only (experts may inspect any session).
		if role != roleExpert {
			if sess.Status != domain.SessionStatusOpen {
				c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
				return
			}
			expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
			if err != nil || time.Now().After(expiresAt) {
				c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
				return
			}
		}

		items, err := h.questions.ListForSession(c.Request.Context(), sessionID, status, perPage, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(err))
			return
		}
		data := make([]any, 0, len(items))
		for _, q := range items {
			if role == roleExpert {
				data = append(data, toExpertResponseFromSession(q))
			} else {
				data = append(data, toUserResponse(q))
			}
		}
		c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
		return
	}

	// No session_id: experts get the global queue; users are forbidden (403).
	if role != roleExpert {
		c.JSON(http.StatusForbidden, errorResponse(domain.ErrForbidden))
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
}
```

Add the new helper near `toExpertResponse` (after it):

```go
// toExpertResponseFromSession builds the expert DTO from the session-scoped read
// (QuestionWithSession). HasVerificationReport is not available on this path
// (it is a moderation-queue convenience) and defaults to false.
func toExpertResponseFromSession(qws *storage.QuestionWithSession) dto.ExpertQuestionResponse {
	q := qws.Question
	resp := dto.ExpertQuestionResponse{
		ID:              q.ID,
		Number:          qws.ExtractedNumber,
		Question:        q.Text,
		MultipleCorrect: q.MultipleCorrect(),
		Choices:         q.Choices,
		Answers:         q.Answers,
		ChoiceLabeling:  q.ChoiceLabeling,
		Confidence:      q.Confidence,
		Explanation:     q.Explanation,
		Tags:            q.Tags,
		Status:          q.Status,
		ImageID:         qws.ImageID,
		VerifiedAt:      q.VerifiedAt,
		VerifiedBy:      q.VerifiedBy,
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}
```

> **`domain.ErrForbidden`:** if this sentinel does not already exist in `internal/domain`, add it alongside `ErrNotFound`/`ErrValidation`/`ErrSessionExpired` (check `internal/domain/errors.go`). The existing `errorResponse(...)` helper is reused, so no new HTTP mapping is needed.

- [ ] **Step 2.7: Verify build + vet + unit tests**

Run:
```bash
go build ./...
go vet ./...
go test -short ./...
```
Expected: build OK; vet clean; the `TestList_*` handler tests PASS (including the flipped 403, the new 404, the empty-default-status expert queue, and the expert-session-scoped path). If `domain.ErrForbidden` was missing and you added it, ensure no other package breaks.

- [ ] **Step 2.8: Commit**

```bash
git add internal/storage/ports.go internal/storage/postgres/question_repo.go \
        internal/httpapi/handlers/questions.go \
        internal/httpapi/handlers/questions_test.go internal/pipeline/pipeline_test.go
# also: git add -f internal/domain/errors.go (only if you added ErrForbidden there)
git commit -m "feat(questions): unify listing behind session_id scoping, fix ownership gap

Listing is now driven by the session_id query param; role governs only
authorization and response shape. Both roles share ListForSession when a session
is in scope; the global moderation queue (ListForModerationExpert) is expert-only
with a now-conditional status filter (absent => all statuses). Deletes the dead
ListForModeration and the superseded ListForUser. Splits the user list path:
session-missing => 404, not-owner => 403 (was a conflated 404), and user without
session_id is now 403 (was 400). Status validation is shared across both roles."
```

---

## Task 3: Section 2 — Moderation `PATCH` → `PUT` (full-replace + validation)

**Files:**
- Modify: `internal/domain/question.go`
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/question_repo.go`
- Modify: `internal/storage/postgres/question_expert_test.go` (4 call sites)
- Modify: `internal/httpapi/dto/requests.go`
- Modify: `internal/httpapi/server.go`
- Modify: `internal/httpapi/handlers/questions.go`
- Modify: `internal/httpapi/handlers/questions_test.go` (fake #1)
- Modify: `internal/pipeline/pipeline_test.go` (fake #2)

- [ ] **Step 3.1: Write the validation unit tests first**

In `internal/httpapi/handlers/questions_test.go`, add tests that encode spec §7.1. The fake's `UpdateByExpert` signature changes in Step 3.4, so write these against the new shape. Update the `fakeQuestionRepo.updateByExpert` field/method to the new signature (see Step 3.4 for the exact fake edit) and add a helper to build a valid full payload.

> **Import (compile-blocker):** `TestUpdate_TooManyTags400` below uses `fmt.Sprintf`, which this test file does not currently import. Add `"fmt"` to the import block at the top of `internal/httpapi/handlers/questions_test.go` (alphabetically, between `"errors"` and `"net/http"`). The package will not compile without it.

```go
func validUpdateBody() string {
	return `{"status":"verified","choices":["A","B"],"answers":["A"],"explanation":"e","tags":["t"],"confidence":0.9}`
}

func TestUpdate_RejectsIncompletePayload400(t *testing.T) {
	q := &fakeQuestionRepo{}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	// {"status":"verified"} alone must NOT null out choices/answers.
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("incomplete payload: got %d want 400", w.Code)
	}
}

func TestUpdate_AnswersNotSubsetOfChoices400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","choices":["A","B"],"answers":["C"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answers not in choices: got %d want 400", w.Code)
	}
	// Case-sensitive: "a" must not match choice "A".
	w = doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","choices":["A"],"answers":["a"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("case-sensitive mismatch: got %d want 400", w.Code)
	}
}

func TestUpdate_ConfidenceOutOfRange400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","choices":["A"],"answers":["A"],"confidence":1.5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confidence > 1: got %d want 400", w.Code)
	}
}

func TestUpdate_TooManyTags400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	// 21 tags => over the 20 limit.
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = "t"
	}
	body := fmt.Sprintf(`{"status":"moderation","choices":["A"],"answers":["A"],"tags":%s}`, asJSON(tags))
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too many tags: got %d want 400", w.Code)
	}
}

func TestUpdate_ConfidenceAbsentDefaultsToOne(t *testing.T) {
	var gotConf float64
	q := &fakeQuestionRepo{
		updateByExpert: func(id string, upd domain.QuestionUpdate, expertID string) error {
			gotConf = upd.Confidence
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","choices":["A"],"answers":["A"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotConf != 1.0 {
		t.Errorf("default confidence: got %v want 1.0", gotConf)
	}
}

func TestUpdate_ModerationStatusClearsVerification(t *testing.T) {
	var got domain.QuestionUpdate
	q := &fakeQuestionRepo{
		updateByExpert: func(id string, upd domain.QuestionUpdate, expertID string) error {
			got = upd
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusModeration}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"moderation","choices":["A"],"answers":["A"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if got.Status != domain.QuestionStatusModeration {
		t.Errorf("status forwarded: got %q want moderation", got.Status)
	}
}
```

Add the tiny helpers used above (top of the test file, near `doReq`):
```go
func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
```

Also **update existing `TestUpdate_*` tests** to use `PUT` and full payloads:
- `TestUpdate_ExpertRepoError500`: change method `"PATCH"` → `"PUT"`, and update the `updateByExpert` fake field type to the new signature (Step 3.4).
- `TestUpdate_ReFetchFallbackReturnsPartialBody`: replace the whole test with the version below — method `PATCH` → `PUT`, the `updateByExpert` fake switched to the new `domain.QuestionUpdate` signature, and the body sent via `validUpdateBody()`:

```go
func TestUpdate_ReFetchFallbackReturnsPartialBody(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, domain.QuestionUpdate, string) error {
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return nil, errors.New("refetch failed")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", validUpdateBody())
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["id"] != "q1" || body["status"] != "verified" {
		t.Fatalf("unexpected partial body: %s", w.Body.String())
	}
}
```

(`validUpdateBody()` sets `"status":"verified"`, so the `body["status"] != "verified"` assertion still holds.)

And update the **test router** registration in `newQuestionRouterWithEmbedder`:
```go
	r.PATCH("/api/v1/questions/:id", h.Update)   // BEFORE
	r.PUT("/api/v1/questions/:id", h.Update)     // AFTER
```

- [ ] **Step 3.2: Run the new tests to confirm they fail**

Run: `go test -run 'TestUpdate_' ./internal/httpapi/handlers/`
Expected: compile failures (`UpdateQuestionRequest` undefined, `domain.QuestionUpdate` undefined, fake signature mismatch, route still PATCH). Expected — proceed.

- [ ] **Step 3.3: Add `domain.QuestionUpdate`**

In `internal/domain/question.go`, add the value type (near the `Question` struct):

```go
// QuestionUpdate is the expert-authored full replacement of a question's
// editable fields (spec §3.2.5). Server-managed fields (id, number, text*,
// embedding, verified_at, verified_by) are NOT carried here. Status drives the
// verified_at/verified_by invariant enforced in the repo.
type QuestionUpdate struct {
	Status      string
	Choices     []string
	Answers     []string
	Explanation string
	Tags        []string
	Confidence  float64
}
```

- [ ] **Step 3.4: Change the interface + both fakes**

In `internal/storage/ports.go`, change the `UpdateByExpert` signature:

Before:
```go
	UpdateByExpert(ctx context.Context, id string, answers []string, choices []string, explanation string, confidence float64, tags []string, expertID string) error
```
After:
```go
	UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error
```

Fake #1 (`internal/httpapi/handlers/questions_test.go`) — change the field and method:

Before:
```go
	updateByExpert func(id string, answers, choices []string, explanation string, conf float64, tags []string, expertID string) error
```
After:
```go
	updateByExpert func(id string, upd domain.QuestionUpdate, expertID string) error
```
And the method:
```go
func (f *fakeQuestionRepo) UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error {
	f.updateCalled = true
	f.updateArgs.id, f.updateArgs.expertID = id, expertID
	f.updateArgs.answers, f.updateArgs.choices = upd.Answers, upd.Choices
	f.updateArgs.explanation, f.updateArgs.conf, f.updateArgs.tags = upd.Explanation, upd.Confidence, upd.Tags
	if f.updateByExpert != nil {
		return f.updateByExpert(id, upd, expertID)
	}
	return nil
}
```
(`updateArgs` is the existing capture struct — keep its field names; only the *source* of the values changes to `upd.*`.)

Fake #2 (`internal/pipeline/pipeline_test.go`) — change the stub signature:
```go
func (r *fakeQuestionRepo) UpdateByExpert(context.Context, string, domain.QuestionUpdate, string) error {
	return nil
}
```

- [ ] **Step 3.5: Update the integration-test call sites**

In `internal/storage/postgres/question_expert_test.go`, the 4 call sites (lines 51, 63, 120, 132) use the old loose signature. Convert each to build a `domain.QuestionUpdate`. For example, line 51:

Before:
```go
	if err := questions.UpdateByExpert(ctx, q1, []string{"a"}, []string{"a"}, "", 1.0, nil, user.ID); err != nil {
```
After:
```go
	if err := questions.UpdateByExpert(ctx, q1, domain.QuestionUpdate{
		Status: domain.QuestionStatusVerified, Answers: []string{"a"}, Choices: []string{"a"}, Confidence: 1.0,
	}, user.ID); err != nil {
```
Apply the same conversion to lines 63, 120, 132 (all use `[]string{"a"}`/`[]string{"b"}`, `""` explanation, `1.0`, `nil` tags). These are integration tests (Docker-gated); they will be exercised in Task 4, but they must **compile** now.

- [ ] **Step 3.6: Rewrite the repo `UpdateByExpert`**

In `internal/storage/postgres/question_repo.go`, replace the method header and body (~lines 281–322):

```go
func (r *QuestionRepo) UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error {
	// Defense-in-depth: normalize nil slices to empty so json.Marshal yields a
	// real array, never null. Validation (handler) is the primary guard that
	// guarantees non-nil at the call site (spec §3.2.5).
	choices := upd.Choices
	if choices == nil {
		choices = []string{}
	}
	answers := upd.Answers
	if answers == nil {
		answers = []string{}
	}
	choicesJSON, _ := json.Marshal(choices)
	answersJSON, _ := json.Marshal(answers)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update by expert: %w", err)
	}
	defer tx.Rollback(ctx)

	// status is no longer hard-coded; the verified_at/verified_by invariant
	// (NOT NULL <=> status='verified') is enforced via CASE (spec §3.2.4).
	tag, err := tx.Exec(ctx, `
		UPDATE questions
		SET answers = $1, choices = $2, explanation = $3, confidence = $4,
		    status = $5,
		    verified_at = CASE WHEN $5 = 'verified' THEN now() ELSE NULL END,
		    verified_by = CASE WHEN $5 = 'verified' THEN $6 ELSE NULL END,
		    updated_at = now()
		WHERE id = $7
	`, answersJSON, choicesJSON, upd.Explanation, upd.Confidence, upd.Status, expertID, id)
	if err != nil {
		return fmt.Errorf("update by expert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update by expert: %w", domain.ErrNotFound)
	}

	// Tag delete-and-reinsert, same transaction.
	if _, err := tx.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, tagName := range upd.Tags {
		if err := r.linkTagTx(ctx, tx, id, tagName); err != nil {
			return fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}

	// Image-byte cleanup (spec §3.5), same transaction as the status flip.
	if err := cleanupImageBytesTx(ctx, tx, id); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update by expert: %w", err)
	}
	return nil
}
```

- [ ] **Step 3.7: Add the `UpdateQuestionRequest` DTO**

In `internal/httpapi/dto/requests.go`, append:

```go
// UpdateQuestionRequest is the body of PUT /api/v1/questions/:id (expert-only,
// full-replace). multiple_correct is intentionally absent — it is derived
// (spec §3.2.2, §3.3).
type UpdateQuestionRequest struct {
	Status      string   `json:"status"      binding:"required,oneof=moderation verified error"`
	Choices     []string `json:"choices"     binding:"required,min=1,dive,required"`
	Answers     []string `json:"answers"     binding:"required,min=1,dive,required"`
	Explanation string   `json:"explanation"`
	Tags        []string `json:"tags,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
}
```

- [ ] **Step 3.8: Change the route PATCH → PUT**

In `internal/httpapi/server.go`, in the questions route group (~line 105), change:
```go
		questions.PATCH("/:id", …)   // BEFORE
		questions.PUT("/:id", …)     // AFTER
```
(Preserve the exact handler reference / `RoleGuard(expert)` wrapper already on that line — change only the HTTP verb.)

- [ ] **Step 3.9: Rewrite the handler `Update`**

In `internal/httpapi/handlers/questions.go`, replace the entire `Update` method (lines 141–191):

```go
// Update — PUT /api/v1/questions/:id. Expert only (RoleGuard enforces 403 at
// the route). Full-replace of editable fields with backend validation (spec §3.2).
func (h *QuestionHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Struct-level rule: answers must be a subset of choices, matched by exact,
	// case-sensitive Go string equality (spec §3.2.3, decision #7).
	if !answersSubsetOfChoices(req.Answers, req.Choices) {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Field rules: confidence range; tags count + non-empty (spec §3.2.3).
	confidence := 1.0 // matches today's "expert confirms => full confidence" default
	if req.Confidence != nil {
		if *req.Confidence < 0 || *req.Confidence > 1 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		confidence = *req.Confidence
	}
	if len(req.Tags) > 20 {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	for _, tg := range req.Tags {
		if tg == "" {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	}

	expertID := c.GetString("user_id")
	upd := domain.QuestionUpdate{
		Status:      req.Status,
		Choices:     req.Choices,
		Answers:     req.Answers,
		Explanation: req.Explanation,
		Tags:        req.Tags,
		Confidence:  confidence,
	}
	if err := h.questions.UpdateByExpert(c.Request.Context(), id, upd, expertID); err != nil {
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

	// Re-fetch the updated expert view for the response.
	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		slog.Warn("re-fetch after expert update failed, returning partial body",
			"question_id", id,
			"err", err)
		c.JSON(http.StatusOK, gin.H{"id": id, "status": req.Status})
		return
	}
	c.JSON(http.StatusOK, toExpertResponse(ev))
}

// answersSubsetOfChoices reports whether every answer equals some choice using
// exact, case-sensitive Go string equality (no normalization). Duplicates in
// answers are fine as long as each is present in choices (spec §3.2.3).
func answersSubsetOfChoices(answers, choices []string) bool {
	for _, a := range answers {
		found := false
		for _, ch := range choices {
			if a == ch {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
```

- [ ] **Step 3.10: Verify build + vet + unit tests**

Run:
```bash
go build ./...
go vet ./...
go test -short ./...
```
Expected: build OK; vet clean; the `TestUpdate_*` validation tests PASS (incomplete payload 400, subset violation 400, case-sensitivity 400, confidence range 400, tags-count 400, confidence-default-1.0, moderation-clears-verification), plus the existing `TestCreate_*` and `TestGet_*` still PASS.

- [ ] **Step 3.11: Commit**

```bash
git add internal/domain/question.go internal/storage/ports.go \
        internal/storage/postgres/question_repo.go \
        internal/storage/postgres/question_expert_test.go \
        internal/httpapi/dto/requests.go internal/httpapi/server.go \
        internal/httpapi/handlers/questions.go \
        internal/httpapi/handlers/questions_test.go internal/pipeline/pipeline_test.go
git commit -m "feat(questions): moderation PATCH->PUT full-replace with backend validation

Rewrites the expert moderation update as a validating PUT. Incomplete payloads
(e.g. {\"status\":\"verified\"} alone) are rejected with 400 instead of nulling
stored choices/answers. Adds struct-level answers-subset-of-choices (exact,
case-sensitive), confidence range [0,1] (default 1.0), and tags count<=20 rules.
status is honored (was forced to 'verified'); the verified_at/verified_by
invariant (NOT NULL <=> status='verified') is enforced in the repo SET via CASE.
UpdateByExpert now takes a domain.QuestionUpdate value type + expertID."
```

---

## Task 4: Integration Tests (Docker / Testcontainers required)

**Files:**
- Modify: `internal/storage/postgres/question_expert_test.go` (extend) and/or add `internal/storage/postgres/question_repo_test.go` cases
- Uses: `setupTestDB(t)` from `internal/storage/postgres/testhelpers_test.go`

> These self-skip under `-short`. Only run when Docker is up. Per spec §7.4.

- [ ] **Step 4.1: Seeding foundation (mirror `question_expert_test.go`)**

Task 4 is **acceptance-criteria-driven**: the scenarios in Step 4.2 are implemented as `t.Run` subtests, each with explicit assertions (status codes, field equality, column existence). Seeding is not design — it mirrors the patterns already proven in `internal/storage/postgres/question_expert_test.go`, which already creates users, sessions, questions, and links them via `session_questions`, then drives the repo. Read that file first and reuse its helpers/sequence rather than inventing new ones.

Build a single seeding helper (or a small `seedFixture(t)` returning a struct of IDs) used by every scenario subtest, using `setupTestDB(t)` for the pool:

- Create two users (`userA`, `userB`) and an `expert` (reuse the user/session/expert creation sequence from `question_expert_test.go`).
- Create two sessions: `sessionA` owned by `userA`, `sessionB` owned by `userB` (both `status='open'`, far-future expiry).
- Create a representative question set linked via `session_questions` (use `repo.Create` + `repo.LinkToSession`):
  - In `sessionA`: one question of each status — `moderation`, `verified` (with `verified_at`/`verified_by` set), and `error`.
  - In `sessionB`: one `verified` question (`qb`).
- Capture the IDs (`userA.ID`, `userB.ID`, `expert.ID`, `sessionA`, `sessionB`, per-question IDs) into the returned struct so each subtest references them concretely.

> No `// ...` ellipses and no `_ = got` no-ops: every scenario below has a concrete seed-then-assert shape. If a helper in `question_expert_test.go` already performs part of the seeding, call it directly instead of duplicating.

- [ ] **Step 4.2: Scenario checklist (each item is a subtest with explicit assertions)**

Implement each scenario as its own `t.Run` subtest inside a top-level `_Integration` test gated by `if testing.Short() { t.Skip("integration: requires Docker") }`. Handler-level scenarios (IDOR, scoping, PUT validation) drive the real HTTP layer through the Gin router assembled in the test (mirror the router wiring from `internal/httpapi/server.go` or a minimal equivalent); repo-level scenarios call `repo.*` directly. HTTP scenarios assert exact status codes **and** response shape (returned question IDs, field values).

- [ ] **(a) IDOR — cross-session read is blocked at the handler.**
  - Seed via Step 4.1.
  - As `userA`: `GET /api/v1/questions?session_id=<sessionB>` → assert **403** (not-owner). The repo *would* return B's rows (proven in (b)), so this asserts the handler ownership gate is the real authorization boundary, not accidental SQL filtering.
  - As `userA`: `GET /api/v1/questions?session_id=<sessionA>` → assert **200** and that only `sessionA`'s question IDs appear in `data`.

- [ ] **(b) Unified scoping — `session_id` yields that session's questions for both roles.**
  - `repo.ListForSession(ctx, sessionB, "", 20, 0)` → assert the result contains exactly `qb` (proves the repo-level read is session-scoped and would expose B's rows — the justification for asserting 403 rather than 404 in (a)).
  - As `expert`: `GET /api/v1/questions?session_id=<sessionA>` → assert **200** and only `sessionA`'s question IDs (experts may inspect any session but are still scoped to it).
  - As `expert`: `GET /api/v1/questions` (no `session_id`) → assert **200** and the global queue contains questions from **both** sessions.
  - As `userA`: `GET /api/v1/questions` (no `session_id`) → assert **403**.

- [ ] **(c) Status-absent means all statuses (within the scope).**
  - As `expert`: `GET /api/v1/questions?session_id=<sessionA>` (no `status`) → assert the response includes the `moderation`, `verified`, **and** `error` questions seeded in `sessionA`.
  - As `userA`: same request → same assertion (a user sees all statuses within an owned session).

- [ ] **(d) Status filter narrows the scope.**
  - As `expert`: `GET /api/v1/questions?session_id=<sessionA>&status=verified` → assert **only** the verified question is returned.
  - `repo.ListForSession(ctx, sessionA, domain.QuestionStatusVerified, 20, 0)` → assert the same single verified row (repo-level confirmation of the conditional `WHERE`).

- [ ] **(e) PUT full-replace — incomplete payload rejected, row untouched.**
  - Snapshot the target question's stored `choices`/`answers`/`status` (read via `repo.FindExpertByID`).
  - As `expert`: `PUT /api/v1/questions/<qA-moderation>` with body `{"status":"verified"}` only → assert **400**.
  - Re-read the row; assert `choices`, `answers`, and `status` are **unchanged** (pre == post).

- [ ] **(f) PUT full-replace — complete payload updates every editable field.**
  - As `expert`: `PUT /api/v1/questions/<qA>` with a full body: new `choices`, `answers`, `explanation`, `tags`, `confidence`, and `status:"verified"`.
  - Re-read via `repo.FindExpertByID`; assert `choices`, `answers`, `explanation`, `tags` (order-insensitive set equality), and `confidence` all match the payload.

- [ ] **(g) `verified_at`/`verified_by` invariant (`NOT NULL ⇔ status='verified'`).**
  - From a `moderation` start: PUT `status:"verified"` → assert `verified_at IS NOT NULL` and `verified_by == expertID`.
  - Then PUT `status:"moderation"` → assert `verified_at IS NULL` **and** `verified_by IS NULL`.
  - Repeat with `status:"error"` → same NULL assertion. (Assert the invariant after each transition.)

- [ ] **(h) Migration 0004 — `multiple_correct` column is gone; existing answers still derive correctly.**
  - After `setupTestDB(t)` (which runs all embedded migrations, including 0004), query and assert `exists == false`:
    ```sql
    SELECT EXISTS (SELECT 1 FROM information_schema.columns
                   WHERE table_name='questions' AND column_name='multiple_correct')
    ```
  - Re-read a seeded question whose `answers` has length > 1 (seed one if the base fixture lacks it) via `repo.FindExpertByID`; assert the response's `multiple_correct` is `true` (derived from `len(answers) > 1`), proving pre-0004 stored data still produces correct derived behavior.

- [ ] **(i) `answers ⊆ choices` enforcement (integration, end-to-end).**
  - As `expert`: `PUT /api/v1/questions/<qA>` with `choices:["A","B"]`, `answers:["C"]` → assert **400** (handler struct-level validation exercised through the real router).
  - Re-read the row; assert it is untouched (pre == post).

- [ ] **Step 4.3: Verify integration tests (Docker must be running)**

Run:
```bash
go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s
```
Expected: all PASS. (If Docker is not running, these skip under `-short` or fail to start the container — confirm Docker is up first with `docker info`.)

- [ ] **Step 4.4: Commit**

```bash
git add internal/storage/postgres/question_expert_test.go internal/storage/postgres/question_repo_test.go
git commit -m "test(questions): integration coverage for ownership, unified scoping, PUT, migration 0004"
```

---

## Self-Review (spec coverage checklist)

- **§3.1.1 behavior matrix** → Task 2 Step 2.6 handler (`session_id` present/absent × role, 403 user-without-session, 403 not-owner, 404 missing).
- **§3.1.2 status param uniform + shared validation before split** → Task 2 Step 2.6 (shared block at top of `List`).
- **§3.1.3 unified session-scoped read** → Task 2 Step 2.4 (`ListForSession`) + Step 2.6 (DTO mapper picks shape) + `toExpertResponseFromSession`.
- **§3.1.4 ownership-gap fix / 404-vs-403 split** → Task 2 Step 2.6 (distinct err→404 / mismatch→403 branches) + tests in Step 2.1.
- **§3.1.5 two listing methods (`ListForSession` + conditional `ListForModerationExpert`); delete `ListForUser`/`ListForModeration`; expand SELECT+scan; reuse `QuestionWithSession`** → Task 2 Steps 2.3–2.4.
- **§3.2.2 editable DTO** → Task 3 Step 3.7 (`UpdateQuestionRequest`).
- **§3.2.3 validation (binding + struct `answers ⊆ choices` exact/case-sensitive; confidence; tags)** → Task 3 Steps 3.7 + 3.9 (`answersSubsetOfChoices`).
- **§3.2.4 status/verified_at invariant** → Task 3 Step 3.6 (CASE in SET).
- **§3.2.5 `domain.QuestionUpdate` + signature + nil-normalization defense-in-depth** → Task 3 Steps 3.3, 3.4, 3.6.
- **§3.3 derive `MultipleCorrect` (domain method, migration 0004, storage/pipeline/schema removal, response DTO keeps wire field, request DTO drops)** → Task 1 (all steps).
- **§5 migration 0004** → Task 1 Step 1.5.
- **§6 ownership-gap security verification** → Task 2 Step 2.1 tests + Task 4 Steps 4.1–4.2 (IDOR scenario (a), unified-scoping scenario (b)).
- **§7.1 unit tests (validation)** → Task 3 Step 3.1.
- **§7.2 unit tests (listing scoping)** → Task 2 Step 2.1.
- **§7.3 unit tests (`MultipleCorrect` derivation)** → Task 1 Step 1.1.
- **§7.4 integration tests** → Task 4.

**Type/name consistency check:** `MultipleCorrect()` (value receiver) used consistently in `toUserResponse`, `toExpertResponse`, `toExpertResponseFromSession`. `ListForSession` signature identical in interface (Step 2.3), repo (2.4), both fakes (2.1, 2.5). `UpdateByExpert(ctx, id, domain.QuestionUpdate, expertID)` identical in interface (3.4), repo (3.6), both fakes (3.4), integration call sites (3.5). `domain.QuestionUpdate` field names (`Status`, `Choices`, `Answers`, `Explanation`, `Tags`, `Confidence`) match between definition (3.3), handler construction (3.9), repo consumption (3.6), and fake capture (3.4).

**No placeholders:** every step shows the actual code or an exact edit. The one exception is Task 4, which is **acceptance-criteria-driven**: seeding mirrors the proven patterns in `internal/storage/postgres/question_expert_test.go`, and each scenario in Step 4.2 is a subtest with explicit assertions (status codes, field equality, column existence) — no `_ = got` no-ops or `// ...` ellipses.
