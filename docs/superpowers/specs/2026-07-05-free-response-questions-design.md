# Free-Response Question Support — Design Specification

**Status:** Approved
**Date:** 2026-07-05
**Target service:** Coeus (`cmd/coeus`, Go 1.26.3, Gin, PostgreSQL + pgvector)

---

## 1. Overview

Coeus currently ingests exam images and parses **multiple-choice (MC)** questions
only: the extractor transcribes a set of `choices`, the verifier solves by
selecting among them, and answers are stored and returned as values drawn from
that choice set.

This spec adds **free-response (FR) questions** — questions that have **no
answer choices** but **do have an input field** the solver must fill in (e.g.
`Ответ: ______`, `[input]` for digital forms, a blank after an equation). The
server must return the answer as a free value in `answers` with `choices: []`.

The change is deliberately narrow: no new question *category* (matching,
ordering, etc.) is introduced — only the MC/FR axis. The single new bit of
information is an explicit `question_type` discriminator, derived deterministically
from the extracted choices, persisted, and surfaced in the API.

### Goals

- Recognize and correctly transcribe free-response questions end to end
  (extraction → dedup → verification → storage → API).
- Persist an explicit `question_type` so reads and expert edits are unambiguous
  even before answers are resolved.
- Allow experts to create and edit free-response questions through the existing
  `POST /api/v1/questions` and `PUT /api/v1/questions/:id` endpoints.
- Serialize free-response answers cleanly in the user-facing response — no
  phantom empty `id` field on answer refs.

### Non-goals

See §7 (Out of scope).

---

## 2. Architecture decision: pipeline-inferred type, persisted as expert-editable column

The question type is a **deterministic function of the extracted choices**:

```
len(choices) == 0  →  free_response
len(choices)  > 0  →  multiple_choice
```

It is computed **once**, in Go, at the `ExtractedQuestion → domain.Question`
mapping boundary inside the pipeline, and persisted to a new `question_type` DB
column. From that point on the type is an ordinary editable field, exactly like
`choice_labeling`.

### Why this approach

Three alternatives were rejected:

| Alternative | Problem |
|---|---|
| **Extractor returns a `type` field** | Adds a new field to the AI JSON contract. Increases prompt complexity, risks schema drift, and the value is 100% redundant with `len(choices)`. The extractor's auto-generated JSON schema (via `invopop/jsonschema` reflection) would change, raising the chance of a silent regression. |
| **Compute on read** (`len(choices)==0`) | Cheap, but makes the discriminator implicit. Expert edits (Create/Update) would have no single source of truth; validation rules would have to re-derive it everywhere. Breaks the precedent set by `choice_labeling`, which *is* persisted. |
| **Verifier decides the type** | The verifier never needs to know the type — it already consumes `choices: []` and already handles open-ended questions. Adding type to its contract buys nothing and costs an AI-round-trip. |

The chosen approach **mirrors the existing `InferChoiceLabeling` / `choice_labeling`
precedent exactly**: a pure inference function in the domain layer, applied at
the one mapping site, stored in its own column, editable by experts. This gives:

- **Zero AI-contract risk** — neither skill's JSON output schema changes.
  Extractor DTO structs are untouched; the verifier flow is untouched.
- **One derivation site** — the pipeline. Every other layer reads the stored
  value.
- **Consistency with `choice_labeling`** — same pattern, same test shape, same
  edit semantics.

### Skills are still updated — but behaviorally, not structurally

The two `SKILL.md` files are updated to *improve recognition and answering
quality* for free-response questions. These are prompt/prose changes only. They
do **not** add a `type` field to either skill's output schema.

---

## 3. Detailed design

### 3.1 Domain model — `internal/domain/question.go`

Add type constants next to the existing `ChoiceLabeling*` constants:

```go
const (
    QuestionTypeMultipleChoice = "multiple_choice"
    QuestionTypeFreeResponse   = "free_response"
)
```

Add a `Type string` field to **both** `Question` and `QuestionUpdate`, placed
immediately after `ChoiceLabeling` (mirrors column/scan ordering, see §3.4):

