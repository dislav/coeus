# Question Import via CSV/Excel Upload — Design Spec

- **Date:** 2026-07-19
- **Status:** Approved
- **Scope:** New endpoint `POST /api/v1/questions/upload` that imports exam questions from a CSV or `.xlsx` file synchronously into the canonical `questions` table.

## 1. Summary

Experts and admins upload a multipart CSV or `.xlsx` file of exam questions. The server parses the file, validates every row, computes exact-hash dedup keys, fetches embeddings in batches (best effort), and upserts each valid row into `questions` as `verified`. Duplicate rows (same normalized question text) update the existing record in place — the file wins. The response is a per-row import report: created / updated / failed counts plus row-level error messages.

Processing is **synchronous**: one request parses, validates, embeds, upserts, and returns a single report. No job queue, no async workers.

## 2. Goals / Non-Goals

### Goals

- Bulk-import questions from CSV or `.xlsx` with a fixed, headerless 5-column layout.
- Reuse existing validation rules, dedup hashing, tag handling, upload size limits, and error envelope.
- Update-in-place dedup: re-importing a question updates content fields without changing its ID or breaking session/image linkage.
- Per-row fault isolation: bad rows never block good rows; the report tells the uploader exactly which rows failed and why.
- Best-effort batched embeddings so imported rows participate in semantic dedup later.

### Non-Goals (YAGNI)

- No semantic dedup during import (exact hash only).
- No async / job-queue processing. Revisit only if files routinely exceed ~20k rows.
- No legacy `.xls` support (explicitly rejected with a clear message).
- No choice-label parsing (`A)`, `1)`, …). The format is value-only.
- No header-row mapping; column order is fixed.
- No multi-sheet support; first sheet only.
- No import history persistence.

## 3. Background / Current State

References an implementer should read first:

| Area | File | Notes |
|---|---|---|
| Manual wiring | `internal/app/wire.go` | Builds repos, embedder (`var emb pipeline.AIEmbedder`, set only when `cfg.AI.Embedder.APIKey != ""`), `httpapi.NewServer(...)`. |
| Upload handler pattern | `internal/httpapi/handlers/images.go` (lines 36–47) | `http.MaxBytesReader` → `c.Request.FormFile("image")` → read. |
| Routing | `internal/httpapi/server.go` (lines 83, 113–119) | `apiGroup.Use(AuthMiddleware(...))`; `questions := apiGroup.Group("/questions")` with per-route `RoleGuard("expert", "admin")` on POST/PUT/DELETE. |
| Create/update question validation | `internal/httpapi/handlers/questions.go` (`Create` at line 290; `Update` at line 169 — identical type-conditional switch blocks at 304–319 and 181–196; `answersSubsetOfChoices` at line 257; `manual-entry` tag at lines 368–371; `Update`-only tag checks at lines 207–216) | Rules the importer mirrors and partially refactors into `domain.ValidateDraft`. |
| Domain helpers | `internal/domain/question.go` | `InferQuestionType` (line 87), `NormalizeQuestion` (line 98), `HashQuestion` (line 104 — func signature), `Question` struct (45), constants `QuestionStatusVerified`, `ChoiceLabelingLetter`, `QuestionTypeMultipleChoice`, `QuestionTypeFreeResponse`. |
| Domain errors | `internal/domain/errors.go` | `NewError(code, msg) *Error` (line 29); `question_in_use` precedent (line 45). |
| Error envelope | `internal/httpapi/handlers/common.go` | `errorResponse(err)` → `{"error":{"code","message"}}` via `errors.As` on `*domain.Error`. |
| Question repo | `internal/storage/ports.go` (`QuestionRepo`, line 85); impl `internal/storage/postgres/question_repo.go` | `linkTagTx` (416), `cleanupImageBytesTx` (326, called by `UpdateByExpert` at 306), `Delete` guard `question_in_use` (390). |
| Hash uniqueness | `internal/storage/postgres/migrations/0002_core.sql` (line 31) | `question_hash text NOT NULL UNIQUE` — the upsert conflict target. |
| Embedder | `internal/pipeline/ports.go` (`AIEmbedder`, line 31); `internal/ai/embedder/embedder.go` (`Embed`, line 46); `internal/ai/embedder/input.go` (`StringInput`) | openai-go client; dim-checked (1536) responses. |
| Config | `internal/config/config.go` (`UploadConfig.MaxBytes` line 104; `applyEnvOverrides` line 121); `internal/config/config.yaml` | `upload.max_bytes: 10485760` (10 MB); `server.write_timeout: 120s`. |
| DTOs | `internal/httpapi/dto/` (`responses.go`, `requests.go`, `question.go`) | Home of the new report DTOs. |

