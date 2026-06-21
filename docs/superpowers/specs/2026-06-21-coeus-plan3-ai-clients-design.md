# Coeus Plan 3 — Real AI Clients, Wiring, and N+1 Fix

| Field | Value |
|-------|-------|
| **Title** | Plan 3: Real AI Clients (Enhancer, Extractor, Verifier, Embedder), Application Wiring, and N+1 Query Fix |
| **Date** | 2026-06-21 |
| **Status** | Approved |
|**Depends on**| Plan 1 (foundation: config, Postgres, HTTP skeleton, auth), Plan 2 (sessions, image upload, pipeline orchestration with fakes, worker pool with LISTEN/NOTIFY) |
| **Module** | `github.com/vlgrigoriev/coeus` (Go 1.26.3) |

---

## 1. Overview

Plan 3 replaces the four fake pipeline ports with real implementations and
brings the background worker pool online. After Plan 3, an uploaded image flows
end-to-end: enhance → extract → verify → embed, with results written to the
database and served back to the client.

Concretely, Plan 3 delivers:

1. **Four AI client implementations** behind the existing `pipeline` interfaces
   (`internal/pipeline/ports.go`):
   - `ImageEnhancer` — deterministic Go image processing (no AI).
   - `AIExtractor` — Moonshot (Kimi) vision model via OpenAI-compatible API.
   - `AIVerifier` — DeepSeek text model via OpenAI-compatible API.
   - `AIEmbedder` — OpenAI embeddings API.
2. **Application wiring** (`internal/app/wire.go`) that constructs all clients,
   the `Pipeline`, and the `WorkerPool`, plus lifecycle changes in
   `cmd/coeus/main.go` and `App.Close()`.
3. **Config renames** from vendor-based to role-based naming (`kimi`→`vision`,
   `deepseek`→`reviewer`), plus required-key validation.
4. **N+1 query fix** in the image-list handler: one bulk job-status lookup
   instead of one query per image.

### 1.1 Goal

An implementer working from this spec should be able to add the four packages,
update config and wiring, apply the N+1 fix, run the tests, and have a working
pipeline — without needing to reverse-engineer intent or make design decisions.

---

## 2. Background

### 2.1 What Plan 1 delivered

Config loading (`internal/config`), Postgres pool + migrations
(`internal/storage/postgres`), domain types (`internal/domain`), auth (JWT +
password), and the Gin HTTP skeleton (`internal/httpapi`).

### 2.2 What Plan 2 delivered

- **Session lifecycle API** (`internal/httpapi/handlers/sessions.go`).
- **Image upload** (`internal/httpapi/handlers/images.go`) — multipart upload,
  MIME sniffing, dimension decode, `images.Create` + `jobs.Enqueue` (which
  fires `NOTIFY jobs_new`).
- **Pipeline orchestration** (`internal/pipeline/pipeline.go`) behind four
  narrow interface ports (`ports.go`): `ImageEnhancer`, `AIExtractor`,
  `AIVerifier`, `AIEmbedder`. The pipeline owns the full 10-step workflow:
  load → enhance → extract (with retries) → per-question dedup/embed/create →
  verify → persist report. It is tested end-to-end with in-memory fakes
  (`pipeline_test.go`).
- **Worker pool** (`internal/pipeline/worker.go`) — N workers, each with a
  dedicated `pgx.Conn` for `LISTEN jobs_new`, plus a reaper that reclaims stale
  and fails exhausted jobs. Fully implemented and unit-tested.
- **The gap Plan 3 closes:** `wire.go` constructs everything *except* the
  pipeline and worker pool. A `TODO(plan-3)` block (`wire.go:51-59`) shows the
  intended construction. Until Plan 3, uploaded images sit forever in `pending`.

### 2.3 The interface contracts (do not change)

All four ports are defined in `internal/pipeline/ports.go` and must remain
stable — the pipeline and its tests already depend on these exact signatures.

```go
// ports.go:10
type ImageEnhancer interface {
    Enhance(ctx context.Context, original []byte, mime string) ([]byte, error)
}
// ports.go:18
type AIExtractor interface {
    Extract(ctx context.Context, image []byte, mime string) (ExtractResult, error)
}
// ports.go:24
type AIVerifier interface {
    Verify(ctx context.Context, questions []ExtractedQuestion) (VerifyResult, error)
}
// ports.go:29
type AIEmbedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}
```

### 2.4 The shared types the clients must produce

Defined in `ports.go`; reproduced here so client authors never have to leave
this document. **These types are the contract — Plan 3 must populate them
correctly.**

```go
// ports.go:33
type Answer struct {
    ID   string // label like "A", "1", "i" — used for choice-labeling inference
    Text string // the answer text — stored value-only in the DB
}

// ports.go:40
type ExtractedQuestion struct {
    Number          int
    Text            string
    Choices         []Answer
    Answers         []Answer
    MultipleCorrect bool
    Confidence      float64  `json:"confidence,omitempty"`
    Tags            []string `json:"tags,omitempty"`
}

// ports.go:51 — error code constants
const (
    ExtractionCodeUnreadableImage = "unreadable_image"
    ExtractionCodeNoQuestions     = "no_questions_found"
    ExtractionCodePartial         = "partial_extraction"
    ExtractionCodeAIUnavailable   = "ai_unavailable" // set by the pipeline, not the client
)

// ports.go:60
type ExtractionError struct {
    Code   string
    Detail string
}

// ports.go:66
type ExtractResult struct {
    Questions []ExtractedQuestion
    Error     *ExtractionError // non-nil for partial or terminal content failures
}

// ports.go:73
type VerifiedQuestion struct {
    Index       int    // matches position in the []ExtractedQuestion passed to Verify
    Confidence  float64
    Explanation string
}

// ports.go:80
type VerificationSummary struct {
    Results []VerifiedQuestion
}

// ports.go:86
type VerifyResult struct {
    Summary VerificationSummary
    Report  json.RawMessage `json:"_verification"` // raw JSON, persisted to images.verification_report
}
```

**Retry contract the extractor must honor** (implemented in
`pipeline.go:230 extractWithRetries`):

| Return shape | Pipeline behavior |
|---|---|
| `(ExtractResult{Questions:[...]}, nil)` | Success — proceed. |
| `(ExtractResult{Error:{Code:"partial_extraction"}}, nil)` | Terminal success — keep partial questions, no retry. |
| `(ExtractResult{Error:{Code:"unreadable_image"\|"no_questions_found"}}, nil)` | Retry up to `ExtractMaxAttempts`. |
| `(ExtractResult{Error:{Code:<unknown>}}, nil)` | Terminal — no retry. |
| `(ExtractResult{}, err)` — transport failure | Retry up to `ExtractMaxAttempts`; after exhaustion the pipeline sets code `ai_unavailable` and creates a placeholder. |

The verifier and embedder have **no** retry contract — any error from them is
best-effort (pipeline logs a warning and continues).

---

## 3. Scope

### 3.1 In scope

- Four client packages: `enhancer`, `extractor`, `verifier`, `embedder`.
- Shared `oai` wrapper for OpenAI-compatible client construction.
- Role-based config rename and env-var rename.
- Required-API-key validation.
- `wire.go` construction of all clients + `Pipeline` + `WorkerPool`.
- `main.go` calls `WorkerPool.Start`; `App.Close()` stops workers before DB.
- N+1 fix: `JobQueue.FindJobStatusesBySession` + handler rewrite.
- Subject tags populated during extraction (medicine, math, etc.) — see §7.2.
- Unit tests for every client using `httptest.NewServer` + fixture JSON (no live
  API calls). Config tests for renamed env vars and validation.

### 3.2 Out of scope (deferred)

| Item | Deferred to |
|------|-------------|
| `CleanBytes` (delete image bytes after all questions verified) | Future moderation plan |
| Expert moderation endpoints (e.g. `PATCH /questions/:id`) | Future moderation plan |
| Tightening `ResponseFormat` from `JSONObject` → `JSONSchema` | After confirming against real APIs |
| Rate limiting / circuit breakers on AI calls | Future plan |
| Live/contract tests against real APIs | Manual smoke test only (documented) |