```go
type Question struct {
    // ... existing fields ...
    Choices         []string
    Answers         []string
    ChoiceLabeling  string
    Type            string   // <-- new
    Confidence      float64
    // ...
}

type QuestionUpdate struct {
    Status      string
    Choices     []string
    Answers     []string
    Explanation string
    Tags        []string
    Confidence  float64
    Type        string   // <-- new (place after a logically adjacent field;
                          //     QuestionUpdate has no ChoiceLabeling)
}
```

Add a pure inference function modeled on `InferChoiceLabeling`:

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

`MultipleCorrect()` is **unchanged** (`len(Answers) > 1`). The MC/FR axis and
the single/multi-correct axis are orthogonal: a free-response question can still
have multiple acceptable answers.

### 3.2 Skills — behavioral updates only

The skills are embedded via `//go:embed` in `skills/skills.go` and assigned to
the package-level `systemPrompt` variables in `internal/ai/extractor/prompt.go`
and `internal/ai/verifier/prompt.go`. Edits happen in the two `SKILL.md` files;
the Go embed wiring is untouched.

**No JSON output schema changes to either skill.** The extractor DTO structs
(`extractor/schema.go`) and verifier DTO structs (`verifier/schema.go`) are
unchanged. (This is itself a testable invariant — see §3.6.)

#### 3.2.1 Extractor skill — `skills/extract-questions-from-image/SKILL.md`

Add a new subsection titled **"Recognizing Free-Response Questions"** alongside
the existing MC guidance. Content:

- **Visual signals** that indicate a free-response question (set `choices: []`):
  - Underscore runs or blank lines acting as an answer field: `Ответ: ______`,
    `Answer: ____`, a trailing blank line.
  - An answer prompt (`Ответ:`, `Answer:`, `=`, `?`) with **no enumerated
    choices following it**.
  - Digital form placeholders: `[input]`, `[____]`, an empty text box glyph.
  - A gap inside a sentence or equation the solver is meant to fill
    (`v = ___ м/с`, `The capital of France is ___`).
- **Guidance:** when these signals are present, **confidently emit
  `choices: []`**. This is a positive, deliberate recognition — not an absence
  of data. Keep the surrounding transcription `confidence` high; the missing
  choices are expected, not a parse failure.
- **Answer transcription:** if the image shows a pre-filled answer in the input
  field (e.g. a worked exam), transcribe it into `answers` as usual. If the
  field is blank, emit `answers: []` and let the verifier fill it.
- Add a **free-response example** next to the existing MC example, showing the
  same DTO shape with `choices: []`.

#### 3.2.2 Verifier skill — `skills/verify-extracted-questions/SKILL.md`

The verifier already consumes `choices: []` and already documents the
open-ended answer format (`answers: [{"value": "..."}]`, `choices: []`). Add
**confidence-tier guidance** for free-response questions:

- **Short objective answers** — formulas, numbers, single words, names, units
  (the common case). Solve at **confidence ≥ 0.80**.
- **Detailed / subjective answers** — algorithms, proofs, "развернутый ответ",
  multi-sentence explanations. Produce a **best-effort** answer at
  **0.50–0.79**. These cannot be graded automatically; the moderate confidence
  signals "plausible, needs human review."
- **Answer format guidance:** produce **exactly what fills the input field** —
  include units if implied by the prompt (`2 м/с²`, not `2`), omit surrounding
  prose. The value must be directly droppable into the blank.

No structural change to the verifier DTO or flow.

### 3.3 Pipeline — `internal/pipeline/pipeline.go`

**One line** added to the `ExtractedQuestion → domain.Question` mapping block
(currently at `pipeline.go:198-209`, the `q := &domain.Question{...}` literal
inside the per-question loop):

```go
q := &domain.Question{
    Number:          eq.Number,
    Text:            eq.Text,
    TextNorm:        norm,
    TextHash:        hash,
    Choices:         answerTexts(eq.Choices),
    Answers:         answerTexts(eq.Answers),
    ChoiceLabeling:  domain.InferChoiceLabeling(answerIDs(eq.Choices)),
    Type:            domain.InferQuestionType(answerTexts(eq.Choices)),  // <-- new
    Status:          domain.QuestionStatusModeration,
    Embedding:       embedding,
    Tags:            append([]string{"ai-generated"}, eq.Tags...),
}
```

