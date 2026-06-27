# Coeus — Manual Questions & CORS

**Status:** Design approved, pending implementation plan
**Date:** 2026-06-27
**Tech stack:** Go + Gin, Postgres + PGX, pgvector

---

## 1. Overview

This is a focused addendum to the [Image Question Analysis Service design](./2026-06-18-coeus-image-question-analysis-service-design.md). It documents two additions to the existing service, neither of which changes the async image pipeline:

1. **Manual question creation** — a new `POST /api/v1/questions` endpoint that lets an expert hand-author a canonical question in the knowledge base, bypassing image extraction. The resulting row is `status='verified'` on creation and is immediately usable for future dedup.
2. **CORS configuration** — cross-origin support so browser frontends can call the API. There is currently no CORS code in the repo and `gin-contrib/cors` is not a dependency.

Auth model, roles, the pipeline, data model, and uniform error shape are unchanged and are not restated here; see sections 2, 4, 5, 6, and 8 of the original spec for context. All file paths and type names below assume the project layout from section 6 of the original.

### Goals

- Let experts seed and correct the knowledge base directly (e.g. fill a known question that the vision model keeps misreading, without going through an upload).
- Make the API callable from a browser-based frontend.

### Non-goals

- Semantic dedup on the manual path (exact-hash only).
- A second-review/approval step for manually created questions (the creating expert is trusted).
- Linking a manual question to a session or image on creation (manual entries are free-standing; linking error placeholders remains a `PATCH /questions/:id` concern).
- A `source` column or other schema migration to record provenance (a tag convention is used instead).

---

## 2. Key Decisions (from brainstorming)

| # | Decision | Choice | Rationale |
|---|---|---|---|
| M1 | Status on manual creation | **`verified`**, with `verified_at=now`, `verified_by=creator` | The creating expert is trusted; no second review needed; immediately dedup-usable |
| M2 | Provenance | **`manual-entry` tag injected**; no `ai-generated`; **no `source` column** | System invariant via tag convention; avoids a schema migration |
| M3 | Dedup on manual path | **Exact-hash only**, returning `409` with the existing `question_id` | Manual entry is deliberate; the expert can `PATCH` the existing row instead of re-creating |
| M4 | Embedding | **Best-effort**: compute if embedder configured, else `nil`; never fail the request | Embedding is for *future* pipeline semantic dedup, not for dedup-ing this request |
| M5 | Request shape | **`question`, `choices`, `answers` required; `number` defaulted to 0; no `session_id`/`image_id`** | Manual questions are free-standing canonical entries, not tied to a session |
| C1 | CORS library | **`github.com/gin-contrib/cors`** | Battle-tested; handles preflight, credentials, `Vary` correctly |
| C2 | Config surface | **`ServerConfig.CORS` sub-struct**, YAML defaults + env overrides | Matches the existing config pattern (section 5.4 of the original) |
| C3 | Middleware order | **After `Recover`/`RequestLog`, before `AuthMiddleware`** | OPTIONS preflight returns `204`, not `401` |

Additional refinements:

- **Error shape extension (duplicate-`409` only):** the handler returns the `409` **inline** via `c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"code":"duplicate","message":"question already exists","question_id": existing.ID}})`. The shared `errorResponse` / `domain.Error` infrastructure is **not** modified — `domain.Error` stays a two-field struct, and all other error responses keep the uniform `{"error":{"code","message"}}` shape. The `question_id` field appears only on this one response.
- **Validation catch:** `*` + `allow_credentials:true` is rejected at startup (browsers reject the combination); better to fail boot than debug silent CORS errors.

---

## 3. Feature 1 — Manual Question Creation

### 3.1 Endpoint

```
POST /api/v1/questions      (expert role only; non-expert → 403 via RoleGuard)
```

Request body:

```json
{
  "question": "...",                  // required
  "choices": ["...","..."],          // required, >=2
  "answers": ["..."],                // required, >=1, value-only (shuffle-safe)
  "multiple_correct": false,         // optional, default false
  "choice_labeling": "letter",       // optional, "letter"|"number", default "letter"
  "explanation": "",                 // optional, default ""
  "tags": ["chemistry"],             // optional, subject tags; service injects "manual-entry"
  "confidence": 0.99                 // optional, *float64, nil → default 0.99
}
```

- `number` is **omitted from the request** and defaulted to `0` in the DB; it is only meaningful with a source image.
- No `session_id` / `image_id`: manual questions are free-standing canonical KB entries. Linking an error placeholder to a real session/image remains a `PATCH /questions/:id` concern.

