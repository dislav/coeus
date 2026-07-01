# Question Listing & Moderation — Design Specification

- **Date:** 2026-07-01
- **Status:** Approved
- **Service:** coeus (Go 1.26, Gin, PostgreSQL + pgvector, CGO/libvips)
- **Scope:** Three coupled changes to the questions API — (1) unified listing/filtering scoping with an ownership fix, (2) moderation update rewritten from `PATCH` partial to `PUT` full-replace with backend validation, (3) removal of the redundant stored `multiple_correct` column in favor of a derived domain method.

## Summary

This spec unifies question listing behind a single `session_id`-driven scoping rule (role governs only authorization and response shape), tightens the ownership boundary so one user cannot read another's session questions, replaces the lossy `PATCH` moderation endpoint with a validating full-replace `PUT` that prevents nil-slice overwrites, and collapses the duplicated `multiple_correct` column into one derived definition (`len(answers) > 1`) while preserving the existing frontend JSON contract.

---

## 1. Background / Current Behavior

### 1.1 Data-model ground truth

- `questions` is a **global canonical store** with no `session_id` column. `question_hash` is `UNIQUE` (exact-hash dedup). (`internal/storage/postgres/migrations/0002_core.sql:26-46`)
- `session_questions` is an **N:M junction** `(session_id, image_id, question_id)` linking canonical questions to the sessions in which they appeared. (`0002_core.sql:48-59`)
- The pipeline calls `QuestionRepo.LinkToSession(job.SessionID, job.ImageID, questionID, …)` for every extracted or dedup-matched question (`internal/pipeline/pipeline.go:144,167,192,282`). Therefore **"questions loaded during a session" = `session_questions WHERE session_id = X`**.

### 1.2 The four observed issues

1. **Listing scoping is role-coupled.** Today the listing handler branches on role: users are forced into a session-scoped path, experts into a global path. Two parallel code paths diverge in shape and behavior.
2. **Status default differs by role.** The expert path forces `status = "moderation"` via `DefaultQuery("status","moderation")` (`handlers/questions.go:52-56`); users get no default. The same `status` parameter therefore means different things depending on who calls.
3. **Missing `session_id` returns 400 for users.** A user omitting `session_id` gets a 400 "missing session_id" — but this is an **authorization** boundary (a user has no global-queue entitlement), so the correct code is 403.
4. **Ownership check gap on the list path (security).** `ListForUser` filters only `WHERE sq.session_id = $1` with **no user-ownership check** (`question_repo.go:121-157`). Any authenticated user can read another user's questions by guessing a `session_id`. The single-item `FindForUserByID` (`question_repo.go:259-279`) already enforces ownership via `s.user_id = $2`; the list path does not.

### 1.3 Moderation update bug