#### Error placeholder path

`handleExtractionFailure` (`pipeline.go:297`) creates a sentinel question with
`Status: domain.QuestionStatusError` and **no `Choices`/`Answers` set** (they
remain nil). Without an explicit type, `InferQuestionType(nil)` would classify
these as `free_response`, which is semantically wrong — they are not
free-response questions, they are failure placeholders.

Fix: set the type explicitly in the placeholder literal:

```go
q := &domain.Question{
    Number:   0,
    Text:     "Extraction failed: " + code,
    TextNorm: "extraction failed " + code,
    TextHash: hash,
    Type:     domain.QuestionTypeMultipleChoice,  // <-- explicit; failure placeholders are not FR
    Status:   domain.QuestionStatusError,
    Tags:     []string{"extraction-failed"},
}
```

#### What does NOT change

- `ports.go` types — `ExtractedQuestion`, `VerifiedQuestion`, and `Answer` get
  **no `Type` field**. The type is a storage/domain concept; it does not cross
  the AI boundary.
- Verifier flow — `Verify`, `resolveVerifiedAnswers`, and
  `UpdateFromVerification` are untouched. The verifier already handles
  `choices: []`; free-response answers flow through `resolveVerifiedAnswers`
  exactly like MC answers.
- `UpdateFromVerification` writes only `answers`, `confidence`, `explanation`
  — it never touches `question_type`, so a verifier round never reclassifies a
  question. Correct: type is fixed at extraction time.

### 3.4 Storage

#### 3.4.1 Migration — `internal/storage/postgres/migrations/0005_add_question_type.sql`

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

Design notes:

- `NOT NULL DEFAULT 'multiple_choice'` — every existing row gets a value
  without a multi-step migration.
- The `CHECK` constraint makes the two-value domain explicit at the DB level
  and gives a second layer of defense beyond handler validation.
- The backfill `WHERE status <> 'error'` mirrors the runtime decision to mark
  error placeholders as `multiple_choice` (§3.3). Without it, pre-existing
  error rows with empty choices would flip to `free_response` and mislabel the
  failure placeholders.

#### 3.4.2 Repository — `internal/storage/postgres/question_repo.go`

The repo uses **positional** `Scan` into a fixed column list. Adding a column
therefore requires updating every SELECT constant and every scan function in
lockstep. This is the highest-risk part of the change (see §6).

**Insert `q.question_type` immediately after `q.choice_labeling`** in all six
places below. That single ordering choice keeps the SELECT column list, the
scan argument order, and the domain struct field order all aligned.

1. **Three SELECT constants** — add `q.question_type,` on its own line right
   after the `q.choice_labeling,` line:
   - `questionSelectBase`
   - `questionExpertSelectBase`
   - `questionWithSessionSelectBase`

2. **Three scan functions** — add `&q.Type,` (or `&qws.Type,`) immediately
   after the `&q.ChoiceLabeling,` argument in the `row.Scan(...)` call:
   - `scanQuestion`
   - `scanQuestionExpert`
   - `scanQuestionWithSession`

3. **`Create` INSERT** — add `question_type` to the column list (after
   `choice_labeling`) and `q.Type` to the VALUES argument list (after
   `q.ChoiceLabeling`). Renumber the placeholder list (`$1..$13` → `$1..$14`).