## 4. API Contract

### 4.1 Endpoint

```
POST /api/v1/questions/upload
Content-Type: multipart/form-data
```

- **Middleware:** existing `AuthMiddleware` (applied group-wide in `server.go`) + per-route `RoleGuard("expert", "admin")`.
- **Multipart field name:** `file`.
- **Routing:** registered as `questions.POST("/upload", ...)`. Gin keeps per-method radix trees, so `POST /questions/upload` does not conflict with `POST /questions` (`""`) or `GET /questions/:id`.

### 4.2 Success response — 200 OK

Returned whenever the file itself parses — even if every row failed validation:

```json
{
  "total_rows": 150,
  "created": 120,
  "updated": 25,
  "failed": 5,
  "errors": [
    { "row": 17, "message": "multiple_choice requires at least 2 choices" }
  ]
}
```

Contract details:

- `total_rows = created + updated + failed`, always.
- With in-file duplicates, each **occurrence** counts in `total_rows`: a question appearing twice in one file is counted twice (first occurrence `created`, second `updated`). The arithmetic `total_rows = created + updated + failed` still holds.
- `row` is the **1-based** file row number (no header row exists, so file row = data row).
- `errors` carries at most the first **100** row errors (`maxImportRowErrors = 100`); `failed` always reports the true count.
- DTOs: `ImportReportResponse` and `ImportRowError` in `internal/httpapi/dto`.

### 4.3 Failure responses — 400 Bad Request

Only for **file-level** failures, in the existing envelope `{"error":{"code","message"}}` produced by `errorResponse` / the `domain.ErrValidation` mapping style:

| Condition | Example message |
|---|---|
| Unsupported format (sniffed) | `unsupported file format` |
| Legacy `.xls` (`application/x-ole-storage`) | `legacy .xls not supported — save as .xlsx` |
| Empty file | `empty file` |
| Malformed CSV/XLSX (parser-fatal) | parser error surfaced as message |
| Row count > `import.max_rows` | `too many rows` |
| Body over `upload.max_bytes` | existing size-limit handling (mirrors `images.go` Upload) |

### 4.4 Idempotency

Re-uploading the same file yields all-`updated` (hashes already exist). Retry after a partial failure = re-upload the whole file; already-imported rows report `updated`, failed rows get another attempt.

## 5. File Format

### 5.1 Format detection

By **content sniffing** (`http.DetectContentType` on the read bytes), never by extension:

| Sniffed type | Handling |
|---|---|
| `text/*` | CSV, stdlib `encoding/csv` |
| `application/zip` | XLSX via `github.com/xuri/excelize/v2` |
| `application/x-ole-storage` | Reject: `legacy .xls not supported — save as .xlsx` |
| anything else | Reject: unsupported format |

Non-UTF-8 CSV sniffs as `application/octet-stream` and is therefore rejected as unsupported — no separate encoding detection.

Accepted risk: detection is content-only, so any `text/*` payload (e.g. an HTML page uploaded as `.csv`) is treated as CSV and surfaces as **row-level** validation errors rather than a file-level rejection.

### 5.2 CSV parsing

- Stdlib `encoding/csv` with a streaming reader.
- `FieldsPerRecord = -1` (variable column counts handled at the row level, see below).
- `TrimLeadingSpace = true`.
- UTF-8 only (consequence of sniffing, above).

### 5.3 XLSX parsing