---

## 4. Architecture

### 4.1 Package layout (role-based, not vendor-based)

Packages are named by **role** (`extractor`, `verifier`, `embedder`,
`enhancer`), not by vendor (`kimi`, `deepseek`). Rationale: the pipeline talks
to roles, not vendors; swapping Moonshot for another vision provider changes
one constructor body, not import paths across the codebase. The vendor is a
config detail (`base_url` + `model`).

```
internal/ai/
├── oai/
│   └── client.go          # NewClient(baseURL, apiKey, timeout) → *openai.Client
├── extractor/
│   ├── extractor.go       # implements pipeline.AIExtractor (vision LLM)
│   ├── prompt.go          # extraction system + user prompt templates
│   ├── schema.go          # JSON-tagged DTOs + invopop schema + →pipeline mapping
│   └── extractor_test.go  # httptest.NewServer + fixture JSON
├── verifier/
│   ├── verifier.go        # implements pipeline.AIVerifier (text LLM)
│   ├── prompt.go          # verification prompt
│   ├── schema.go          # JSON-tagged DTOs + →pipeline mapping
│   └── verifier_test.go
├── embedder/
│   ├── embedder.go        # implements pipeline.AIEmbedder
│   └── embedder_test.go
└── enhancer/
    ├── enhancer.go        # implements pipeline.ImageEnhancer (deterministic Go)
    └── enhancer_test.go
```

Each package declares `var _ pipeline.AI<Name> = (*<Name>)(nil)` to guarantee
interface satisfaction at compile time — the same pattern already used by the
storage layer (`job_queue.go:23`).

### 4.2 The `oai` wrapper

A single factory removes SDK boilerplate from each client. All three LLM-style
clients (extractor, verifier, embedder) take an OpenAI-compatible endpoint, so
they share one constructor. The enhancer is pure Go and does **not** use `oai`.

```go
// internal/ai/oai/client.go
package oai

import (
    "net/http"
    "time"

    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
)

func NewClient(baseURL, apiKey string, timeout time.Duration) *openai.Client {
    return openai.NewClient(
        option.WithBaseURL(baseURL),
        option.WithAPIKey(apiKey),
        option.WithHTTPClient(&http.Client{Timeout: timeout}),
    )
}
```

**Why `Chat.Completions.New()` and not `Responses.New()`:** Kimi and DeepSeek
implement the OpenAI `/v1/chat/completions` surface but do **not** implement
`/v1/responses`. Using `Chat.Completions.New()` keeps one code path for all
providers.

---

## 5. Config changes

### 5.1 New struct shape (`internal/config/config.go`)

Rename `KimiConfig` → `VisionConfig`, `DeepSeekConfig` → `ReviewerConfig`.
`EmbedderConfig` is unchanged. `AIConfig` field names change from `kimi`/
`deepseek` to `vision`/`reviewer`.

```go
type AIConfig struct {
    Vision   VisionConfig   `yaml:"vision"`   // was: Kimi     KimiConfig     `yaml:"kimi"`
    Reviewer ReviewerConfig `yaml:"reviewer"` // was: DeepSeek DeepSeekConfig `yaml:"deepseek"`
    Embedder EmbedderConfig `yaml:"embedder"` // unchanged
}

type VisionConfig struct {    // was: KimiConfig — same fields
    BaseURL string        `yaml:"base_url"`
    APIKey  string        `yaml:"api_key"`
    Model   string        `yaml:"model"`
    Timeout time.Duration `yaml:"timeout"`
}

type ReviewerConfig struct {  // was: DeepSeekConfig — same fields
    BaseURL string        `yaml:"base_url"`
    APIKey  string        `yaml:"api_key"`
    Model   string        `yaml:"model"`
    Timeout time.Duration `yaml:"timeout"`
}

// EmbedderConfig is UNCHANGED (no Timeout field — see §5.5).
type EmbedderConfig struct {
    BaseURL string `yaml:"base_url"`
    APIKey  string `yaml:"api_key"`
    Model   string `yaml:"model"`
    Dim     int    `yaml:"dim"`
}
```

### 5.2 Env var renames (`applyEnvOverrides`)

Replace the four Kimi/DeepSeek env reads with the renamed equivalents. The
embedder reads are unchanged.

| Old env var | New env var |
|---|---|
| `COEUS_AI_KIMI_API_KEY` | `COEUS_AI_VISION_API_KEY` |
| `COEUS_AI_KIMI_BASE_URL` | `COEUS_AI_VISION_BASE_URL` |
| `COEUS_AI_DEEPSEEK_API_KEY` | `COEUS_AI_REVIEWER_API_KEY` |
| `COEUS_AI_DEEPSEEK_BASE_URL` | `COEUS_AI_REVIEWER_BASE_URL` |
| `COEUS_AI_EMBEDDER_API_KEY` | unchanged |
| `COEUS_AI_EMBEDDER_BASE_URL` | unchanged |

Replacement code in `applyEnvOverrides`:

```go
if v := os.Getenv("COEUS_AI_VISION_API_KEY"); v != "" {
    cfg.AI.Vision.APIKey = v
}
if v := os.Getenv("COEUS_AI_VISION_BASE_URL"); v != "" {
    cfg.AI.Vision.BaseURL = v
}
if v := os.Getenv("COEUS_AI_REVIEWER_API_KEY"); v != "" {
    cfg.AI.Reviewer.APIKey = v
}
if v := os.Getenv("COEUS_AI_REVIEWER_BASE_URL"); v != "" {
    cfg.AI.Reviewer.BaseURL = v
}
// embedder reads unchanged
```

### 5.3 Embedded YAML defaults (`internal/config/config.yaml`)

```yaml
ai:
  vision:
    model: "kimi-k2.7"
    timeout: 90s
  reviewer:
    model: "deepseek-v4-pro"
    timeout: 60s
  embedder:
    model: "text-embedding-3-small"
    dim: 1536
```

(`base_url` and `api_key` are intentionally absent from the YAML — secrets and
provider endpoints come from env. The structs already treat empty `base_url` as
"use the SDK default" for OpenAI.)

### 5.4 Validation (`Validate()`)

Add the three required-key checks after the existing DSN and JWT checks
(`config.go:143`):

```go
if c.AI.Vision.APIKey == "" {
    return fmt.Errorf("ai.vision.api_key is required (set COEUS_AI_VISION_API_KEY)")
}
if c.AI.Reviewer.APIKey == "" {
    return fmt.Errorf("ai.reviewer.api_key is required (set COEUS_AI_REVIEWER_API_KEY)")
}
if c.AI.Embedder.APIKey == "" {
    return fmt.Errorf("ai.embedder.api_key is required (set COEUS_AI_EMBEDDER_API_KEY)")
}
```

This makes `main.go` fail fast at startup (`main.go:23`) if any key is missing,
rather than failing per-request inside the pipeline.

### 5.5 Embedder timeout (detail)

`EmbedderConfig` has no `Timeout` field (intentionally unchanged). The embedder
client still needs an HTTP timeout — use a package-level constant in
`internal/ai/embedder` rather than expanding the config struct:

```go
const embedderDefaultTimeout = 30 * time.Second
```

Pass it to `oai.NewClient(cfg.BaseURL, cfg.APIKey, embedderDefaultTimeout)`.

---

## 6. Dependencies

Add to `go.mod`:

| Module | Purpose |
|---|---|
| `github.com/openai/openai-go` | Official SDK — `Chat.Completions.New()`, `Embeddings.New()`, response formats, `option.With*` |
| `github.com/davidbyttow/govips/v2` | Enhancer — CGO binding to libvips (`AutoRotate`, `Linear1`, `Gamma`, `Sharpen`, JPEG/PNG/WebP decode+encode) |
| `github.com/invopop/jsonschema` | Generate JSON Schema from Go structs — fed into the prompt now, into `ResponseFormatJSONSchema` later |