Response: `201` with the full `ExpertQuestionResponse` (same shape as `GET /questions/:id` for experts, section 4.6 of the original).

### 3.2 Handler flow

`Create` method added to the existing `QuestionHandler` in `internal/httpapi/handlers/questions.go`:

1. Bind JSON (binding tags validate required/min) → `400` on failure.
2. Validate `choice_labeling ∈ {"letter","number",""}` → `400` on invalid. If `req.Confidence != nil`, validate `0 ≤ *req.Confidence ≤ 1` → `400` on out-of-range (the `numeric(3,2)` column would otherwise reject it with a cryptic `500`).
3. `norm = domain.NormalizeQuestion(req.Question); hash = domain.HashQuestion(norm)`.
4. Exact-dedup: `questionRepo.FindExact(ctx, hash)`. On hit, return `409` **inline** (see §2 refinement): `c.AbortWithStatusJSON(409, gin.H{"error": gin.H{"code":"duplicate","message":"question already exists","question_id": existing.ID}})`.
5. Best-effort embedding (only if `embedder != nil`):
   ```go
   emb, embErr := h.embedder.Embed(ctx, req.Question)
   if embErr != nil {
       slog.Error("manual question embedder failed", "request_id", reqID, "error", embErr)
       emb = nil
   }
   ```
   When the embedder is unconfigured (`nil`), skip the call entirely. **The question is always created**, with or without an embedding.
6. Assemble `domain.Question`:
   - `Number: 0`, `Text: req.Question`, `TextNorm: norm`, `TextHash: hash`
   - `MultipleCorrect`, `Choices`, `Answers` from the request
   - `ChoiceLabeling`: resolved (`"letter"` default)
   - `Explanation: req.Explanation`
   - `Confidence: *req.Confidence` or `0.99`
   - `Embedding: emb` (may be `nil`)
   - `Status: "verified"`
   - `VerifiedAt: pointerTo(time.Now().UTC().Format(time.RFC3339))` — RFC 3339 / ISO 8601 UTC, matching how `scanQuestionExpert` reads it back
   - `VerifiedBy: pointerTo(expertID)` — the caller's user ID from `c.GetString("user_id")`
   - `Tags: req.Tags + ["manual-entry"]`
7. `questionRepo.Create(ctx, q)` → id. **`Create` must persist `verified_at` and `verified_by`** (see §3.6a).
8. Re-fetch the full question (`FindExpertByID`) for the response. **Note:** `image_id` will be `""` (empty string) for manual questions — there is no `session_questions` row, so the correlated subquery in `questionExpertSelectBase` returns NULL, which `scanQuestionExpert` maps to `""`. This is expected and documented for frontends (see §3.8).
9. `201` → `ExpertQuestionResponse` JSON.

### 3.3 Domain extraction (pure refactor)

`normalizeQuestion` and `sha256String` currently live unexported in the `pipeline` package. Move them into `domain` as exported functions so the HTTP layer can reach them:

```go
// internal/domain/question.go
func NormalizeQuestion(s string) string {
    return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}
func HashQuestion(norm string) string {
    h := sha256.Sum256([]byte(norm))
    return hex.EncodeToString(h[:])
}
```

The pipeline (`internal/pipeline/pipeline.go`) switches to call `domain.NormalizeQuestion` / `domain.HashQuestion` and **deletes its local copies**. The functions are byte-for-byte identical, so existing DB hashes are unaffected. **Do not add punctuation stripping** — that would invalidate existing hashes and break dedup against already-stored rows.

### 3.4 Tag seeding

New migration `internal/storage/postgres/migrations/0003_manual_tag.sql`:

```sql
INSERT INTO tags (name) VALUES ('manual-entry') ON CONFLICT DO NOTHING;
```

Auto-applied on boot alongside the existing migrations (section 10 of the original). Not strictly required — `linkTag` upserts by name — but keeps the `tags` table tidy and makes the manual-entry tag visible to the expert tag filter.

### 3.5 Request DTO