- Library: `github.com/xuri/excelize/v2`, **pinned at v2.11.0** (fixes CVE-2026-54063; pure Go, no CGO — does not disturb the existing CGO/libvips build).
- Open with `excelize.OpenReader` and explicit limits — the library defaults (16 GB / 16 MB) are unsafe against a 10 MB input cap:
  ```go
  excelize.Options{UnzipSizeLimit: 100 << 20, UnzipXMLSizeLimit: 64 << 20}
  ```
- **First sheet only:** `GetSheetList()[0]`.
- Stream rows via the `Rows()` iterator.

### 5.4 Row layout

Fixed column order, **no header row**, exactly 5 logical columns:

| # | Column | Multi-value? |
|---|---|---|
| 1 | `question` | no |
| 2 | `choices` | yes — `;`-separated |
| 3 | `answers` | yes — `;`-separated |
| 4 | `explanation` | no |
| 5 | `tags` | yes — `;`-separated |

- Multi-value cells split on `;` with per-item trim; empty items dropped (`splitMulti`).
- Row-shape rule — deterministic and identical for CSV and XLSX, applied in this order:
  1. **Trim** trailing empty cells from the row.
  2. If fewer than 5 columns remain → **right-pad** with empty strings.
  3. If more than 5 columns remain → **row-level error**.

  Trimming first removes any parser-behavior ambiguity (e.g. excelize may or may not materialize trailing empty cells for a row, depending on the sheet's stored dimension).
- No `type` column: `question_type` is inferred with `domain.InferQuestionType(choices)` — empty choices ⇒ `free_response`, otherwise `multiple_choice`.

### 5.5 Per-row validation

Shared structural rules are **refactored out of the `Create` and `Update` handlers** (`handlers/questions.go:290` and `:169` — identical inline switch blocks today) into a new domain function, called by both handlers and by the importer:

```go
// internal/domain
func ValidateDraft(text string, choices, answers []string, typ string) error
```

`ValidateDraft` owns the moved `answersSubsetOfChoices` logic and the type-conditional structural checks:

- question text must be non-empty;
- `len(answers) >= 1` for **both** question types;
- `multiple_choice`: `len(choices) >= 2` **and** every answer ∈ choices (exact, case-sensitive equality);
- `free_response`: no choices.

The `answers >= 1` rule applies to both types deliberately: without it, empty answers would **vacuously pass** the answers-subset-of-choices check for `multiple_choice`.

**No handler behavior change.** The DTOs already enforce `len(answers) >= 1` at binding time — `CreateQuestionRequest.Answers` has `binding:"required,min=1"` (`dto/requests.go:29`) and `UpdateQuestionRequest.Answers` has `binding:"required,min=1,dive,required"` (`dto/requests.go:16`). The refactor moves only the type-conditional structural checks (choices ≥ 2 for MC, answers-subset-of-choices, no-choices for FR, answers ≥ 1) from the `Create`/`Update` handler bodies into the domain function with identical semantics; the handlers keep their existing binding tags. (`Update`'s DTO carries no question text, so it validates the stored question's text — the non-empty-text check is trivially satisfied there; only the type-conditional checks are load-bearing.)

**Tag rules** are applied per-row by the **importer only**, alongside `ValidateDraft` (tags are not part of its signature):

- at most 20 **file** tags;
- no empty tag.

The ≤20 cap applies to file tags only: the `"import"` marker is appended **after** validation, so a stored row may carry 21 tags in total — mirroring how `Create` appends `"manual-entry"` after binding (`handlers/questions.go:368-371`).

**Out of scope:** the refactor does **not** add tag validation to the `Create` handler (it currently has none; `Update`'s own tag checks at `handlers/questions.go:207-216` stay where they are). Tag rules (≤20, non-empty) are enforced only in the importer. Changing `Create`'s tag behavior is out of scope.

### 5.6 Imported field values

| Field | Value |
|---|---|
| `number` | `0` |
| `choice_labeling` | `"letter"` (`domain.ChoiceLabelingLetter`) — constant; the file carries no choice labels, answers/choices are value-only |
| `confidence` | `0.99` — constant; the file has no confidence column; fits the `numeric(3,2)` column (`0002_core.sql:36`) |
| `status` | `verified` (`domain.QuestionStatusVerified`) |
| `verified_at` | `now()` |
| `verified_by` | uploading user's ID (from JWT) |
| `tags` | file tags + `"import"` marker appended **after** the ≤20 file-tag check, so a stored row may carry 21 tags (mirrors the `manual-entry` convention in `Create`, `handlers/questions.go:368-371`) |
| `question_normalized` / `question_hash` | `domain.NormalizeQuestion(text)` → `domain.HashQuestion(norm)` |

## 6. Processing Pipeline

Synchronous, in request order:

1. **Read** the body (`MaxBytesReader` → `FormFile("file")` → `io.ReadAll`), mirroring `handlers/images.go` Upload.
2. **Sniff** format (`SniffKind`); reject unsupported / legacy `.xls`.
3. **Parse** all rows (CSV stream or XLSX first-sheet iterator). Parser-fatal errors ⇒ 400. Row count > `import.max_rows` ⇒ 400.
4. **Validate** every row, building a `*domain.Question` per valid row (hash, normalized text, constants from §5.6). Invalid rows are recorded in the report and skipped.
5. **Embed** (best effort, see §8): collect valid rows' texts, send in sequential chunks of 100; on the first failed chunk, skip embedding for all remaining chunks.
6. **Upsert** each valid row in its **own transaction** (row upsert + tag replace are atomic per row). A failed row is recorded; later rows proceed — bad rows never block good rows.
7. **Respond** 200 with the `ImportReportResponse`.

In-file duplicates are **not** pre-collapsed: rows process in file order, and a later row overwrites an earlier identical-hash row via the upsert (last-wins). The second occurrence truthfully reports `updated`, keeping `total = created + updated + failed` consistent.

## 7. Duplicate Resolution

Dedup during import is **exact hash only** (`domain.NormalizeQuestion` → `domain.HashQuestion`). No semantic dedup. On hash conflict the **file wins**: all content fields are replaced.

### 7.1 Chosen approach: SQL upsert, update-in-place

New method on `storage.QuestionRepo`:

```go
UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)
```

Implemented in a new file `internal/storage/postgres/question_import.go`. One transaction containing:

1. The upsert (conflict target: the `question_hash` UNIQUE constraint, `0002_core.sql:31`):

   ```sql
   INSERT INTO questions (...)
   VALUES (...)
   ON CONFLICT (question_hash) DO UPDATE SET
       question       = EXCLUDED.question,
       choices        = EXCLUDED.choices,
       answers        = EXCLUDED.answers,
       explanation    = EXCLUDED.explanation,
       confidence     = EXCLUDED.confidence,
       question_type  = EXCLUDED.question_type,
       status         = 'verified',
       verified_at    = now(),
       verified_by    = EXCLUDED.verified_by,
       embedding      = COALESCE(EXCLUDED.embedding, questions.embedding),
       updated_at     = now()
   RETURNING (xmax = 0)  -- true on INSERT ⇒ created flag
   ```

2. Tag delete-and-reinsert via the existing `linkTagTx` helper (`question_repo.go:416`), so tags are fully replaced by the file's set (plus `import`).

`COALESCE` preserves the existing embedding when the new one is nil: a hash match implies identical normalized text, so the old vector stays valid.

**`question_normalized` invariant:** `question_normalized` is deliberately **absent** from the `UPDATE SET` — a `question_hash` conflict implies identical normalized text under the current `NormalizeQuestion`, so the stored value is already correct. (`question = EXCLUDED.question` is kept: the file wins on raw casing/whitespace.) If `NormalizeQuestion` ever changes, old hashes simply won't match new ones, so former conflicts become plain inserts — the invariant is self-healing.

Properties: the question **ID never changes**; `session_questions` linkage is untouched; image bytes are untouched (see below).

### 7.2 Rejected alternatives

**Delete + recreate.** Rejected because:

1. `QuestionRepo.Delete` refuses session-linked questions with `question_in_use` (`question_repo.go:390`) — imports would fail on exactly the questions most worth updating.
2. `session_questions.question_id` is `ON DELETE CASCADE` — a forced delete silently unlinks historical session/image linkage.
3. Recreate assigns a new ID, breaking lineage.

**Reuse `UpdateByExpert`.** Rejected because it runs `cleanupImageBytesTx` (nulls original+enhanced image bytes for every linked image, `question_repo.go:306/326`) inside its transaction — an unwanted cross-aggregate side effect for import — and it never updates `embedding`. `UpsertFromImport` explicitly does **not** call `cleanupImageBytesTx`.

## 8. Embeddings (best-effort, batched)

- Extend `pipeline.AIEmbedder` with:

  ```go
  EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
  ```

- `*embedder.Embedder` implements it natively using the existing openai-go client (array input). Results are aligned to inputs by index and dim-checked (1536) exactly like `Embed`.
- `internal/ai/embedder/input.go` gains an array-input variant next to `StringInput` (same union-reconciliation comment applies).
- The importer collects valid rows' texts and sends them in **sequential chunks of 100**.
- **Failure strategy — fail-fast:** chunks are sent sequentially; on the **first failed chunk**, log a warning and **skip embedding for all remaining chunks**. Affected rows import with nil embedding — best-effort, **not** row errors, and the report is unaffected. This bounds embedding unavailability to a single client timeout (~30 s) instead of N_chunks × 30 s, keeping total request time within `server.write_timeout: 120s`. Individual chunk calls use the embedder's existing timeout.
- A **nil embedder** (unconfigured) ⇒ the embedding step is skipped silently.
- Existing pipeline call sites keep `Embed`; test fakes gain a trivial `EmbedBatch` stub.

**Typed-nil caveat:** `wire.go` currently declares `var emb pipeline.AIEmbedder` and assigns it only when `cfg.AI.Embedder.APIKey != ""`, so an unconfigured embedder is a **true nil interface**. When adapting `emb` to the importer's `BatchEmbedder`, preserve this: pass a real nil (e.g. `if emb != nil { batchEmb = emb }`), never a nil `*embedder.Embedder` wrapped in an interface — a typed nil would pass a `!= nil` check and panic on call.

## 9. Component Design

### 9.1 New package `internal/importer`

Mirrors `internal/pipeline`'s role: domain logic between HTTP and storage. Consumer-side narrow ports keep the package decoupled:

```go
type QuestionUpserter interface {
    UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)
}

type BatchEmbedder interface { // nil-able
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}
```

| File | Contents |
|---|---|
| `importer.go` | `type Service struct{...}`; `New(questions QuestionUpserter, embedder BatchEmbedder, maxRows int, log *slog.Logger) *Service`; `func (s *Service) Import(ctx context.Context, r io.Reader, kind FileKind, userID string) (Report, error)`. Orchestrates parse → validate → embed → per-row upsert → report. |
| `parse.go` | `FileKind` (`KindCSV`, `KindXLSX`); `SniffKind(data []byte) (FileKind, error)`; CSV row iterator; `splitMulti(cell string) []string`. |
| `parse_xlsx.go` | excelize streaming reader; first sheet only; limits per §5.3. |
| `validate.go` | Per-row validation building a `*domain.Question` (calls `domain.ValidateDraft`, tag rules, hash/normalized text, §5.6 constants). |
| `report.go` | `Report`, `RowError`, `maxImportRowErrors = 100`. |

### 9.2 Domain changes — `internal/domain`

- New `ValidateDraft(text string, choices, answers []string, typ string) error` (§5.5), absorbing `answersSubsetOfChoices` (moved from `handlers/questions.go:257`) and the type-conditional structural checks — including `len(answers) >= 1` for both question types.
- `QuestionHandler.Create` **and** `QuestionHandler.Update` are refactored to call `ValidateDraft` in place of their identical inline switch blocks (`handlers/questions.go:304-319`, `:181-196`); behavior unchanged — binding tags stay as-is, and `Update`'s own tag/confidence checks remain in the handler.

### 9.3 Storage changes

- `internal/storage/ports.go`: add `UpsertFromImport` to `QuestionRepo` (§7.1).
- New `internal/storage/postgres/question_import.go`: implementation per §7.1 — one tx with the `ON CONFLICT (question_hash)` upsert + tag replace via `linkTagTx`; **no** `cleanupImageBytesTx`.

### 9.4 Embedder changes

- `internal/pipeline/ports.go`: `AIEmbedder` gains `EmbedBatch` (§8).
- `internal/ai/embedder/embedder.go`: native `EmbedBatch` implementation.
- `internal/ai/embedder/input.go`: array-input variant next to `StringInput`.
- Pipeline test fakes: add an `EmbedBatch` stub.

### 9.5 HTTP layer

- New `internal/httpapi/handlers/question_import.go`:
  ```go
  func NewQuestionImportHandler(svc *importer.Service, uploadCfg config.UploadConfig) *QuestionImportHandler
  func (h *QuestionImportHandler) Upload(c *gin.Context)
  ```
  `Upload` follows the `images.go` Upload read pattern for size limiting and multipart handling: `MaxBytesReader` → `FormFile("file")` → `io.ReadAll(file)` → `SniffKind(data)` → `svc.Import(...)` → 200 report, or 400 with mapped file-level errors via `errorResponse`.

  **Data flow (explicit):** after `data, err := io.ReadAll(file)`, the handler calls `svc.Import(ctx, bytes.NewReader(data), kind, userID)`. The multipart reader is fully drained by `io.ReadAll` + `SniffKind`, so a **fresh `bytes.Reader` over the byte slice** — never the multipart stream — is passed to the importer. (`kind` is the `FileKind` returned by `SniffKind`; `userID` comes from the JWT context.)
- `internal/httpapi/dto`: `ImportReportResponse`, `ImportRowError`.
- `internal/httpapi/server.go`: construct the handler and register
  ```go
  questions.POST("/upload", RoleGuard("expert", "admin"), questionImportHandler.Upload)
  ```
  inside the existing questions group (line 113). `httpapi.NewServer` gains one parameter — growing to **11 parameters** (it already takes 10 today) — and the `Server` struct stores it. Acceptable for manual wiring, but flagged: consider a params struct if the signature grows further.

### 9.6 Wiring — `internal/app/wire.go`

```go
// emb must remain a nil-able interface variable: it is declared as
// `var emb pipeline.AIEmbedder` and assigned only when
// COEUS_AI_EMBEDDER_API_KEY is set (as wire.go does today).
// Interface-to-interface assignment preserves true nil, so the
// importer Service's nil-checks work. Never pass a concrete
// (*embedder.Embedder)(nil).
imp := importer.New(questionRepo, emb, cfg.Import.MaxRows, slog.Default())
```

…passed into `httpapi.NewServer` (respecting the typed-nil caveat of §8). Since `pipeline.AIEmbedder` includes `EmbedBatch` after §9.4, a non-nil `emb` satisfies the importer's `BatchEmbedder` port directly; an unconfigured `emb` arrives as a true nil interface.

## 10. Config

New section in `internal/config`:

```go
type ImportConfig struct {
    MaxRows int `yaml:"max_rows"`
}
```

- `Config` gains `Import ImportConfig \`yaml:"import"\``.
- Embedded `config.yaml` default:
  ```yaml
  import:
    max_rows: 20000
  ```
- Env override in `applyEnvOverrides`: `COEUS_IMPORT_MAX_ROWS` (parse-int with the existing error-wrapping style).

**Rationale for 20000:** worst-case synchronous runtime (~1–3 ms per-row transaction + N/100 sequential embedding calls) stays within `server.write_timeout: 120s`.

## 11. Error Taxonomy

**File-level errors** — new sentinel errors in the importer package (constructed via `domain.NewError(code, message)` so `errorResponse` maps them), all ⇒ 400:

| Condition | Handling |
|---|---|
| Unsupported format | sentinel; message `unsupported file format` |
| Legacy `.xls` | same sentinel family, explicit message `legacy .xls not supported — save as .xlsx` |
| Empty file | sentinel |
| Too many rows (> `import.max_rows`) | sentinel |
| Malformed CSV/XLSX (parser-fatal) | sentinel wrapping the parser error |
| Body over size limit | existing `MaxBytesReader` handling, as in `images.go` |

**Row-level errors** — plain strings in the report mirroring the validation rules (§5.5). No new sentinels, following the `question_in_use` string-code precedent (`domain/errors.go:45`).

## 12. Testing Plan

### 12.1 Unit tests (`-short`-safe)

- **`validate.go` / `splitMulti` / `SniffKind`:** table-driven tests — column padding and overflow, multi-value splitting/trimming/empty-drop, type inference, tag rules, all `ValidateDraft` branches; sniffing of CSV / zip / ole-storage / octet-stream / empty inputs.
- **XLSX parsing:** fixtures generated in-test via excelize (no binary fixtures committed).
- **`Service.Import` orchestration** against a one-method fake `QuestionUpserter`:
  - in-file duplicates: last-wins, second occurrence reports `updated`;
  - nil-embedder path (embedding skipped);
  - failed-chunk path: first chunk failure skips embedding for all remaining chunks (rows import with nil embedding, no row errors);
  - report arithmetic: `total = created + updated + failed`, error cap at 100, 1-based row numbers.
- **Handler tests:** multipart bodies mirroring `images_test.go` — 200 report shape; 400 mapping for unsupported format, legacy `.xls`, empty file, oversize body; role enforcement via `RoleGuard`.

### 12.2 Integration tests (testcontainers, non-`-short`)

Using `setupTestDB(t)` (`internal/storage/postgres/testhelpers_test.go`), for `UpsertFromImport`:

- insert → upsert same hash returns `created = false`; content fields replaced; tags replaced; ID unchanged; `verified_by`/`verified_at` set;
- embedding preserved when nil is passed (`COALESCE`); fresh embedding stored when provided;
- upsert on a **session-linked** question succeeds (the `Delete` guard is never in play);
- **image bytes survive** an import-update of an image-linked, fully-resolved question — this test is the regression guard proving the upsert never triggers `cleanupImageBytesTx` semantics (`question_repo.go:326`; rejected alternative in §7.2).

## 13. Risks / Boundary Conditions

| Risk / boundary | Mitigation |
|---|---|
| XLSX zip-bomb / decompression blowup | `excelize.Options{UnzipSizeLimit: 100 << 20, UnzipXMLSizeLimit: 64 << 20}`; input already capped at 10 MB. Pin excelize v2.11.0 (CVE-2026-54063). |
| Huge files block a worker synchronously | `import.max_rows: 20000` keeps worst case within the 120 s write timeout; async processing is a documented future option only if routinely exceeded. |
| Embedding outage during import | Best-effort, fail-fast: first failed chunk ⇒ skip embedding for all remaining chunks (bounded to one ~30 s client timeout, keeping the request within the 120 s write timeout); affected rows import with nil embedding, warning logged, import still succeeds. Nil embedder ⇒ step skipped. |
| Typed-nil embedder panics | Wire a true nil when unconfigured (§8); keep the existing `var emb pipeline.AIEmbedder` pattern. |
| In-file duplicates skew counts | Deliberate: not pre-collapsed; last-wins semantics, second occurrence reports `updated`; totals stay consistent. |
| Non-UTF-8 CSV silently mangled | Impossible by construction: non-UTF-8 sniffs as octet-stream ⇒ rejected as unsupported. |
| Update-in-place clobbers curated edits | Accepted product decision: file wins on all content fields; `status` is forced to `verified` and `verified_by/at` to the uploader/now, so the audit trail reflects the import. |
| Over-5-column rows from sloppy exports | Row-level error, not file failure; uploader fixes and re-uploads (idempotent). |