Run `go get github.com/openai/openai-go github.com/davidbyttow/govips/v2 github.com/invopop/jsonschema` then `go mod tidy`.

**Why official `openai/openai-go` and not `sashabaranov/go-openai`:** the
sashabaranov client is stale (lacks current structured-output and embeddings
APIs). The official SDK is maintained, supports `option.WithBaseURL` for
OpenAI-compatible providers (Kimi/DeepSeek), and is the path forward for
`ResponseFormatJSONSchema`.

**Why `davidbyttow/govips/v2` and not `disintegration/imaging`:** `imaging` has
not been updated in 6–7 years. govips is actively maintained, wraps libvips
(a high-performance image processing library), and handles JPEG/PNG/WebP decode
and encode natively — no separate WebP decoder/encoder needed.

> **System dependency — libvips (CGO required):** govips is a CGO binding to
> libvips, which must be installed on the system. Install before building:
> - **macOS:** `brew install vips pkg-config`
> - **Linux:** `apt install libvips-dev`
>
> CGO must be enabled (`CGO_ENABLED=1`, which is the default for native builds).
> govips handles WebP natively via libvips — no `golang.org/x/image/webp`
> blank-import is needed in the enhancer.

---

## 7. Client implementations

### 7.1 Enhancer (`internal/ai/enhancer/`)

Deterministic Go image processing via govips (libvips). **No AI call.**

#### Constructor & method

```go
func New(log *slog.Logger) *Enhancer
func (e *Enhancer) Enhance(ctx context.Context, original []byte, mime string) ([]byte, error)
```

#### Lifecycle (libvips init/shutdown)

govips requires a one-time `vips.Startup(nil)` before any image operation,
and `vips.Shutdown()` at exit. This is wired in §8.1 (Build) and §8.3 (Close),
not inside the enhancer — the enhancer assumes libvips is already initialized.

#### Behavior

The govips API is imperative/mutating on `*vips.ImageRef`. Implementation:

```go
package enhancer

import (
    "bytes"
    "context"
    "fmt"
    "log/slog"

    "github.com/davidbyttow/govips/v2/vips"
)

func New(log *slog.Logger) *Enhancer {
    return &Enhancer{log: log}
}

func (e *Enhancer) Enhance(ctx context.Context, original []byte, mime string) ([]byte, error) {
    if err := ctx.Err(); err != nil {
        return nil, fmt.Errorf("enhance: %w", err)
    }

    img, err := vips.NewImageFromBuffer(original)
    if err != nil {
        return nil, fmt.Errorf("enhance: decode: %w", err)
    }
    defer img.Close()

    if err := img.AutoRotate(); err != nil {
        return nil, fmt.Errorf("enhance: auto-rotate: %w", err)
    }

    // +15% contrast (Linear1: out = a*in + b, pivoting around mid-gray 128)
    // 1.15 * (in - 128) + 128 = 1.15*in - 19.2
    if err := img.Linear1(1.15, -19.2); err != nil {
        return nil, fmt.Errorf("enhance: contrast: %w", err)
    }

    // Gamma 1.15 brightens midtones (govips: out = in^(1/exponent), so >1.0 brightens)
    if err := img.Gamma(1.15); err != nil {
        return nil, fmt.Errorf("enhance: gamma: %w", err)
    }

    // Mild sharpen for text edge crispness (sigma=0.5, x1=1.0, m2=2.0)
    if err := img.Sharpen(0.5, 1.0, 2.0); err != nil {
        return nil, fmt.Errorf("enhance: sharpen: %w", err)
    }

    // Encode back to the same MIME type
    switch mime {
    case "image/jpeg":
        buf, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: 92})
        return buf, err
    case "image/png":
        buf, _, err := img.ExportPng(&vips.PngExportParams{Compression: 6})
        return buf, err
    case "image/webp":
        buf, _, err := img.ExportWebp(&vips.WebpExportParams{Quality: 92})
        return buf, err
    default:
        return nil, fmt.Errorf("enhance: unsupported MIME %q", mime)
    }
}
```

Operations applied, in order:
1. `AutoRotate()` — honor EXIF orientation (govips reads EXIF via libvips).
2. `Linear1(1.15, -19.2)` — +15% contrast, pivoting around mid-gray 128.
3. `Gamma(1.15)` — brightens midtones (govips `out = in^(1/exponent)`, so >1.0 brightens).
4. `Sharpen(0.5, 1.0, 2.0)` — mild sharpen for text edge crispness.
5. Encode to the **same MIME** the caller passed:
   - `image/jpeg` → `ExportJpeg` (quality 92)
   - `image/png` → `ExportPng` (compression 6)
   - `image/webp` → `ExportWebp` (quality 92)
   - any other MIME → return `(nil, error)` (pipeline falls back to original).
6. Return encoded bytes.

#### Error handling & fallback

The pipeline already treats enhancer failure as best-effort
(`pipeline.go:95-97`): on error it logs a warning and continues with the
original bytes. Therefore `Enhance` returns `(nil, err)` for any decode/encode
failure or unsupported MIME; it does **not** attempt its own fallback. Return
`(nil, fmt.Errorf("enhance: %w", err))` with context.

If `len(encoded) == 0` the pipeline keeps the original (the `len(enhanced) > 0`
guard at `pipeline.go:97`).

#### Tests (`enhancer_test.go`)

CGO/libvips required; no `httptest`. govips handles decode/encode internally.
Cases:

| Case | Assertion |
|---|---|
| JPEG round-trip | no error; decoded dimensions preserved; output bytes differ from input; output decodes as JPEG |
| PNG round-trip | same, decodes as PNG |
| WebP round-trip | same, decodes as WebP (govips handles natively) |
| Invalid bytes | error (e.g. `[]byte("not an image")`) |
| Unsupported MIME (`"application/pdf"`) | error |

Synthesize a small input image (PNG via `image/png` + `image.NewRGBA`, then
encode to the target MIME with govips, or use a small fixture file). Verify
dimensions with `vips.NewImageFromBuffer` + `img.Width()`/`img.Height()`.

---

### 7.2 Extractor (`internal/ai/extractor/`)

Vision LLM (Moonshot/Kimi) via OpenAI-compatible Chat Completions.

#### Constructor & method

```go
func New(cfg config.VisionConfig, log *slog.Logger) *Extractor
func (e *Extractor) Extract(ctx context.Context, image []byte, mime string) (ExtractResult, error)
```

`New` builds the client once: `oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout)`.

#### Request shape

Call `client.Chat.Completions.New(ctx, ...)` with:

- **Model:** `cfg.Model`.
- **Messages:**
  - System: the extraction system prompt (§7.2.2).
  - User: a multimodal message — one text part (the user prompt, optionally
    including the rendered JSON schema as a format reminder) and one image part
    as a base64 data URL: `data:<mime>;base64,<base64(image)>`.
- **ResponseFormat:** `openai.ResponseFormatJSONObject{}` (decision #6 — start
  broad, tighten to `ResponseFormatJSONSchema` later). Serialized by the SDK as
  `{"type":"json_object"}`.

The data-URL image part uses the openai-go chat-content-part union:

```go
openai.ChatCompletionContentPartUnionParam{
    OfChatCompletionContentPart: openai.ChatCompletionContentPart{
        Type: "image_url",
        ImageURL: openai.ChatCompletionContentPartImageImageURL{
            URL:    "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image),
        },
    },
}
```

(Confirm the exact union field names against the pinned openai-go version's
generated types during implementation; the shape above is the documented
intent. The wire-level requirement is a message part of type `image_url` whose
`url` is the data URL.)

#### 7.2.2 Prompt (`prompt.go`)