`PATCH /api/v1/questions/:id` (handlers/questions.go:142-191 → `UpdateByExpert`, `question_repo.go:281-322) does an **unconditional `UPDATE`** against whatever the client sent. Nil slices marshal to JSON `null`, so e.g. `{"status":"verified"}` alone overwrites stored `choices`/`answers` with null. The handler also **forces** `status='verified'`, ignoring the client's stated status. There is no validation that `answers ⊆ choices`.

### 1.4 `multiple_correct` redundancy

`questions.multiple_correct` is a stored bool. The frontend never consumes it for rendering or logic — it derives "has multiple answers" from `len(answers)`. The stored column is pure redundancy with two definitions that can drift.

---

## 2. Goals & Non-Goals

### Goals
- G1 — One scoping rule for listing, driven by `session_id`, identical SQL for both roles when a session is in scope.
- G2 — Uniform, optional `status` filter (absent ⇒ all statuses) across both modes.
- G3 — Close the ownership gap: a user cannot read another user's session questions.
- G4 — Moderation becomes a validating full-replace (`PUT`); incomplete/invalid payloads are rejected, never applied.
- G5 — `multiple_correct` has exactly one definition, in one location (a domain method), backed by no column.

### Non-Goals
- Making question **text** editable (forces `text_hash`/`embedding` recompute + re-dedup — explicitly deferred; see §9).
- Making `choice_labeling` or other AI-derived metadata editable.
- Re-introducing an "allows multiple" *question-type* signal. The semantic distinction between "type allows multiple" and "count > 1" is real in the abstract but currently unconsumed; documented as rationale only (§9).
- Changing pagination mechanics (`parsePaging`, `handlers/questions.go:35-36`) or auth/JWT/`RoleGuard` plumbing.

---

## 3. Detailed Design

### 3.1 Section 1 — Listing & Filtering

**Endpoint:** `GET /api/v1/questions` (route registered in `internal/httpapi/server.go:97-106`).

**Core principle:** `session_id` (query param) drives **scoping**. Role drives **only** authorization and response shape.

#### 3.1.1 Behavior matrix

| `session_id` | Role | Behavior |
|---|---|---|
| present | user | That session's questions, **only if** the session belongs to `ctx.user_id`; else **403**. |
| present | expert | That session's questions (experts may inspect any session). |
| absent | expert | Global moderation queue (all sessions). |
| absent | user | **403 Forbidden** (currently 400; change to 403 — this is an authorization boundary). |

#### 3.1.2 Query parameters

- `session_id` — optional for experts, required-with-ownership for users (see matrix). Drives scope.
- `status` — **optional and uniform across both modes.**
  - Absent/empty ⇒ **no status filter** ⇒ all statuses returned.
  - Present ⇒ must be one of `moderation | verified | error` (constants at `internal/domain/question.go:12-16`); else **400**.
  - This **replaces** the current expert-path `DefaultQuery("status","moderation")` at `handlers/questions.go:52-56`.
  - **Validation placement:** the `status` validation (must be one of `moderation | verified | error`, else 400) — which today runs ONLY on the expert path (`internal/httpapi/handlers/questions.go:53-56`) — moves to a **shared validation block that executes BEFORE the role-split branch** in the handler, so **both** roles validate `status` uniformly. The user path today passes `c.Query("status")` straight to the repo with no validation; after this change it is validated first.
- `tag` — expert-only, optional. If supplied by a user ⇒ 400/403 per existing policy (unchained here).
- `page`, `per_page` — unchanged (parsed via `parsePaging`, `handlers/questions.go:35-36`).

#### 3.1.3 "Consistent logic for all roles" — the unified read path

When `session_id` is present, **both** roles traverse **one** session-scoped read path:

- Same SQL, joining `questions` → `session_questions` → `sessions`.
- `SELECT` returns full `domain.Question` columns **plus** the link fields: `image_id`, `extracted_number`, `extracted_confidence`.
- The DTO mapper then picks the shape:
  - user → `dto.UserQuestionResponse` (`dto/question.go:17-26`)
  - expert → `dto.ExpertQuestionResponse` (`dto/question.go:29-45`)

The global expert queue (`session_id` absent) is a **separate** query: no session join, aggregates tags.

#### 3.1.4 Ownership-gap fix

The list path must match the single-item path's ownership enforcement (`FindForUserByID`, `question_repo.go:259-279`, uses `s.user_id = $2`).

The current code at `internal/httpapi/handlers/questions.go:81-84` **conflates two distinct cases** into a single response. The handler must **separate** them:
- **Session genuinely missing** (repo lookup `err != nil`) ⇒ **404 Not Found**.
- **Session exists but `sess.UserID != userID`** ⇒ **403 Forbidden**.

Fix at the **handler** boundary for the user role:
1. Resolve the session by `session_id` (distinct lookup-error path ⇒ **404** on miss).
2. Verify `session.user_id == ctx.user_id` (distinct ownership-mismatch path ⇒ **403**).
3. Then perform the repo read filtered by `session_id` (the ownership check is already done; the repo need not re-check user).

> Do **not** simply flip a single status code to 403 — that would turn "session not found" into 403, contradicting the status-code table (§4.4: 404 not-found / 403 not-owner). Split the lookup-error path from the ownership-mismatch path so each returns its correct code.

Experts skip the ownership check (they may inspect any session).

#### 3.1.5 Repository interface changes (`internal/storage/ports.go:65-80`)

After this work there are **exactly two** question-listing repo methods: `ListForSession` (session-scoped, both roles) and `ListForModerationExpert` (global, expert). Everything else is deleted.

**Unify** the session-scoped read into one method, used by both roles, returning the **existing** `storage.QuestionWithSession` (defined at `internal/storage/ports.go:19-26`) — do **not** invent a new `SessionQuestion` struct:

```go
// ListForSession returns the full question representation plus session link
// fields (image_id, extracted_number, extracted_confidence) for every question
// linked to sessionID. statusFilter is applied only when non-empty.
ListForSession(ctx context.Context, sessionID, statusFilter string, limit, offset int) ([]QuestionWithSession, error)
```

Implementation steps for `ListForSession`:
- **Delete `ListForUser`** entirely — from the interface (`internal/storage/ports.go:71`), its impl (`internal/storage/postgres/question_repo.go:121-157`), and any test fakes. `ListForSession` fully replaces it.
- **Delete `ListForModeration`** entirely — from the interface (`internal/storage/ports.go:72`), its impl (`internal/storage/postgres/question_repo.go:159`), and **both** test fakes (`internal/httpapi/handlers/questions_test.go:64`, `internal/pipeline/pipeline_test.go:195`). It is **dead code** (`.ListForModeration(` has zero call sites; only `ListForModerationExpert` is wired to a handler) and is superseded by the two-method design.
- Expand `SELECT` to **all** `questions` columns plus the link fields. Required consequence: the current `ListForUser` SELECT at `internal/storage/postgres/question_repo.go:123` selects only ~9 columns, and `scanQuestionWithSession` at `:539-556` scans only those. **Both must be expanded** to read the full `domain.Question` column set so the expert rich DTO (`dto.ExpertQuestionResponse`) can be populated from the same read.
- `statusFilter` is applied **conditionally**: empty ⇒ skip the `AND q.status = …` clause (the same pattern `ListForUser` already uses for its filter).

**Keep** the global queue method, but make its status filter **conditional**:

```go
// ListForModerationExpert returns the global moderation queue across all sessions.
// statusFilter and tagFilter are applied only when non-empty.
ListForModerationExpert(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]Question, error)
```

- Currently always emits `WHERE q.status = $1` (`question_repo.go:218-254`). Change so an empty `statusFilter` produces **no** status `WHERE` (⇒ all statuses).

#### 3.1.6 Frontend-visible contract examples

```
GET /api/v1/questions?session_id=X        → session X's questions (user or expert)
GET /api/v1/questions                      → global queue (expert only; user → 403)
GET /api/v1/questions?session_id=X&status=verified   → verified questions in session X
GET /api/v1/questions?status=moderation    → global moderation queue (expert)
```

---

### 3.2 Section 2 — Moderation Update: `PATCH` → `PUT`, Full-Replace + Backend Validation

**Endpoint:** `PATCH /api/v1/questions/:id` → **`PUT /api/v1/questions/:id`** (expert-only; `RoleGuard` unchanged; route at `server.go:97-106`).

**Route registration edit (concrete):** `internal/httpapi/server.go:~105` changes `questions.PATCH("/:id", …)` → `questions.PUT("/:id", …)`.

`PUT` semantics: **full replacement** of the client-editable representation. The server owns the rest.

#### 3.2.1 Server-managed fields (NOT client-settable)

`id`, `number`, `text`, `text_hash`, `text_norm`, `embedding`, `created_at`, `updated_at`, `verified_at`, `verified_by`.

> Question **text** stays as-extracted this iteration. Making it editable forces `text_hash`/`embedding` recompute and re-dedup — explicitly out of scope (§9).

#### 3.2.2 Editable request DTO

`multiple_correct` is intentionally **not** editable — it is derived (§3.3).

```go
type UpdateQuestionRequest struct {
    Status      string   `json:"status"      binding:"required,oneof=moderation verified error"`
    Choices     []string `json:"choices"     binding:"required,min=1,dive,required"`
    Answers     []string `json:"answers"     binding:"required,min=1,dive,required"`
    Explanation string   `json:"explanation"`
    Tags        []string `json:"tags,omitempty"`
    Confidence  *float64 `json:"confidence,omitempty"`
}
```

#### 3.2.3 Validation (structural fix — backend owns correctness)

The current bug: an unconditional `UPDATE` applied whatever arrived, so nil slices marshaled to JSON `null` and overwrote stored values. Under `PUT` + validation, **incomplete payloads are rejected, not applied**.

Binding-level (via tags above):
- `status` ∈ `{moderation, verified, error}`.
- `choices` non-empty; each element a non-empty string.
- `answers` non-empty; each element a non-empty string.

Struct-level rule (enforce in the handler/service, **after** binding succeeds):
- **`answers ⊆ choices`** — every value in `answers` must be **equal** to some value in `choices` using **exact, case-sensitive** Go string equality (`==`). No normalization, no case-folding. Else **400**. (Decided contract — cover with a unit test; see §7.1.)

Field rules:
- `confidence` — optional; if present must be ∈ `[0,1]`; if **absent**, the server defaults to `1.0` (matches today's behavior).
- `tags` — each a non-empty string; count ≤ 20. Else **400**.

**On any failure ⇒ 400 with a precise message; the row is NEVER touched.**

> Worked example: `{"status":"verified"}` alone now fails with `"choices required"` / `"answers required"` (400). The clearing-by-omission bug cannot recur.

#### 3.2.4 Status & verification semantics

The current handler **forces** `status='verified'`. Under the new contract all three statuses are allowed, with an invariant:

| `status` | `verified_at` | `verified_by` |
|---|---|---|
| `verified` | `now()` | `ctx.user_id` |
| `moderation` | `NULL` | `NULL` |
| `error` | `NULL` | `NULL` |

**Invariant:** `verified_at IS NOT NULL ⇔ status = 'verified'`.

#### 3.2.5 Repository change (small)

`UpdateByExpert` (`question_repo.go:281-322`): its unconditional `SET` is **correct for full-replace** — the bug was unvalidated nils reaching it. Changes:

1. **Validation guarantees** `choices`/`answers` are non-nil before the call ⇒ `json.Marshal` yields a real array, never `null`.
2. **Defense-in-depth:** inside the repo, normalize any nil slice to `[]` (empty) before marshal — so a future caller cannot reintroduce the bug. Validation (§3.2.3) is the **primary** guard guaranteeing non-nil at the call site; this repo-level normalization is **secondary insurance** only.
3. **Adjust the `SET` clause** to honor the new status/`verified_at` logic (currently hard-codes `status='verified'`):
   - `status = verified` ⇒ `verified_at = now(), verified_by = $user`.
   - `status ∈ {moderation, error}` ⇒ `verified_at = NULL, verified_by = NULL`.
4. Tag delete-and-reinsert stays inside the **same transaction**.
5. **Required signature change** to `UpdateByExpert`. The current loose `[]string` params are no longer sufficient: `status` is no longer hard-coded to `'verified'` inside the repo (see item 3) — it must now be **carried by the caller**. Define a new value type in `internal/domain`:

   ```go
   type QuestionUpdate struct {
       Status      string
       Choices     []string
       Answers     []string
       Explanation string
       Tags        []string
       Confidence  float64
   }
   ```

   New signature (interface at `internal/storage/ports.go:73`):

   ```go
   UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error
   ```

   The handler builds `domain.QuestionUpdate` from the validated `UpdateQuestionRequest` DTO and passes `expertID = ctx.user_id`. `expertID` populates `verified_by` when `status == "verified"` (§3.2.4).

---

### 3.3 Section 3 — Derive `MultipleCorrect` (remove stored redundancy)

**Confirmed:** the frontend does **not** use `multiple_correct` for any rendering or logic — it derives "has multiple answers" from the answer count. The stored field is pure redundancy and is removed.

> **Rationale note:** the semantic distinction between "the *type* allows multiple" and "the *count* has multiple correct" is real in the abstract but **irrelevant here** — nobody consumes the type signal. If a future frontend needs the type, it can be re-added as an explicit field (§9).

#### 3.3.1 Changes per layer

**Domain** (`internal/domain/question.go:24-42`):
- **Delete** the `MultipleCorrect bool` field.
- **Add** a method:
  ```go
  // MultipleCorrect reports whether the question has more than one correct answer.
  func (q Question) MultipleCorrect() bool { return len(q.Answers) > 1 }
  ```
  - Name kept to match existing vocabulary. `IsMultipleCorrect()` / `HasMultipleCorrect()` are equally valid; defer to the `golang-naming` skill at implementation time.

**Database** (new migration — next number after the current highest in `internal/storage/postgres/migrations/`; at time of writing the highest is `0003`, so the new file is `0004_*.sql`):
```sql
ALTER TABLE questions DROP COLUMN IF EXISTS multiple_correct;
```
Destructive but **lossless** (the data is fully reproducible from `len(answers) > 1`).

**Storage** (`internal/storage/postgres/question_repo.go`) — ~10 mechanical edits:
- Remove `multiple_correct` from every `INSERT` column + value list (~`:38,43`).
- Remove from `SELECT` column list (~`:123,166,261`).
- Remove from **both** `*SelectBase` constants (~`:445,458`).
- Remove from every `scan*` target `&q.MultipleCorrect` (~`:476,495,522,547`).

**Pipeline**:
- Drop `MultipleCorrect` from `ExtractedQuestion` (`internal/pipeline/ports.go:45`) and from `domain.Question` construction (`internal/pipeline/pipeline.go:180`).

**AI schemas** (Go JSON unmarshal ignores stale model emissions — non-breaking):
- Extractor: remove `MultipleCorrect` from `questionDTO` (`internal/ai/extractor/schema.go:22`) and `toPipeline` (~`:82`).
- Verifier: remove from **both** `questionDTO` types and from `fromPipeline` (`internal/ai/verifier/schema.go:19,36,67`).
- Drop the instruction from the skill prompts under `skills/extract-questions-from-image/` and the verifier skill (these are gitignored markdown; `git add -f` if tracking is desired).

**HTTP**:
- **Response DTOs KEEP** the field — the frontend still receives `multiple_correct` — but it is **populated via `q.MultipleCorrect()` at build time**. Call sites `handlers/questions.go:299,313` switch from field-read to method-call.
- **Request DTOs DROP** it: remove `CreateQuestionRequest.MultipleCorrect` (`dto/requests.go:16`); the manual-create handler (`handlers/questions.go:264`) stops setting it.

#### 3.3.2 Net effect

One canonical definition (`len(answers) > 1`), in one location (a domain method), backed by no column. The frontend JSON contract is unchanged — `multiple_correct` is still emitted, now derived.

---

## 4. API Contract Changes (Before / After)

### 4.1 `GET /api/v1/questions`

| Aspect | Before | After |
|---|---|---|
| Scoping driver | Role | `session_id` query param |
| Expert default `status` | Forced `"moderation"` | None (absent ⇒ all statuses) |
| User default `status` | None | None (absent ⇒ all statuses) |
| User, `session_id` omitted | **400** "missing session_id" | **403 Forbidden** |
| User reads another user's session | **200 (bug)** | **403 Forbidden** |
| `status` invalid value | (expert path) ignored/defaulted | **400** for both roles |
| Response shape per role | Divergent paths | Same SQL when session-scoped; DTO mapper picks shape |

### 4.2 `PUT /api/v1/questions/:id` (was `PATCH`)

| Aspect | Before (`PATCH`) | After (`PUT`) |
|---|---|---|
| Method | `PATCH` | `PUT` |
| Semantics | Partial (but applied unconditionally) | Full-replace of editable fields |
| `{"status":"verified"}` alone | Silently nulls `choices`/`answers` | **400** "choices required"/"answers required" |
| `status` honored | No (forced `verified`) | Yes (all three; see §3.2.4) |
| `answers ⊆ choices` | Not checked | **400** if violated |
| `confidence` omitted | defaulted `1.0` | defaulted `1.0` (unchanged) |
| `verified_at`/`verified_by` | always set | set iff `status=verified`; else `NULL` |

**Route registration:** `internal/httpapi/server.go:~105` — `questions.PATCH("/:id", …)` → `questions.PUT("/:id", …)` (the concrete edit backing the method column above).

### 4.3 Response shape — `multiple_correct`

Unchanged on the wire (still present, boolean). Semantics shift from "stored type flag" to "derived `len(answers) > 1`".

### 4.4 Status codes summary

- `200` — successful list / successful update.
- `400` — invalid `status`/`tag` value; binding failure (choices/answers empty or containing empty strings); struct-level `answers ⊈ choices`; `confidence` out of `[0,1]`; too many tags / empty tag strings.
- `403` — user without `session_id`; user reading a session they don't own.
- `404` — question id not found (PUT) / session not found.

---

## 5. Database Migration

**New file:** `internal/storage/postgres/migrations/0004_drop_multiple_correct.sql`
(name follows the current highest `0003_manual_tag.sql`; confirm the next free number at implementation time).

```sql
-- 0004_drop_multiple_correct.sql
-- multiple_correct is fully derivable from len(answers) > 1.
-- Drop the redundant column; the value is recomputed in the domain layer.
ALTER TABLE questions DROP COLUMN IF EXISTS multiple_correct;
```

This migration runs automatically on boot via `postgres.RunMigrations` (called from `app.Build`). There is **no separate `migrate` step** (per `AGENTS.md`).

No other schema changes are required for Sections 1 and 2 — those reuse existing columns (`session_questions.image_id`, `extracted_number`, `extracted_confidence`; `questions.verified_at`, `verified_by`, `status`).

---

## 6. Security Consideration — Ownership-Gap Fix

**The bug:** `ListForUser` (`question_repo.go:121-157`) filters only `WHERE sq.session_id = $1`. An authenticated user who guesses or enumerates another user's `session_id` can read that session's questions in full. This is an IDOR-style authorization bypass.

**The fix:** enforce at the handler that, for the **user** role, the resolved session's `user_id` equals `ctx.user_id` **before** any repo read occurs; otherwise return **403**. Experts are exempt (they may inspect any session for moderation). The single-item path `FindForUserByID` already does the equivalent (`s.user_id = $2`); this brings the list path to parity.

**Verification:** add a test where user A requests user B's `session_id` and asserts `403` and zero repo reads of B's data (§7).

---

## 7. Testing Strategy

Per `AGENTS.md`: unit tests run with `go test -short ./...`; integration tests need Docker (Testcontainers + `pgvector/pgvector:pg16`), self-skip under `-short`, use **no build tags**, and use `setupTestDB(t)` in `internal/storage/postgres/testhelpers_test.go`.

### 7.1 Unit tests — validation (Section 2)

- `status` not in `{moderation, verified, error}` ⇒ 400.
- `choices` empty / containing empty string ⇒ 400.
- `answers` empty / containing empty string ⇒ 400.
- **`answers ⊄ choices`** (struct-level rule) ⇒ 400. Cover: an answer value absent from choices; duplicates. Matching is **exact, case-sensitive** (`==`) — assert e.g. that `"a"` does not match choices `["A"]`.
- `confidence` present and outside `[0,1]` ⇒ 400.
- `tags` count > 20 or containing empty string ⇒ 400.
- `confidence` absent ⇒ defaults to `1.0`.
- Valid full payload ⇒ 200, row updated exactly as sent.

### 7.2 Unit tests — listing scoping (Section 1)

Table-driven across role × `session_id` × `status`:
- user + own session + no status ⇒ 200, only that session's questions, all statuses.
- user + own session + `status=verified` ⇒ 200, only verified.
- user + **other user's** session ⇒ **403**, no repo read of other user's data.
- user + no `session_id` ⇒ **403** (was 400).
- expert + any session ⇒ 200.
- expert + no `session_id` + no `status` ⇒ 200, all statuses (was forced to `moderation`).
- expert + no `session_id` + `status=moderation` ⇒ 200, moderation queue.
- invalid `status` value ⇒ 400 for both roles.
- both roles share the **same** session-scoped SQL when `session_id` is present (assert via the unified `ListForSession` call, not a duplicated path).

### 7.3 Unit tests — `MultipleCorrect` derivation (Section 3)

- `Question{Answers: []string{"A"}}.MultipleCorrect()` ⇒ `false`.
- `Question{Answers: []string{"A","B"}}.MultipleCorrect()` ⇒ `true`.
- Response DTO build sets `multiple_correct` from the method, not a stored field.

### 7.4 Integration tests (Docker / Testcontainers)

Using `setupTestDB(t)` + pgvector:
- Seed two users, two sessions (one each), questions linked via `session_questions`.
- Ownership: user A `GET ?session_id=B` ⇒ 403; user A `GET ?session_id=A` ⇒ 200 with A's questions only.
- Unified scoping: expert `GET ?session_id=A` ⇒ same rows as user A's own request (shape differs by DTO).
- Global queue: expert `GET` (no session) ⇒ questions across both sessions; with `status` filter applied conditionally.
- PUT full-replace: send full payload, assert `choices`/`answers`/`tags`/`status`/`verified_at`/`verified_by` match §3.2.4 semantics; send incomplete payload ⇒ 400 and assert the row is **unchanged** (read-back compare).
- `answers ⊄ choices` ⇒ 400 and row unchanged.
- Migration `0004`: after boot, `information_schema` confirms `multiple_correct` column absent; listing still returns derived `multiple_correct`.

---

## 8. Out of Scope / Future

- **Question-text editability.** Editing `text` requires recomputing `text_hash` and `text_norm`, regenerating the `embedding`, and re-running dedup (which may collapse the edited question into a different canonical row). Deliberately deferred.
- **`choice_labeling` / AI-derived metadata editability.** Not in this iteration.
- **Re-adding an "allows multiple" question-type signal.** If a future frontend needs to distinguish "the question type permits multiple selection" from "this instance has >1 correct answer", re-introduce it as an explicit, separately-named field — not by restoring the overloaded `multiple_correct` column.
- **Pagination / cursor changes.** `parsePaging` is unchanged.
- **Auth/role plumbing.** `RoleGuard` and JWT middleware are unchanged.

---

## 9. Decisions Resolved

1. **`multiple_correct` removal** — confirmed against verified frontend non-usage; the stored field is pure redundancy. Removed at DB/domain/storage/pipeline/schema layers; response DTO keeps the wire field, now derived via `len(answers) > 1`.
2. **`PUT` full-replace over `PATCH` partial** — chosen so backend validation can guarantee completeness; under full-replace, incomplete payloads are rejected rather than silently overwriting stored values with nulls.
3. **Unified session-scoped read over nested routes** — one `session_id`-driven read path (`ListForSession`) serves both roles when a session is in scope; the global expert queue remains a separate query. This replaces role-coupled branching and makes the `status` default uniform (absent ⇒ all).
4. **403 over 400 for user-without-session** — recognized as an authorization boundary, not a malformed request.
5. **Ownership check at the handler** (resolve session ⇒ compare `user_id`) rather than in SQL, to match the existing single-item pattern and keep the repo read simple; the repo filters by `session_id` only.
6. **`answers ⊆ choices` as a struct-level rule** (not a binding tag) — Gin/go-playground `dive` tags validate element shape, not cross-field membership; enforced post-binding in the handler/service.
7. **`answers ⊆ choices` matching semantics** — exact, case-sensitive Go string equality (`==`); no normalization, no case-folding. (Resolves the previously open "decide and document".)
8. **`UpdateByExpert` signature change is required, not cosmetic** — `status` is no longer hard-coded to `'verified'` in the repo, so it must be carried by the caller via a new `domain.QuestionUpdate` value type plus an `expertID` arg. (Corrects the earlier "cosmetic; recommended" framing.)
9. **Exactly two listing repo methods** — `ListForSession` (session-scoped, both roles) and `ListForModerationExpert` (global, expert). `ListForUser` and `ListForModeration` are deleted; `ListForModeration` is dead code with zero production call sites (only `ListForModerationExpert` is wired to a handler).
10. **Reuse `storage.QuestionWithSession`** (no new `SessionQuestion` struct) as the return type of `ListForSession`; its SELECT (`question_repo.go:123`) and `scanQuestionWithSession` (`:539-556`) must be expanded to read all question columns.
11. **Disentangle 404 from 403 on the list path** — session-missing ⇒ 404, not-owner ⇒ 403. The current `internal/httpapi/handlers/questions.go:81-84` conflates them; the handler must split the lookup-error path from the ownership-mismatch path rather than flipping a single code to 403.