```go
// internal/httpapi/dto/requests.go
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

### 3.6 Wiring changes

**`QuestionHandler` constructor** gains the embedder:

```go
// before
NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo)
// after
NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo, embedder pipeline.AIEmbedder)
```

The embedder is the existing `pipeline.AIEmbedder` interface (section 5.3 of the original), passed nil-safe. When `nil` (embedder unconfigured, i.e. `COEUS_AI_EMBEDDER_API_KEY` unset), step 5 of the handler flow is skipped.

**`NewServer` signature** gains an `embedder pipeline.AIEmbedder` parameter; stored on the `Server` struct; passed to `NewQuestionHandler` inside `registerRoutes`.

### 3.6a `QuestionRepo.Create` extension (required for `verified` on creation)

The current `Create` INSERT (`internal/storage/postgres/question_repo.go`) omits `verified_at` and `verified_by`, so the handler's `Status:"verified"` / `VerifiedAt` / `VerifiedBy` would never be persisted. Extend the INSERT to include both columns, reading them from the `domain.Question`:

```go
// pseudo-diff of the INSERT in Create():
//   ..., status, verified_at, verified_by) VALUES (..., $status,
//   $verifiedAt /* may be NULL */, $verifiedBy /* may be NULL */)
//   RETURNING id
```

Both columns are nullable in the schema, and the existing pipeline caller passes `VerifiedAt: nil`, `VerifiedBy: nil` — so the change is **backward-compatible**: existing callers write `NULL`, the new manual caller writes real values. No new repo method is needed; the `QuestionRepo` interface is unchanged. The `domain.Question` struct already has `VerifiedAt *string` and `VerifiedBy *string` fields.

**Route registration** (`internal/httpapi/server.go`), alongside the existing `PATCH`:

```go
questions.POST("", RoleGuard("expert"), questionHandler.Create)
```

### 3.7 Error handling

| Condition | Result |
|---|---|
| Bind / validation failure | `400` validation |
| Invalid `choice_labeling` | `400` validation |
| Exact-hash duplicate | `409 {"error":{"code":"duplicate","message":"question already exists","question_id":"<uuid>"}}` |
| Embedder failure / unconfigured | Swallowed, logged via `slog` with request id; question created without embedding |
| `questionRepo.Create` failure | `500` via the existing domain-error-to-HTTP switch (section 8 of the original) |
| Non-expert role | `403` from `RoleGuard`, before the handler runs |

### 3.8 Invariants

- `manual-entry` tag **always** injected on the manual path.
- `ai-generated` **never** injected on the manual path.
- `status='verified'` on creation; no moderation step.
- `number=0`; no `session_questions` link created.
- Embedding is **best-effort only**; a missing embedding never fails the request.
- **`image_id` is `""` (empty string)** in the response for manual questions — there is no source image. Frontends must treat an empty `image_id` as "no image" and not attempt `GET /api/v1/images/`. (Changing `ExpertQuestionResponse.ImageID` to `*string` with `omitempty` would alter the existing API contract for pipeline questions, so the field stays `string` and is simply empty for manual entries.)

---

## 4. Feature 2 — CORS Configuration

### 4.1 Dependency

```bash
go get github.com/gin-contrib/cors
```

### 4.2 Config

```go
// internal/config/config.go
type ServerConfig struct {
    Addr            string        `yaml:"addr"`
    ReadTimeout     time.Duration `yaml:"read_timeout"`
    WriteTimeout    time.Duration `yaml:"write_timeout"`
    ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
    CORS            CORSConfig    `yaml:"cors"`
}

type CORSConfig struct {
    AllowedOrigins   []string      `yaml:"allowed_origins"`
    AllowedMethods   []string      `yaml:"allowed_methods"`
    AllowedHeaders   []string      `yaml:"allowed_headers"`
    ExposeHeaders    []string      `yaml:"expose_headers"`
    AllowCredentials bool          `yaml:"allow_credentials"`
    MaxAge           time.Duration `yaml:"max_age"`
}
```

Defaults in `internal/config/config.yaml`:

```yaml
server:
  cors:
    allowed_origins: ["*"]
    allowed_methods: ["GET", "POST", "PATCH", "DELETE", "OPTIONS"]
    allowed_headers: ["Authorization", "Content-Type", "X-Request-Id"]
    expose_headers: ["X-Request-Id"]
    allow_credentials: false
    max_age: 12h