The system prompt is based on the `extract-questions-from-image` skill
(`skills/extract-questions-from-image/SKILL.md`), which defines the base JSON
contract the model must produce. We **extend** the skill format with one
additional field: `tags` (subject classifiers like "math", "chemistry") — this
field is not in the skill's required-keys list but is needed for the pipeline's
tagging workflow (§3.1). Store the prompt as a `const`:

```
You are an exam-image OCR and parsing engine. Analyze the image and extract
every visible question as structured JSON.

Output a single JSON object with this exact format:
{
  "questions": [
    {
      "number": <int>,
      "question": "<full question text>",
      "multiple_correct": <bool>,
      "choices": ["<choice text without label prefix>", ...],
      "answers": [{"id": "<bare label>", "value": "<choice text>"}],
      "confidence": <0.0-1.0>,
      "explanation": "<brief reasoning for the answer>",
      "tags": ["<subject tag>", ...]
    }
  ]
}

Rules:
- Strip label prefixes from choices: store "Paris" not "A) Paris".
- Answer IDs are bare labels: "A", "B", "1", "2" (no punctuation — no ")", no ".").
- answers[].value must match a choice string exactly (without label prefix).
- Tags are subject classifiers: "math", "chemistry", "history", "medicine", etc.
  Populate at least one tag per question when the subject is identifiable.
- Confidence scoring:
    0.95-1.0: very clear, little doubt
    0.80-0.94: minor ambiguity (small/blurry text)
    0.50-0.79: significant inference needed (rotated, partial)
    0.0-0.49: unreliable / guess
- Image orientation: mentally rotate the image to the correct orientation before
  reading. Do not mention rotation unless it caused partial extraction failure.
- Never invent answers. If the correct answer is not visible, leave "answers": []
  and lower confidence.

Error handling — if the image cannot be fully parsed, return:
{
  "error": {
    "code": "unreadable_image" | "partial_extraction" | "no_questions_found",
    "message": "<human-readable description>",
    "details": "<which questions were affected, if applicable>",
    "questions_extracted": <N>,
    "questions_expected": <M>
  },
  "questions": [<any questions that were successfully extracted>]
}

Error codes:
- unreadable_image: the entire image is unreadable (blurry, blank, corrupt).
- partial_extraction: some questions extracted, some not (include the extracted
  ones in "questions" and list the missing ones in "details").
- no_questions_found: the image is readable but contains no questions.

[Reference: skills/extract-questions-from-image/SKILL.md for the full specification]
```

User prompt (text part): a short instruction plus the rendered JSON schema for
reference:

> Extract all questions from this exam image. Respond as a JSON object matching
> this schema:\n\n<schema>

The `<schema>` is the JSON generated by `invopop/jsonschema.Reflector` from
`extractionResponse` (§7.2.3). This anchors the model on field names without
requiring `ResponseFormatJSONSchema`.

#### 7.2.3 Schema & DTOs (`schema.go`)

**Key design point:** the DTOs match the `extract-questions-from-image` skill's
wire format exactly. The skill returns `choices` as plain strings (labels
stripped) and `answers` as `{id, value}` pairs — this is the format models
produce most reliably and what the skill's examples demonstrate.

The pipeline's `ExtractedQuestion.Choices` is `[]Answer` (each with `ID` +
`Text`). The extractor must therefore synthesize choice IDs from the answer
labeling pattern detected from the answers array.

```go
package extractor

type answerDTO struct {
    ID    string `json:"id"`
    Value string `json:"value"`
}

type questionDTO struct {
    Number          int         `json:"number"`
    Question        string      `json:"question"` // NOT "text"
    MultipleCorrect bool        `json:"multiple_correct"`
    Choices         []string    `json:"choices"` // plain strings, no labels
    Answers         []answerDTO `json:"answers"`
    Confidence      float64     `json:"confidence"`
    Explanation     string      `json:"explanation"`
    Tags            []string    `json:"tags,omitempty"` // subject tags (medicine, math, etc.)
}

type extractionErrorDTO struct {
    Code               string `json:"code"`
    Message            string `json:"message"`
    Details            string `json:"details,omitempty"`
    QuestionsExtracted int    `json:"questions_extracted,omitempty"`
    QuestionsExpected  int    `json:"questions_expected,omitempty"`
}

type extractionResponse struct {
    Questions []questionDTO       `json:"questions,omitempty"`
    Error     *extractionErrorDTO `json:"error,omitempty"`
}
```

Mapping to pipeline types — the key challenge: the skill returns `choices` as
plain strings and `answers` as `{id, value}`. The pipeline's
`ExtractedQuestion.Choices` is `[]Answer` (with ID + Text). The extractor:

1. Detects the labeling pattern from answer IDs (letters → "letter",
   numbers → "number").
2. Assigns sequential IDs to choices based on that pattern.
3. Maps answer `{id, value}` to pipeline `Answer{ID, Text}`.
4. Maps the skill's `"message"` to the pipeline's `ExtractionError.Detail`
   (the pipeline field is named `Detail` but the skill field is `Message`).

```go
func toPipeline(r extractionResponse) pipeline.ExtractResult {
    res := pipeline.ExtractResult{}
    if r.Error != nil {
        res.Error = &pipeline.ExtractionError{
            Code:   r.Error.Code,
            Detail: r.Error.Message, // skill "message" → pipeline "Detail"
        }
    }
    res.Questions = make([]pipeline.ExtractedQuestion, len(r.Questions))
    for i, q := range r.Questions {
        labeling := detectChoiceLabeling(q.Answers)
        res.Questions[i] = pipeline.ExtractedQuestion{
            Number:          q.Number,
            Text:            q.Question, // skill "question" → pipeline "Text"
            Choices:         assignChoiceIDs(q.Choices, labeling),
            Answers:         mapAnswers(q.Answers),
            MultipleCorrect: q.MultipleCorrect,
            Confidence:      q.Confidence,
            Tags:            q.Tags,
        }
    }
    return res
}

// detectChoiceLabeling inspects answer IDs to determine if choices are labeled
// with letters (A, B, C…) or numbers (1, 2, 3…). Defaults to "letter".
func detectChoiceLabeling(answers []answerDTO) string {
    if len(answers) == 0 {
        return "letter"
    }
    id := answers[0].ID
    if len(id) > 0 && id[0] >= '0' && id[0] <= '9' {
        return "number"
    }
    return "letter"
}

// assignChoiceIDs creates Answer objects with sequential IDs based on the
// labeling pattern: A,B,C… for "letter", 1,2,3… for "number".
func assignChoiceIDs(choices []string, labeling string) []pipeline.Answer {
    out := make([]pipeline.Answer, len(choices))
    for i, text := range choices {
        out[i] = pipeline.Answer{
            ID:   labelFor(i, labeling),
            Text: text,
        }
    }
    return out
}

// mapAnswers converts skill {id, value} pairs to pipeline {ID, Text}.
func mapAnswers(answers []answerDTO) []pipeline.Answer {
    out := make([]pipeline.Answer, len(answers))
    for i, a := range answers {
        out[i] = pipeline.Answer{ID: a.ID, Text: a.Value}
    }
    return out
}

// labelFor returns the i-th label in the given pattern.
// labelFor(0, "letter") = "A", labelFor(1, "letter") = "B", ...
// labelFor(0, "number") = "1", labelFor(1, "number") = "2", ...
// Note: for >26 letter-labeled choices (extremely rare on exams), this wraps
// to non-letter characters. Acceptable for MVP — real exams rarely exceed ~10 choices.
func labelFor(i int, labeling string) string {
    switch labeling {
    case "number":
        return strconv.Itoa(i + 1)
    default:
        return string(rune('A' + i))
    }
}
```

**Note on the `Explanation` field:** the skill returns `explanation` per
question, but the pipeline's `ExtractedQuestion` has no `Explanation` field
(only `VerifiedQuestion` does). The explanation is therefore not carried into
the pipeline type — it is consumed by the extractor's DTO for schema generation
and prompt fidelity, but not persisted at extraction time. The verifier (§7.3)
re-derives its own `explanation` field from the model output.