4. **`UpdateByExpert` UPDATE** — add `question_type = $N` to the `SET` clause
   and `upd.Type` to the argument slice. The current SET args are `$1`–`$7`
   (choices, answers, explanation, tags, confidence, status, id). Insert
   `question_type` and renumber: the exact position depends on where in the SET
   list it is inserted; the implementer must verify all `$N` references and the
   `args` slice length stay aligned. Place `question_type` right after
   `choice_labeling` conceptually, but note that `UpdateByExpert` does not
   currently write `choice_labeling` (it's immutable post-creation), so
   `question_type` can go at any consistent position in the SET clause.

5. **`UpdateFromVerification`** — **no change**. It writes only `answers`,
   `confidence`, `explanation`; type is immutable post-extraction.

### 3.5 HTTP API

#### 3.5.1 Response DTOs — `internal/httpapi/dto/question.go`

Add `Type` to both response structs and make `AnswerRef.ID` omitempty:

```go
type AnswerRef struct {
    ID    string `json:"id,omitempty"`   // was: json:"id"
    Value string `json:"value"`
}

type UserQuestionResponse struct {
    // ... existing fields ...
    Type            string      `json:"type"`     // <-- new
    // ...
}

type ExpertQuestionResponse struct {
    // ... existing fields ...
    ChoiceLabeling  string      `json:"choice_labeling"`   // unchanged (already present)
    Type            string      `json:"type"`              // <-- new
    // ...
}
```

> `Type` is placed adjacent to `ChoiceLabeling` for readability; field order in
> the struct has no effect on JSON output (tags control the wire keys).

#### 3.5.2 Response builders — `internal/httpapi/handlers/questions.go`

All three builders set `Type: q.Type` (or `qq.Type`):

- `toUserResponse` (`handlers/questions.go:349`)
- `toExpertResponse` (`handlers/questions.go:363`)
- `toExpertResponseFromSession` (`handlers/questions.go:391`)

#### 3.5.3 Request DTOs — `internal/httpapi/dto/requests.go`

Add a required `Type` discriminator to both request structs and **relax** the
`Choices` binding so an empty slice is permitted (free-response):

```go
type CreateQuestionRequest struct {
    Question        string   `json:"question" binding:"required"`
    Type            string   `json:"type" binding:"required,oneof=multiple_choice free_response"`  // <-- new
    Choices         []string `json:"choices" binding:"dive,required"`     // was: required,min=2
    Answers         []string `json:"answers" binding:"required,min=1"`    // unchanged
    ChoiceLabeling  string   `json:"choice_labeling"`
    Explanation     string   `json:"explanation"`
    Tags            []string `json:"tags"`
    Confidence      *float64 `json:"confidence"`
}

type UpdateQuestionRequest struct {
    Status      string   `json:"status"      binding:"required,oneof=moderation verified error"`
    Type        string   `json:"type"        binding:"required,oneof=multiple_choice free_response"`  // <-- new
    Choices     []string `json:"choices"     binding:"dive,required"`     // was: required,min=1,dive,required
    Answers     []string `json:"answers"     binding:"required,min=1,dive,required"`  // unchanged
    Explanation string   `json:"explanation"`
    Tags        []string `json:"tags,omitempty"`
    Confidence  *float64 `json:"confidence,omitempty"`
}
```

Binding-tag rationale:

- `Choices` drops `required,min=N` and keeps only `dive,required`: the slice
  itself may be empty (FR), but **if** elements are present each must be
  non-empty. The structural `min` check moves into the handler (§3.5.4) where
  it can be made type-conditional.
- `Answers` stays `required,min=1`: every question, MC or FR, must have at
  least one answer.

#### 3.5.4 Handler validation — `internal/httpapi/handlers/questions.go`

Today the struct-level rule `answersSubsetOfChoices` (line 234) is enforced
**only in `Update`** (line 171). The `Create` handler (line 252) has no
answers-subset-of-choices check today — it relied on the old `binding:"required,min=2"`
tag on `Choices` to ensure structure. Now that the binding is relaxed (to allow
empty choices for FR), both handlers need explicit type-conditional validation
in the handler body (after `ShouldBindJSON`). For `Update`, this replaces the
existing `answersSubsetOfChoices` call. For `Create`, this is new logic that
replaces the old binding-only enforcement:

```go
// Structural rules are type-conditional. Binding guarantees:
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
    // no subset checks — answers are free values
}
```

`answersSubsetOfChoices` itself is unchanged.

**Nil-to-empty normalization:** with `required` dropped from the `Choices`
binding, a missing or `null`-valued `choices` field in the JSON body binds to
`nil` in Go. `json.Marshal(nil)` produces `"null"`, which would be stored as a
SQL `NULL` rather than `'[]'`, and the migration backfill
(`WHERE choices = '[]'::jsonb`) would miss those rows. Normalize nil to an
empty slice before constructing the domain object — the `UpdateByExpert` path
already does this (lines 234–237); apply the same normalization in `Create`:

```go
choices := req.Choices
if choices == nil {
    choices = []string{} // normalize nil → empty for FR questions with omitted choices
}
```

Both handlers must also thread `Type` into the constructed domain object:

- `Create`: add `Type: req.Type` to the `domain.Question` literal; pass the
  normalized `choices` slice (not `req.Choices` directly).
- `Update`: add `Type: req.Type` to the `domain.QuestionUpdate` literal.

#### 3.5.5 Free-response response shape

A free-response question returned to a user serializes as:

```json
{
  "id": "0a1b...",
  "number": 7,
  "question": "Чему равно ускорение тела через 2 с?",
  "type": "free_response",
  "multiple_correct": false,
  "choices": [],
  "answers": [
    { "value": "2 м/с²" }
  ],
  "status": "verified",
  "confidence": 0.86
}
```

The `omitempty` on `AnswerRef.ID` (§3.5.1) is what makes the answer entry
serialize as `{"value": "..."}` rather than `{"id": "", "value": "..."}`:
`idForValue` already returns `""` for any value not found in `choices`, and
with empty choices every answer misses. The phantom-empty-`id` problem
disappears without any change to `DeriveAnswerRefs` or `idForValue`.

The expert response is the same plus `explanation`, `tags`, `choice_labeling`,
`verified_at`, `verified_by`, etc.

---

## 4. Change surface

| File | Change |
|---|---|
| `internal/domain/question.go` | + `QuestionType*` constants, + `Type` field on `Question` and `QuestionUpdate`, + `InferQuestionType` |
| `internal/pipeline/pipeline.go` | + 1 line in extraction mapping; + 1 line (`Type`) in `handleExtractionFailure` placeholder |
| `internal/storage/postgres/migrations/0005_add_question_type.sql` | **new file** |
| `internal/storage/postgres/question_repo.go` | 3 SELECT constants, 3 scan functions, `Create` INSERT, `UpdateByExpert` SET — all get `question_type` after `choice_labeling` |
| `internal/httpapi/dto/question.go` | + `Type` on `UserQuestionResponse` / `ExpertQuestionResponse`; `AnswerRef.ID` → `omitempty` |
| `internal/httpapi/dto/requests.go` | + `Type` on `CreateQuestionRequest` / `UpdateQuestionRequest`; relax `Choices` binding |
| `internal/httpapi/handlers/questions.go` | type-conditional validation in `Create` + `Update`; thread `Type` into domain objects; set `Type` in 3 response builders |
| `skills/extract-questions-from-image/SKILL.md` | + "Recognizing Free-Response Questions" subsection + FR example (prose only) |
| `skills/verify-extracted-questions/SKILL.md` | + free-response confidence tiers + answer-format guidance (prose only) |

**Not touched:** `ports.go`, extractor/verifier `schema.go` DTOs,
`UpdateFromVerification`, `resolveVerifiedAnswers`, auth, config, `wire.go`,
worker pool, reaper, migrations 0001–0004.

---

## 5. Testing strategy

### 5.1 Domain unit tests — `internal/domain`

Add a table-driven `TestInferQuestionType` covering: nil slice, empty slice,
single choice, many choices. Pure function, no deps; runs under `-short`.

### 5.2 Storage integration tests — `internal/storage/postgres` (Docker required)

Round-trip both types through `Create` → `FindByID` / `FindExpertByID` /
`FindForUserByID`; assert `Type` round-trips for both `multiple_choice` and
`free_response`. Then `UpdateByExpert` a question's `Type` and assert
persistence. These tests are the **critical guard against positional column
drift** — if any of the six repo edits (§3.4.2) is misaligned, the scan will
return a wrong value or error. Use `setupTestDB(t)` from
`internal/storage/postgres/testhelpers_test.go`. Self-skip under `-short`.

### 5.3 Pipeline tests — `internal/pipeline`

Using the existing fake AI clients (no Docker):
- free-response inference: extractor returns `Choices: []` → stored question
  has `Type == free_response`.
- MC inference: extractor returns ≥1 choice → `Type == multiple_choice`.
- error placeholder: `handleExtractionFailure` produces a question with
  `Type == multiple_choice` and `Status == error` (guards the §3.3 fix).
- verifier override for free-response: verifier returns an answer for a
  `choices: []` question; `UpdateFromVerification` persists it without
  altering `Type`.

### 5.4 Handler tests — `internal/httpapi/handlers`

Validation matrix (apply to both `Create` and `Update`):

| `type` | `choices` | `answers` | expected |
|---|---|---|---|
| `multiple_choice` | 2+, each in choices | ≥1, ⊆ choices | 200/201 |
| `multiple_choice` | 1 | ≥1 | 400 |
| `multiple_choice` | 2, answer not in choices | ≥1 | 400 |
| `free_response` | `[]` | ≥1 | 200/201 |
| `free_response` | non-empty | ≥1 | 400 |
| `free_response` | `[]` | `[]` | 400 (binding) |
| *(missing/invalid `type`)* | — | — | 400 (binding) |

Serialization test: a free-response `UserQuestionResponse` marshals to JSON
with `answers[0]` containing **no `id` key** (verifies the `omitempty` fix).

### 5.5 Skills — manual verification, no automated tests

- Verify both `SKILL.md` files are well-formed Markdown (an editor/linter pass
  is sufficient; there is no markdown linter in the repo).
- **Schema regression guard:** confirm the extractor DTO structs in
  `internal/ai/extractor/schema.go` are unchanged — the auto-generated
  JSON schema (via `invopop/jsonschema`) must not have gained a `type` field.
  A cheap way to enforce this in CI later is a golden-file snapshot of the
  generated schema; out of scope for this change (§7).

---

## 6. Risks and mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| **Positional column desync** in `question_repo.go` — any of the 6 edits out of order yields wrong data or a scan error at runtime. | High: silent mislabeling or 500s on every question read. | (1) Insert `question_type` in the **same logical position** (after `choice_labeling`) everywhere. (2) The §5.2 round-trip tests fail loudly on any mismatch. (3) Code review focuses on this file. |
| **Backfill mislabels error placeholders** as `free_response`. | Medium: existing error rows have empty choices; naive `WHERE choices = '[]'` would reclassify them. | Migration backfill includes `AND status <> 'error'`, matching the runtime rule that error placeholders are `multiple_choice`. |
| **`omitempty` on `AnswerRef.ID` is a wire-format change** for MC answers too. | Low: for MC, `idForValue` always finds a match (answers ⊆ choices by validation), so `id` is non-empty and still emitted. Only the previously-broken empty case changes. | Covered by the §5.4 MC serialization path; the MC response is byte-identical before/after. |
| **Future third question type** (matching, ordering) breaks the binary. | Low (future). | The `CHECK` constraint and the `oneof` binding tag are the only binary assumptions; both are single-line edits when a third type arrives. `InferQuestionType` stays valid as the MC/FR default classifier. |
| **Skill prose edits regress extraction quality.** | Medium: behavioral prompt changes are hard to test. | Keep changes additive (new subsections, new examples); do not alter existing MC guidance. Schema-regression guard (§5.5) catches structural drift. |
| **Expert sets `type` inconsistent with `choices`.** | Medium: e.g. `type=multiple_choice` with `choices=[]`. | Handler-level type-conditional validation (§3.5.4) rejects the combination with 400. The DB `CHECK` is a backstop, not the primary guard. |

---

## 7. Out of scope

- **New question categories** beyond MC/FR — matching, ordering, fill-in-the-blank-with-bank,
  hot-spot, etc. The type system is designed to accommodate them later, but
  none are added now.
- **Automated skill regression testing** (golden-file snapshots of the
  generated JSON schema, prompt eval harnesses). Noted as future work in §5.5.
- **CI changes.** There is no CI today (per `AGENTS.md`); none is added here.
- **API versioning / a new endpoint.** Free-response rides the existing
  `POST /api/v1/questions`, `PUT /api/v1/questions/:id`, and read endpoints.
- **Changing `MultipleCorrect()` semantics.** It remains `len(Answers) > 1`,
  orthogonal to type.
- **Embedding/dedup changes.** Free-response questions dedup identically to MC
  (by `question_hash` and semantic embedding of the question text).