```

### 4.3 Env overrides (`applyEnvOverrides`)

| Env var | Maps to | Notes |
|---|---|---|
| `COEUS_CORS_ALLOWED_ORIGINS` | `AllowedOrigins` | Comma-separated |
| `COEUS_CORS_ALLOW_CREDENTIALS` | `AllowCredentials` | `"true"` / `"1"` → true |

Only `allowed_origins` and `allow_credentials` get env overrides — these are the deployment-specific values (origins differ per environment; credentials flip for credentialed auth). `allowed_methods`, `allowed_headers`, `expose_headers`, and `max_age` are stable enough to live in `config.yaml` and don't need env vars. This matches the existing pattern (not every config field gets an env override; see section 5.4 of the original).

### 4.4 Startup validation

In `config.Validate()`: if `AllowCredentials == true` **and** `AllowedOrigins` contains `"*"`, return an error and exit. The `*` wildcard is invalid with credentials (browsers reject the combination); catching this at boot beats debugging silent CORS failures.

### 4.5 Middleware wiring

`NewServer` gains a `corsCfg config.CORSConfig` parameter. The CORS middleware is inserted **after** `Recover`/`RequestLog` and **before** `registerRoutes` (which mounts `AuthMiddleware`), so an OPTIONS preflight returns `204` with CORS headers rather than `401`:

```go
// internal/httpapi/server.go, NewServer
r.Use(Recover(), RequestLog())
corsConfig := cors.Config{
    AllowOrigins:     corsCfg.AllowedOrigins,
    AllowMethods:     corsCfg.AllowedMethods,
    AllowHeaders:     corsCfg.AllowedHeaders,
    ExposeHeaders:    corsCfg.ExposeHeaders,
    AllowCredentials: corsCfg.AllowCredentials,
    MaxAge:           corsCfg.MaxAge,
}
r.Use(cors.New(corsConfig))
s.registerRoutes()
```

`gin-contrib/cors` auto-handles OPTIONS preflight (aborts with `204` + headers). Health endpoints (`/healthz`, `/readyz`) receive CORS headers too — harmless and correct.

---

## 5. Files Changed

### Feature 1 — Manual Question Creation

**Created:**

- `internal/storage/postgres/migrations/0003_manual_tag.sql` — seeds the `manual-entry` tag. (If `0003` is taken by the time implementation starts, use the next available number.)

**Modified:**

- `internal/domain/question.go` — add exported `NormalizeQuestion`, `HashQuestion`.
- `internal/pipeline/pipeline.go` — switch to `domain.NormalizeQuestion` / `domain.HashQuestion`; delete local copies.
- `internal/storage/postgres/question_repo.go` — extend `Create`'s INSERT to persist `verified_at` / `verified_by` (§3.6a). The `QuestionRepo` interface in `internal/storage/ports.go` is **unchanged**.
- `internal/httpapi/dto/requests.go` — add `CreateQuestionRequest`.
- `internal/httpapi/handlers/questions.go` — add `Create` method; constructor gains `embedder`.
- `internal/httpapi/server.go` — `NewServer` gains `embedder` and `corsCfg`; `questions.POST("", ...)` route.
- `internal/app/wire.go` — pass the embedder and CORS config through to `NewServer` / `NewQuestionHandler`.

### Feature 2 — CORS Configuration

**Modified:**

- `go.mod` / `go.sum` — add `github.com/gin-contrib/cors`.
- `internal/config/config.go` — add `CORSConfig` struct, `ServerConfig.CORS` field, env overrides, validation.
- `internal/config/config.yaml` — add `server.cors` defaults.
- `internal/httpapi/server.go` — `NewServer` gains `corsCfg`; mount the CORS middleware in the right position.

---

## 6. Testing Strategy

| Area | Approach |
|---|---|
| `domain.NormalizeQuestion` / `HashQuestion` | Unit: idempotency, case/whitespace folding, determinism against known vectors. Verify pipeline behavior unchanged (refactor is behavior-preserving). |
| `QuestionHandler.Create` | Handler unit tests with a fake `QuestionRepo` + fake `AIEmbedder`: happy path → `201` + `verified`; duplicate hash → `409` with `question_id`; embedder failure → still `201` with no embedding; non-expert → `403`; validation → `400`. Assert `manual-entry` injected and `ai-generated` absent. |
| CORS config validation | Unit: `"*"` + `allow_credentials:true` → validation error. |
| CORS middleware | `httptest`: preflight `OPTIONS /api/v1/questions` → `204` with `Access-Control-Allow-*` headers, **not** `401`; `GET /healthz` with an `Origin` header echoes the allowed origin. |
| Existing tests | Must pass unchanged (pipeline refactor is behavior-preserving). |

No integration/DB test for the `Create` happy path beyond confirming `Create` works — `QuestionRepo.Create` is already covered by existing storage tests.

---

## 7. Open / Deferred Items

- A bulk-import endpoint for manual questions (single-question `POST` only in this iteration).
- Tag normalization/dedup on input (passed through as-is for now).
- Fine-grained CORS per-route (a single global policy is sufficient for v1).
- Surfacing embedder failures beyond `slog` (no metric/alert in v1).