**Tags:** the `Tags` field IS populated (in scope per §3.1). The pipeline always
appends `"ai-generated"` to the tags when persisting questions (see
`pipeline.go`), so the extractor only needs to return subject tags like
`"medicine"`, `"math"`, `"chemistry"`.

JSON Schema (for the prompt) is generated once via
`jsonschema.Reflector{DoNotReference: true}.Reflect(extractionResponse{})`.

#### Response parsing & error handling

After the call, take `completion.Choices[0].Message.Content` (a string). Then:

1. **Defensive cleanup:** strip a leading/trailing ```` ```json ```` / ```` ``` ````
   fence if present (some models wrap JSON despite `json_object`). A small helper
   trims whitespace, then if the string starts with ```` ``` ```` it removes the
   first line and the trailing fence. This is best-effort.
2. `json.Unmarshal` into `extractionResponse`.
3. Branch on the result:
   - On `Unmarshal` failure → return `(pipeline.ExtractResult{}, fmt.Errorf("extract: parse model JSON: %w", err))` — **transport-class** error so the pipeline retries.
   - On success with `r.Error != nil` and `code == "partial_extraction"` and
     `len(r.Questions) > 0` → return `(toPipeline(r), nil)` (content-class; the
     pipeline keeps the partial questions).
   - On success with `r.Error != nil` (unreadable / no_questions / unknown) →
     return `(toPipeline(r), nil)` (content-class; pipeline retries per code).
   - On success with questions and no error → `(toPipeline(r), nil)`.
4. If the SDK call itself errors (HTTP 5xx, timeout, rate limit, network) →
   return `(pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err))` —
   transport-class, pipeline retries.

The split between content-class (`ExtractResult.Error != nil`, nil Go error) and
transport-class (non-nil Go error) is what the retry layer keys off of.

#### Tests (`extractor_test.go`)

Spin an `httptest.NewServer` whose handler reads the request body, asserts the
path is `/chat/completions`, the model matches, `messages[0]` is system, the
user message contains an `image_url` part, and `response_format.type == "json_object"`,
then writes a canned JSON completion. Pass `server.URL` as `VisionConfig.BaseURL`.

| Case | Server returns | Assert |
|---|---|---|
| Happy path | completion with `questions:[{number:1,question:"…",multiple_correct:false,choices:["Paris","London"],answers:[{id:"A",value:"Paris"}],confidence:0.9,explanation:"…",tags:["geography"]}]` | `result.Questions` length 1; `Text` from `question`; `Choices` have synthesized IDs (A,B…) + text; `Answers` mapped `{ID,Text}`; `Tags` present; nil Go error; nil `result.Error` |
| Unreadable image | `{"error":{"code":"unreadable_image","message":"blurry"}}` | nil Go error; `result.Error.Code == "unreadable_image"`; `result.Error.Detail == "blurry"` (mapped from `message`) |
| No questions | `{"error":{"code":"no_questions_found",...}}` | nil Go error; `result.Error.Code == "no_questions_found"` |
| Partial extraction | `{"questions":[...], "error":{"code":"partial_extraction","questions_extracted":1,"questions_expected":3,...}}` | nil Go error; questions present; `result.Error.Code == "partial_extraction"` |
| Transport error (HTTP 500) | 500 | non-nil Go error; zero-value result |
| Malformed JSON (prose-wrapped) | completion whose content is ```` ```json\n{bad}\n``` ```` then real JSON — or pure prose | after cleanup the real-JSON variant parses; pure-prose variant returns transport error |
| Timeout | server sleeps beyond `Timeout` (use a tiny `VisionConfig.Timeout`) | non-nil Go error; request honors ctx |
| Custom base URL | assert request went to `server.URL` | (covered by all the above — the server only answers at its own URL) |

Share an `extractorTestServer(t, content string)` helper.

---

### 7.3 Verifier (`internal/ai/verifier/`)

Text LLM (DeepSeek) via OpenAI-compatible Chat Completions.

#### Constructor & method

```go
func New(cfg config.ReviewerConfig, log *slog.Logger) *Verifier
func (v *Verifier) Verify(ctx context.Context, questions []ExtractedQuestion) (VerifyResult, error)
```

Note the input type is `pipeline.ExtractedQuestion` (the interface contract).

#### Request shape

Call `client.Chat.Completions.New(ctx, ...)` with:

- **Model:** `cfg.Model`.
- **Messages:**
  - System: the verification prompt (§7.3.2).
  - User: the extracted questions serialized as JSON inside a fenced block:
    ```` ```json\n<marshaled questions>\n``` ````
- **ResponseFormat:** `openai.ResponseFormatJSONObject{}`.

The user-message JSON is a serialization of the input questions for the model
to read, using the `verificationInput` DTO (§7.3.3). The DTO field names
(`question`, `choices`, `answers` with `{id, value}`) match the extraction
output vocabulary, so the verifier sees the same shape it is asked to return.

#### 7.3.2 Prompt (`prompt.go`)

The system prompt is based on the `verify-extracted-questions` skill
(`skills/verify-extracted-questions/SKILL.md`), which defines the verification
contract: structural validation, answer correctness checks, and confidence
re-evaluation. Like the extractor, we extend the skill format with `tags` on
each question object. The verifier returns the **full** verified questions array
plus a `_verification` summary. Store the prompt as a `const`:

```
You are an answer-checking reviewer. You receive a JSON object containing
questions extracted from an exam image. For EACH question, independently:
1. Validate the JSON structure (fix missing/malformed fields — see rules below).
2. Verify the answer is correct (re-solve if needed).
3. Re-evaluate the confidence score.

Output the verified JSON with the same {questions: [...]} structure, plus a
"_verification" summary object:
{
  "_verification": {
    "timestamp": "<ISO-8601>",
    "questions_verified": <N>,
    "structural_fixes": ["..."],
    "answers_flagged": ["..."],
    "confidence_adjustments": ["Question 1: 0.92 → 0.85 (reason)"],
    "garbled_text_detected": ["..."],
    "summary": "<one-line summary>"
  },
  "questions": [
    {
      "number": 1,
      "question": "...",
      "multiple_correct": false,
      "choices": ["..."],
      "answers": [{"id": "A", "value": "..."}],
      "confidence": 0.85,
      "explanation": "original text\n\n[VERIFICATION FLAG]\n...",
      "tags": ["..."]
    }
  ]
}

Rules:
- Do NOT modify the question text, choices array, or answers array. Those come
  from the original image.
- If you disagree with an answer, append a [VERIFICATION FLAG] block to the
  explanation field — do NOT change the answers array. Format:
    [VERIFICATION FLAG]
    Original answer: {value} (id: {id})
    Verifier suggests: {your answer} (id: {your id})
    Reason: {brief reason}
    Action: awaiting human review
- Adjust the confidence field based on your assessment:
    - Increase: straightforward, unambiguously correct, clear explanation.
    - Decrease: garbled text, OCR artifacts, visual ambiguity you cannot see,
      multiple_correct with missed answers.
- Garbled text: note in explanation as
  [NOTE: possible OCR error in question text — "X" may be "Y"]. Do NOT fix it.
- Confidence ranges: 0.95-1.0 clear, 0.80-0.94 minor uncertainty,
  0.50-0.79 significant uncertainty (human must verify), 0.0-0.49 unreliable.
- The _verification summary lists: structural fixes, flagged answers,
  confidence adjustments, garbled text detected, and a one-line summary.

[Reference: skills/verify-extracted-questions/SKILL.md for the full specification]
```

The user message contains the extracted questions serialized as JSON using the
`verificationInput` DTO format (§7.3.3) — same field names as the extraction
output (`question`, `choices`, `answers`, etc.).

#### 7.3.3 Schema & DTOs (`schema.go`)

Same rationale as extractor: the pipeline `VerifiedQuestion` has no lowercase
`json` tags, so define JSON-tagged DTOs and map. The verifier both **consumes**
and **produces** the skill format — the input is serialized from
`pipeline.ExtractedQuestion` (converted to the skill's `questionDTO` shape), and
the output is the skill's verified format with the `_verification` summary.

```go
package verifier

import "encoding/json"

// answerDTO mirrors the extraction output's answer shape.
type answerDTO struct {
    ID    string `json:"id"`
    Value string `json:"value"`
}

// questionDTO is the input shape — the questions serialized for the model.
type questionDTO struct {
    Number          int         `json:"number"`
    Question        string      `json:"question"`
    MultipleCorrect bool        `json:"multiple_correct"`
    Choices         []string    `json:"choices"`
    Answers         []answerDTO `json:"answers"`
    Confidence      float64     `json:"confidence"`
    Explanation     string      `json:"explanation"`
    Tags            []string    `json:"tags,omitempty"`
}

// verificationInput is the top-level object sent to the model.
type verificationInput struct {
    Questions []questionDTO `json:"questions"`
}

// verifiedQuestionDTO is the output shape — what the model returns per question.
type verifiedQuestionDTO struct {
    Number          int         `json:"number"`
    Question        string      `json:"question"`
    MultipleCorrect bool        `json:"multiple_correct"`
    Choices         []string    `json:"choices"`
    Answers         []answerDTO `json:"answers"`
    Confidence      float64     `json:"confidence"`  // adjusted by verifier
    Explanation     string      `json:"explanation"` // may include [VERIFICATION FLAG]
    Tags            []string    `json:"tags,omitempty"`
}

// verificationResponse is the top-level object the model returns.
type verificationResponse struct {
    Verification json.RawMessage       `json:"_verification"` // raw, persisted as Report
    Questions    []verifiedQuestionDTO `json:"questions"`
}
```

**Input conversion (`fromPipeline`):** the pipeline passes
`[]pipeline.ExtractedQuestion`. The verifier converts each to the skill's
`questionDTO` shape — flattening `Choices` (`[]pipeline.Answer` with ID+Text)
back to plain strings, and converting `Answers` from `{ID, Text}` to `{id, value}`:

```go
func fromPipeline(questions []pipeline.ExtractedQuestion) verificationInput {
    out := verificationInput{Questions: make([]questionDTO, len(questions))}
    for i, q := range questions {
        choices := make([]string, len(q.Choices))
        for j, c := range q.Choices {
            choices[j] = c.Text // strip the synthesized ID; skill uses bare strings
        }
        answers := make([]answerDTO, len(q.Answers))
        for j, a := range q.Answers {
            answers[j] = answerDTO{ID: a.ID, Value: a.Text}
        }
        out.Questions[i] = questionDTO{
            Number:          q.Number,
            Question:        q.Text, // pipeline "Text" → skill "question"
            MultipleCorrect: q.MultipleCorrect,
            Choices:         choices,
            Answers:         answers,
            Confidence:      q.Confidence,
            Tags:            q.Tags,
            // Explanation left empty on input — the extractor does not carry it.
        }
    }
    return out
}
```

**Output mapping (`toPipeline`):** the verifier returns the full question list.
We map by **position** (index = position in the array, matching the input order).
Only `Confidence` and `Explanation` are consumed into the pipeline's
`VerifiedQuestion`; the rest is for the model's internal consistency and is
captured in the raw `Report`.

```go
func toPipeline(r verificationResponse, inputCount int) pipeline.VerifyResult {
    out := pipeline.VerifyResult{Report: r.Verification}
    for i, q := range r.Questions {
        if i >= inputCount {
            break // safety: don't map beyond input range
        }
        out.Summary.Results = append(out.Summary.Results, pipeline.VerifiedQuestion{
            Index:       i,             // 0-based position
            Confidence:  q.Confidence,  // adjusted confidence
            Explanation: q.Explanation, // includes verification flags
        })
    }
    return out
}
```

`Report` is the **raw** `_verification` JSON from the model response, persisted
verbatim to `images.verification_report` by the pipeline (`pipeline.go:218`).
Apply the same defensive fence-stripping as the extractor before parsing.

#### Error handling

The verifier has **no** retry path. Any error → return
`(pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err))`. The pipeline logs a
warning and leaves questions in `moderation` (`pipeline.go:206-207`). On
malformed JSON, return a Go error (treated identically to a transport error —
best-effort skip).

The `Index` field is assigned by **position** in the returned questions array
(matching the input order). If the model returns fewer questions than were sent,
only those positions are mapped; the pipeline's existing bounds-check
(`pipeline.go:210`: `vq.Index >= 0 && vq.Index < len(newQs)`) still applies. If
the model returns more questions than were sent, the `i >= inputCount` guard in
`toPipeline` prevents mapping beyond the input range.

#### Tests (`verifier_test.go`)

`httptest.NewServer` returning a chat completion. Cases:

| Case | Server returns | Assert |
|---|---|---|
| Happy path | `{"_verification":{"timestamp":"…","questions_verified":2,"summary":"…"},"questions":[{"number":1,"question":"…","confidence":0.9,"explanation":"ok",...},{"number":2,"question":"…","confidence":0.4,"explanation":"[VERIFICATION FLAG]…",...}]}` | `Summary.Results` length 2; `Index` 0 and 1 (by position); `Confidence` and `Explanation` mapped; `Report` is the raw `_verification` bytes |
| Fewer questions returned | model returns 1 question for a 2-question input | `Summary.Results` length 1; only index 0 mapped |
| Transport error (500) | 500 | non-nil Go error; zero-value result |
| Malformed JSON | prose content | non-nil Go error |

Verify the request body contains a text-only user message (no `image_url` part)
and `response_format.type == "json_object"`.

---

### 7.4 Embedder (`internal/ai/embedder/`)

OpenAI embeddings API.

#### Constructor & method

```go
func New(cfg config.EmbedderConfig, log *slog.Logger) *Embedder
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error)
```

`New` builds the client: `oai.NewClient(cfg.BaseURL, cfg.APIKey, embedderDefaultTimeout)`
(§5.5 — 30s default).

#### Request shape

Call `client.Embeddings.New(ctx, openai.EmbeddingNewParams{
    Model: openai.EmbeddingModel(cfg.Model), // or the String form per SDK version
    Input: openai.EmbeddingNewParamsInputUnion{...: text},
})`.

Pin the exact union field against the installed SDK version at implementation
time; the wire requirement is `{"model":"<cfg.Model>","input":"<text>"}` to
`/embeddings`.

#### Response handling

The SDK returns `[]float64`. Cast to `[]float32`:

```go
emb := resp.Data[0].Embedding // []float64
out := make([]float32, len(emb))
for i, v := range emb { out[i] = float32(v) }
return out, nil
```

Assert `len(out) == cfg.Dim` in tests (1536 for `text-embedding-3-small`). A
dimension mismatch in production is logged but not fatal (the pipeline just
passes whatever it got to `FindSemantic`; pgvector will reject a wrong-dim
vector at query time). For robustness, if `len(out) != cfg.Dim && cfg.Dim > 0`,
return an error so the pipeline's best-effort path skips semantic dedup cleanly.

#### Error handling

Any error → `return nil, fmt.Errorf("embed: %w", err)`. The pipeline logs a
warning and skips semantic dedup for that question (`pipeline.go:155-156`).
Empty input text: the model will reject it; treat as a normal error (do not
special-case — let the API or the `len(text)==0` guard return an error).

#### Tests (`embedder_test.go`)

`httptest.NewServer` returning `/embeddings` JSON. Cases:

| Case | Server returns | Assert |
|---|---|---|
| Happy path | `{"data":[{"embedding":[0.1, 0.2, … 1536 values]}]}` | `[]float32` length 1536; values cast correctly |
| Transport error (500) | 500 | non-nil Go error |
| Empty text | call `Embed(ctx, "")` | non-nil Go error |
| Dimension mismatch guard | 10-dim vector with `cfg.Dim=1536` | non-nil Go error |

The happy-path fixture can synthesize a 1536-element array in the test rather
than hand-writing it.

---

## 8. Wiring

### 8.1 `internal/app/wire.go`

Replace the `TODO(plan-3)` block (`wire.go:51-59`) with real construction.
`Build` stays side-effect-free except for one lifecycle call: `vips.Startup`
must run before the enhancer is constructed (and before any govips operation).
`Build` constructs the `WorkerPool` but does **not** call `Start` (that is
`main.go`'s job, so `Build` remains testable and the start point is explicit).

Add a logger at the top of `Build`:

```go
log := slog.Default()
```

Construction block:

```go
vips.Startup(nil) // initialize libvips before any image processing

enhancer  := enhancer.New(log)
extractor := extractor.New(cfg.AI.Vision, log)
verifier  := verifier.New(cfg.AI.Reviewer, log)
embedder  := embedder.New(cfg.AI.Embedder, log)

pip := pipeline.NewPipeline(imageRepo, questionRepo, jobQueue,
    enhancer, extractor, verifier, embedder, cfg.Pipeline, log)

wp := pipeline.NewWorkerPool(jobQueue, pip,
    cfg.Workers, cfg.Pipeline, cfg.Postgres.DSN, log)
```

Add `WorkerPool: wp` to the returned `App` struct (the field already exists at
`wire.go:25`). New imports: `internal/ai/enhancer`, `internal/ai/extractor`,
`internal/ai/verifier`, `internal/ai/embedder`, and
`github.com/davidbyttow/govips/v2/vips` (for `vips.Startup`).

### 8.2 `cmd/coeus/main.go`

Add exactly one line after `app.Build` returns and before the HTTP server
starts (after `defer application.Close()` at `main.go:36`):

```go
application.WorkerPool.Start(ctx)
```

Place it after `application.Close()` is deferred but before `slog.Info("coeus
started", ...)`. This spawns the reaper and N workers; they begin claiming
pending jobs immediately.

### 8.3 `App.Close()`

Stop workers **before** shutting down libvips, and shut down libvips **before**
closing the DB pool — so in-flight jobs finish (or release) while the DB is
still reachable, and govips finishes any pending image work before libvips
itself is torn down:

```go
func (a *App) Close() {
    if a.WorkerPool != nil {
        a.WorkerPool.Stop()
    }
    vips.Shutdown() // release libvips resources
    if a.Pool != nil {
        a.Pool.Close()
    }
}
```

`WorkerPool.Stop()` (`worker.go:85`) cancels the worker context and waits on the
`WaitGroup`, so all goroutines exit before `vips.Shutdown()` and `Pool.Close()`
run.

### 8.4 Shutdown sequence on SIGINT/SIGTERM

The existing `main.go` sequence is already correct; Plan 3 only adds the worker
dimension. On signal (`main.go:56`):

1. `httpServer.Shutdown(shutdownCtx)` — stop accepting HTTP (existing, `main.go:62`).
2. `cancel()` — cancel the root `ctx` (`main.go:65`). Workers observe
   `ctx.Done()` and stop claiming; in-flight `Pipeline.Run` calls return their
   error **without** completing or failing the job (`pipeline.go:63-68`), so the
   reaper reclaims them on next startup.
3. `defer application.Close()` runs → `WorkerPool.Stop()` (waits for goroutines)
   → `vips.Shutdown()` (releases libvips) → `Pool.Close()`.

This ordering means: HTTP goes down first (no new uploads), then workers drain,
then libvips shuts down, then the DB closes. No code change needed beyond §8.3
— the order is already correct.

---

## 9. N+1 query fix

### 9.1 The problem

`ImageHandler.List` (`internal/httpapi/handlers/images.go:92`) calls
`h.jobs.FindByImageID` once **per image** in a loop (`images.go:111`). A session
with 50 images issues 50 job queries + 1 image-list query = 51 queries per
`GET /sessions/:id/images`.

### 9.2 The fix

Add a single bulk method to the `JobQueue` interface and use it in the handler.

**Interface** (`internal/storage/ports.go`, add to `JobQueue`):

```go
FindJobStatusesBySession(ctx context.Context, sessionID string) (map[string]string, error)
```

Returns `imageID → status`. Jobs without a row (shouldn't happen — every image
gets a job at upload) are simply absent from the map; the handler treats absence
as `"unknown"` (the existing default, `images.go:110`).

**Implementation** (`internal/storage/postgres/job_queue.go`):

```go
func (q *JobQueue) FindJobStatusesBySession(ctx context.Context, sessionID string) (map[string]string, error) {
    rows, err := q.pool.Query(ctx, `
        SELECT DISTINCT ON (image_id) image_id, status
        FROM jobs
        WHERE session_id = $1
        ORDER BY image_id, queued_at DESC
    `, sessionID)
    if err != nil {
        return nil, fmt.Errorf("find job statuses by session: %w", err)
    }
    defer rows.Close()

    out := make(map[string]string)
    for rows.Next() {
        var imageID, status string
        if err := rows.Scan(&imageID, &status); err != nil {
            return nil, fmt.Errorf("scan job status: %w", err)
        }
        out[imageID] = status
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("job status rows: %w", err)
    }
    return out, nil
}
```

> **Note on duplicates:** `jobs` has no uniqueness constraint on
> `(session_id, image_id)` — a re-enqueue could in theory produce two rows.
> `DISTINCT ON (image_id) ... ORDER BY image_id, queued_at DESC` returns exactly
> one row per `image_id` (the newest by `queued_at`), making the map
> deterministic. This mirrors the existing `FindByImageID` which uses
> `ORDER BY queued_at DESC LIMIT 1` (`job_queue.go:140`).

**Index:** `idx_jobs_status_queued ON jobs(status, queued_at)` exists
(`0002_core.sql:78`) but there is no index on `session_id`. For typical session
sizes (single-digit to low-hundreds of images) a sequential scan of `jobs` is
fine. If profiling later shows this query on a hot path, add
`CREATE INDEX idx_jobs_session ON jobs(session_id);` — **not** required for
Plan 3; listed as an optional follow-up.

**Handler rewrite** (`internal/httpapi/handlers/images.go`, `List`):

```go
ctx := c.Request.Context()

images, err := h.images.ListBySession(ctx, session.ID)
if err != nil {
    c.JSON(http.StatusInternalServerError, errorResponse(err))
    return
}

statuses, err := h.jobs.FindJobStatusesBySession(ctx, session.ID)
if err != nil {
    slog.Warn("find job statuses for session failed", "session", session.ID, "error", err)
    statuses = make(map[string]string) // degrade gracefully — all "unknown"
}

data := make([]dto.ImageResponse, 0, len(images))
for _, img := range images {
    jobStatus := statuses[img.ID]
    if jobStatus == "" {
        jobStatus = "unknown"
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
```

This reduces the handler to **2 queries** regardless of image count.

### 9.3 Touch points

| File | Change |
|---|---|
| `internal/storage/ports.go` | Add `FindJobStatusesBySession` to `JobQueue` interface |
| `internal/storage/postgres/job_queue.go` | Implement the method |
| `internal/storage/postgres/job_queue_test.go` | Test: insert jobs for 2 images in a session + 1 in another; assert map has exactly the session's two |
| `internal/httpapi/handlers/images.go` | Rewrite `List` to use the bulk call |
| `internal/httpapi/handlers/images_test.go` | Add `FindJobStatusesBySession` to `fakeJobQueueForImages`; update `TestImageHandler_List` to seed the map |

The interface addition is a breaking change for every `storage.JobQueue`
implementation — the fakes in `images_test.go:63` and `pipeline_test.go` (if any
embed `storage.JobQueue`) must be updated. Grep for `FindByImageID` to find all
implementations; the compiler will flag any stragglers.

---

## 10. Testing strategy

### 10.1 Unit tests (no live API calls)

Every AI client is tested in isolation with `httptest.NewServer` and fixture
JSON. The server URL is passed as the client's `base_url`, so the client hits
the test server exactly as it would hit a real provider. The enhancer is the
exception: it uses govips (no HTTP), so its tests exercise real image bytes
through libvips directly.

| Package | Harness | Key cases (see §7 for full lists) |
|---|---|---|
| `enhancer` | govips (CGO/libvips), real synthesized image bytes | JPEG/PNG/WebP round-trip, invalid bytes, unsupported MIME |
| `extractor` | httptest server | happy (with tags), unreadable, no-questions, partial, 500, malformed-JSON, timeout, base-URL |
| `verifier` | httptest server | happy, fewer-questions-returned, 500, malformed-JSON |
| `embedder` | httptest server | happy (1536 dims, `[]float32`), 500, empty text, dim mismatch |

**Shared helper:** create a small unexported helper in each `_test.go` (or a
shared `internal/ai/oai/oai_test.go` if duplication grows) that builds an
`httptest.NewServer` returning a canned chat-completion or embeddings body. Keep
the fixtures as `const` strings in the test file.

### 10.2 Config tests (`internal/config/config_test.go`)

Update existing tests that set `COEUS_AI_KIMI_API_KEY` /
`COEUS_AI_DEEPSEEK_API_KEY` to the new names. Add:

- `TestEnvOverridesYAML` variant: set `COEUS_AI_VISION_API_KEY`,
  `COEUS_AI_REVIEWER_API_KEY`, `COEUS_AI_EMBEDDER_API_KEY` and assert they land
  on `cfg.AI.Vision.APIKey`, etc.
- `TestValidate_MissingSecrets`: add subtests asserting `Validate()` rejects
  empty `ai.vision.api_key`, `ai.reviewer.api_key`, `ai.embedder.api_key` with
  the expected substring. (The existing `TestValidate_WithSecrets` must now set
  all three keys or it will start failing.)

### 10.3 N+1 fix tests

- `job_queue_test.go`: integration test against Testcontainers Postgres — seed
  two sessions with jobs, assert the map returns exactly the right session's
  image→status pairs.
- `images_test.go`: update `fakeJobQueueForImages` to implement the new method;
  update `TestImageHandler_List` to seed `statuses` and assert the handler reads
  from the map (one call, not per-image).

### 10.4 Existing pipeline tests

`pipeline_test.go` uses fakes for all four ports. It does **not** construct the
real clients, so it continues to pass unchanged. The fakes are unaffected by the
real-client work.

### 10.5 Build verification

After wiring, `go build ./...` must succeed, `go vet ./...` must be clean, and
the interface-satisfaction assertions (`var _ pipeline.AI<Name> = ...`) must
compile. Run `go test ./...` — all existing tests must still pass (config tests
updated per §10.2).

### 10.6 Manual smoke test (documented, not automated)

Not part of the automated suite. Document the steps in the plan (not enforced by
CI):

1. Export real keys: `COEUS_AI_VISION_API_KEY`, `COEUS_AI_REVIEWER_API_KEY`,
   `COEUS_AI_EMBEDDER_API_KEY`, plus `base_url` overrides for Kimi/DeepSeek.
2. Start the app, create a session, upload a sample exam image.
3. Watch logs: worker claims the job, pipeline runs enhance → extract → verify →
   embed.
4. `GET /sessions/:id/images` shows `job_status: "done"`.
5. Query `questions` / `session_questions` to confirm extracted questions, the
   `verification_report` JSON on the image, and embeddings populated.

---

## 11. Risks and assumptions

| # | Risk / assumption | Mitigation |
|---|---|---|
| R1 | Kimi/DeepSeek exact `image_url` multimodal + `json_object` behavior is assumed, not yet confirmed against live APIs. | Start with `ResponseFormatJSONObject`; defensive fence-stripping; smoke test (§10.6) before tightening to `JSONSchema`. |
| R2 | The openai-go union types for content parts / embedding input may differ across SDK patch versions. | Pin a version in `go.mod`; confirm exact field names against the installed version's generated types during implementation; keep request-building code isolated to the client package. |
| R3 | Vision extraction latency can exceed the 90s default timeout for large/busy models. | `VisionConfig.Timeout` is configurable; pipeline retries on transport timeout; `ExtractMaxAttempts` bounds total wait. |
| R4 | Embeddings returned as `[]float64` must cast to `[]float32` for pgvector. | Explicit cast + dimension check; mismatch returns an error so semantic dedup is skipped rather than corrupting the index. |
| R5 | Renaming config fields/env vars is a breaking change for any deployed instance. | This is a pre-production system; the rename happens before any real deployment. Document the env-var rename in the plan. |
| R6 | `jobs` has no uniqueness on `(session_id, image_id)`. | The bulk query uses last-write-wins (optionally `ORDER BY queued_at DESC`), mirroring `FindByImageID`. |
| R7 | The enhancer re-encodes lossy (JPEG q92) — repeated enhance cycles would degrade. | Enhance runs once per image (pipeline persists enhanced bytes at `images.enhanced_data`); no re-enhancement path exists. |
| A1 | Providers honor OpenAI-compatible `/v1/chat/completions` and `/v1/embeddings` at the configured `base_url`. | Assumed true for Moonshot (Kimi) and DeepSeek by their public docs. |
| A2 | `text-embedding-3-small` returns 1536 dimensions. | Matches the OpenAI spec and `embedder.dim` default. |

---

## 12. Deferred items (explicit)

| Deferred item | Reason | Future home |
|---|---|---|
| `CleanBytes` (null out `image_data`/`enhanced_data` once all derived questions are verified) | Tied to the expert-moderation workflow that doesn't exist yet. `ImageRepo.CleanBytes` (`ports.go:51`) already exists; pipeline just doesn't call it. | Moderation plan |
| Expert moderation endpoints (`PATCH /questions/:id`) | Requires the expert UI/role workflow. | Moderation plan |
| `ResponseFormatJSONObject` → `ResponseFormatJSONSchema` | Wait for live-API confirmation; then pass the invopop-generated schema as the structured-output constraint. | Follow-up after smoke test |
| Per-provider rate limiting / circuit breaking | Not needed at current volume. | Future plan |
| Live contract tests | Brittle and provider-dependent. Manual smoke test covers confidence. | N/A |

---

## 13. Implementation order (suggested)

A dependency-respecting order for an implementer executing this spec:

> **Prerequisite:** install libvips before any govips code will build:
> - **macOS:** `brew install vips pkg-config`
> - **Linux:** `apt install libvips-dev`
>
> CGO must be enabled (`CGO_ENABLED=1`, the default for native builds).

1. **Config** (§5) — rename structs, env vars, YAML; add validation. Update
   `config_test.go`. `go test ./internal/config`.
2. **`oai` wrapper** (§4.2) — tiny, unexported, no tests of its own.
3. **Enhancer** (§7.1) — govips; requires libvips installed. Fastest to validate
   end-to-end (no network calls).
4. **Embedder** (§7.4) — simplest LLM client; validates the `oai` + httptest
   pattern.
5. **Verifier** (§7.3) — text-only LLM; reuses the pattern.
6. **Extractor** (§7.2) — most complex (multimodal + error taxonomy); build last.
7. **N+1 fix** (§9) — independent of the clients; can be done in parallel with
   3–6.
8. **Wiring** (§8) — construct everything in `wire.go` (including `vips.Startup`),
   add `Start` in `main.go`, update `App.Close` (including `vips.Shutdown`).
9. **Full build + test** (§10.5) — `go build ./...`, `go vet ./...`,
   `go test ./...`.
10. **Smoke test** (§10.6) — manual, with real keys.

Each step is independently testable; steps 3–7 and 9 can be parallelized across
subagents if desired (they touch disjoint files).
