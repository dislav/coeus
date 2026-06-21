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
- Unit tests for every client using `httptest.NewServer` + fixture JSON (no live
  API calls). Config tests for renamed env vars and validation.

### 3.2 Out of scope (deferred)

| Item | Deferred to |
|------|-------------|
| `CleanBytes` (delete image bytes after all questions verified) | Future moderation plan |
| Expert moderation endpoints (e.g. `PATCH /questions/:id`) | Future moderation plan |
| AI-assigned tags during extraction (`Tags` field exists, stays empty) | Future plan |
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
| `github.com/disintegration/imaging` | Enhancer — `Sharpen`, `AdjustContrast`, `Gamma`, decode/encode for JPEG/PNG |
| `github.com/invopop/jsonschema` | Generate JSON Schema from Go structs — fed into the prompt now, into `ResponseFormatJSONSchema` later |

Run `go get github.com/openai/openai-go github.com/disintegration/imaging github.com/invopop/jsonschema` then `go mod tidy`.

**Why official `openai/openai-go` and not `sashabaranov/go-openai`:** the
sashabaranov client is stale (lacks current structured-output and embeddings
APIs). The official SDK is maintained, supports `option.WithBaseURL` for
OpenAI-compatible providers (Kimi/DeepSeek), and is the path forward for
`ResponseFormatJSONSchema`.

> **Note on WebP:** `disintegration/imaging` decodes JPEG/PNG natively. WebP
> input requires `golang.org/x/image/webp`, which is already imported for decode
> registration in `internal/httpapi/handlers/images.go:13`. The enhancer must
> also blank-import `golang.org/x/image/webp` (and `vp8l` is not needed) so
> `imaging.Decode` can read WebP. Re-encode WebP output via
> `golang.org/x/image/webp` encoder if the source MIME is `image/webp`;
> otherwise encode to the source MIME.

---

## 7. Client implementations

### 7.1 Enhancer (`internal/ai/enhancer/`)

Deterministic Go image processing. **No AI call.**

#### Constructor & method

```go
func New(log *slog.Logger) *Enhancer
func (e *Enhancer) Enhance(ctx context.Context, original []byte, mime string) ([]byte, error)
```

#### Behavior

1. Decode `original` with `imaging.Decode(bytes.NewReader(original), imaging.AutoOrientation(true))`. Honors `ctx` via the decode (decode is CPU-bound and fast; `ctx.Err()` is checked before decoding).
2. Apply, in order:
   - `imaging.AdjustContrast(img, +15)` — +15% contrast.
   - `imaging.Gamma(img, 1.15)` — slight brightening of midtones (gamma > 1.0 brightens in `disintegration/imaging`).
   - `imaging.Sharpen(img, 0.5)` — mild sharpen.
3. Encode back to the **same MIME** the caller passed:
   - `image/jpeg` → `imaging.JPEG(buf, img, 92)`
   - `image/png` → `imaging.PNG(buf, img)`
   - `image/webp` → encode via `golang.org/x/image/webp` encoder (quality 80)
   - any other MIME → return `(nil, error)` (pipeline falls back to original).
4. Return encoded bytes.

#### Error handling & fallback

The pipeline already treats enhancer failure as best-effort
(`pipeline.go:95-97`): on error it logs a warning and continues with the
original bytes. Therefore `Enhance` returns `(nil, err)` for any decode/encode
failure or unsupported MIME; it does **not** attempt its own fallback. Return
`(nil, fmt.Errorf("enhance: %w", err))` with context.

If `len(encoded) == 0` the pipeline keeps the original (the `len(enhanced) > 0`
guard at `pipeline.go:97`).

#### Tests (`enhancer_test.go`)

Pure Go, no `httptest`. Cases:

| Case | Assertion |
|---|---|
| JPEG round-trip | no error; decoded dimensions preserved; output bytes differ from input; output decodes as JPEG |
| PNG round-trip | same, decodes as PNG |
| WebP round-trip | same, decodes as WebP |
| Invalid bytes | error (e.g. `[]byte("not an image")`) |
| Unsupported MIME (`"application/pdf"`) | error |

Use `image/png` + `image.NewRGBA` to synthesize a small input image (pattern
already used in `images_test.go:86 validPNG`). Verify dimensions with
`image.DecodeConfig`.

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

System prompt (store as a `const`):

> You are an exam-image OCR and parsing engine. You receive a photo of a quiz or
> test worksheet. Extract every visible question into the JSON object described
> below. Output ONLY a JSON object — no prose, no markdown fences.
>
> Each question has:
> - `number` (int) — the question number as printed (1-based). Use 0 if unnumbered.
> - `text` (string) — the full question text, verbatim.
> - `choices` (array of `{id, text}`) — every answer option. `id` is the printed
>   label ("A", "B", "1", "2", "i", "ii", …); `text` is the option text.
> - `answers` (array of `{id, text}`) — the CORRECT answers only, each with its
>   label and text.
> - `multiple_correct` (bool) — true if more than one answer is correct.
> - `confidence` (float 0.0–1.0) — your confidence this question was parsed
>   correctly.
> - `tags` (array of strings) — subject tags (may be empty).
>
> If you cannot process the image, set `error` instead of `questions`:
> - `{"code":"unreadable_image","detail":"…"}` — image too blurry, dark, or corrupt.
> - `{"code":"no_questions_found","detail":"…"}` — image readable but contains no questions.
> - `{"code":"partial_extraction","detail":"…"}` — some questions parsed, some not (still include the ones you got in `questions`).
>
> Never invent answers. If the correct answer is not visible, leave `answers`
> empty and lower `confidence`.

User prompt (text part): a short instruction plus the rendered JSON schema for
reference:

> Extract all questions from this exam image. Respond as a JSON object matching
> this schema:\n\n<schema>

The `<schema>` is the JSON generated by `invopop/jsonschema.Reflector` from
`extractionResponse` (§7.2.3). This anchors the model on field names without
requiring `ResponseFormatJSONSchema`.

#### 7.2.3 Schema & DTOs (`schema.go`)

**Key design point:** the `pipeline.ExtractedQuestion` / `pipeline.Answer` /
`pipeline.ExtractionError` types do **not** carry lowercase `json` tags (only
`Confidence` and `Tags` do). Reusing them directly as the wire format would
force the model to emit capitalized field names (`"Number"`, `"Text"`, …),
which models will not do reliably. Therefore the extractor package defines its
own JSON-tagged DTOs, generates the schema from them, and maps them to the
pipeline types. This keeps the pipeline contract clean and the wire format
predictable.

```go
package extractor

type choiceDTO struct {
    ID   string `json:"id"`
    Text string `json:"text"`
}

type questionDTO struct {
    Number          int         `json:"number"`
    Text            string      `json:"text"`
    Choices         []choiceDTO `json:"choices"`
    Answers         []choiceDTO `json:"answers"`
    MultipleCorrect bool        `json:"multiple_correct"`
    Confidence      float64     `json:"confidence"`
    Tags            []string    `json:"tags,omitempty"`
}

type extractionErrorDTO struct {
    Code   string `json:"code"`
    Detail string `json:"detail"`
}

type extractionResponse struct {
    Questions []questionDTO        `json:"questions,omitempty"`
    Error     *extractionErrorDTO  `json:"error,omitempty"`
}
```

Mapping to pipeline types:

```go
func toPipeline(r extractionResponse) pipeline.ExtractResult {
    res := pipeline.ExtractResult{}
    if r.Error != nil {
        res.Error = &pipeline.ExtractionError{Code: r.Error.Code, Detail: r.Error.Detail}
    }
    res.Questions = make([]pipeline.ExtractedQuestion, len(r.Questions))
    for i, q := range r.Questions {
        res.Questions[i] = pipeline.ExtractedQuestion{
            Number:          q.Number,
            Text:            q.Text,
            Choices:         toAnswers(q.Choices),
            Answers:         toAnswers(q.Answers),
            MultipleCorrect: q.MultipleCorrect,
            Confidence:      q.Confidence,
            Tags:            q.Tags,
        }
    }
    return res
}

func toAnswers(cs []choiceDTO) []pipeline.Answer {
    out := make([]pipeline.Answer, len(cs))
    for i, c := range cs {
        out[i] = pipeline.Answer{ID: c.ID, Text: c.Text}
    }
    return out
}
```

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
| Happy path | completion with `questions:[{number:1,text:"…",choices:[…],answers:[…],multiple_correct:false,confidence:0.9}]` | `result.Questions` length 1; fields mapped to `pipeline.ExtractedQuestion`; nil Go error; nil `result.Error` |
| Unreadable image | `{"error":{"code":"unreadable_image","detail":"blurry"}}` | nil Go error; `result.Error.Code == "unreadable_image"` |
| No questions | `{"error":{"code":"no_questions_found",...}}` | nil Go error; `result.Error.Code == "no_questions_found"` |
| Partial extraction | `{"questions":[...], "error":{"code":"partial_extraction",...}}` | nil Go error; questions present; `result.Error.Code == "partial_extraction"` |
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
to read. Reuse `questionDTO`-shaped tags (define a local marshaler or a small
struct) so the field names are consistent with the extraction vocabulary.

#### 7.3.2 Prompt (`prompt.go`)

System prompt:

> You are an answer-checking reviewer. You receive a JSON array of questions,
> each with its text, choices, and the correct answer(s) proposed by an
> extraction model. For EACH question, independently judge whether the proposed
> answer is correct.
>
> Output ONLY a JSON object: `{"results":[{"index":0,"confidence":0.93,"explanation":"…"}, …]}`.
>
> Rules:
> - `index` is the 0-based position of the question in the input array.
> - `confidence` (0.0–1.0) is your confidence the proposed answer is correct.
> - `explanation` is a one-sentence justification. Empty string if fully confident.
> - Do NOT modify the question text, choices, or answers. You only score them.
> - If you cannot judge (question unclear, domain outside your knowledge), set
>   `confidence` to 0 and explain in `explanation`.

#### 7.3.3 Schema & DTOs (`schema.go`)

Same rationale as extractor: the pipeline `VerifiedQuestion` has no lowercase
`json` tags, so define a JSON-tagged DTO and map.

```go
type verifiedQuestionDTO struct {
    Index       int    `json:"index"`
    Confidence  float64 `json:"confidence"`
    Explanation string `json:"explanation"`
}

type verificationResponse struct {
    Results []verifiedQuestionDTO `json:"results"`
}
```

Mapping:

```go
func toPipeline(r verificationResponse, raw json.RawMessage) pipeline.VerifyResult {
    out := pipeline.VerifyResult{Report: raw}
    out.Summary.Results = make([]pipeline.VerifiedQuestion, len(r.Results))
    for i, q := range r.Results {
        out.Summary.Results[i] = pipeline.VerifiedQuestion{
            Index:       q.Index,
            Confidence:  q.Confidence,
            Explanation: q.Explanation,
        }
    }
    return out
}
```

`Report` is the **raw** model JSON (the full `verificationResponse` bytes),
persisted verbatim to `images.verification_report` by the pipeline
(`pipeline.go:218`). Apply the same defensive fence-stripping as the extractor
before parsing.

#### Error handling

The verifier has **no** retry path. Any error → return
`(pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err))`. The pipeline logs a
warning and leaves questions in `moderation` (`pipeline.go:206-207`). On
malformed JSON, return a Go error (treated identically to a transport error —
best-effort skip).

The `Index` field is trusted but the pipeline bounds-checks it
(`pipeline.go:210`: `vq.Index >= 0 && vq.Index < len(newQs)`), so an out-of-range
index is safely ignored. Do not silently clamp in the client — preserve what the
model returned.

#### Tests (`verifier_test.go`)

`httptest.NewServer` returning a chat completion. Cases:

| Case | Server returns | Assert |
|---|---|---|
| Happy path | `{"results":[{"index":0,"confidence":0.9,"explanation":"ok"},{"index":1,"confidence":0.4,"explanation":"unsure"}]}` | `Summary.Results` length 2; fields mapped; `Report` is the raw bytes |
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
`Build` stays side-effect-free — it constructs the `WorkerPool` but does **not**
call `Start` (that is `main.go`'s job, so `Build` remains testable and the
start point is explicit).

Add a logger at the top of `Build`:

```go
log := slog.Default()
```

Construction block:

```go
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
`internal/ai/verifier`, `internal/ai/embedder`.

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

Stop workers **before** closing the pool, so in-flight jobs finish (or release)
while the DB is still reachable:

```go
func (a *App) Close() {
    if a.WorkerPool != nil {
        a.WorkerPool.Stop()
    }
    if a.Pool != nil {
        a.Pool.Close()
    }
}
```

`WorkerPool.Stop()` (`worker.go:85`) cancels the worker context and waits on the
`WaitGroup`, so all goroutines exit before `Pool.Close()` runs.

### 8.4 Shutdown sequence on SIGINT/SIGTERM

The existing `main.go` sequence is already correct; Plan 3 only adds the worker
dimension. On signal (`main.go:56`):

1. `httpServer.Shutdown(shutdownCtx)` — stop accepting HTTP (existing, `main.go:62`).
2. `cancel()` — cancel the root `ctx` (`main.go:65`). Workers observe
   `ctx.Done()` and stop claiming; in-flight `Pipeline.Run` calls return their
   error **without** completing or failing the job (`pipeline.go:63-68`), so the
   reaper reclaims them on next startup.
3. `defer application.Close()` runs → `WorkerPool.Stop()` (waits for goroutines)
   → `Pool.Close()`.

This ordering means: HTTP goes down first (no new uploads), then workers drain,
then the DB closes. No code change needed beyond §8.3 — the order is already
correct.

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
the test server exactly as it would hit a real provider.

| Package | Harness | Key cases (see §7 for full lists) |
|---|---|---|
| `enhancer` | pure Go, real synthesized image bytes | JPEG/PNG/WebP round-trip, invalid bytes, unsupported MIME |
| `extractor` | httptest server | happy, unreadable, no-questions, partial, 500, malformed-JSON, timeout, base-URL |
| `verifier` | httptest server | happy, 500, malformed-JSON |
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
| Populating `ExtractedQuestion.Tags` / `Question.Tags` from the model | Schema supports it; prompts request it; but downstream tag-driven features aren't built. Field stays empty in Plan 3. | Future plan |
| `ResponseFormatJSONObject` → `ResponseFormatJSONSchema` | Wait for live-API confirmation; then pass the invopop-generated schema as the structured-output constraint. | Follow-up after smoke test |
| Per-provider rate limiting / circuit breaking | Not needed at current volume. | Future plan |
| Live contract tests | Brittle and provider-dependent. Manual smoke test covers confidence. | N/A |

---

## 13. Implementation order (suggested)

A dependency-respecting order for an implementer executing this spec:

1. **Config** (§5) — rename structs, env vars, YAML; add validation. Update
   `config_test.go`. `go test ./internal/config`.
2. **`oai` wrapper** (§4.2) — tiny, unexported, no tests of its own.
3. **Enhancer** (§7.1) — no external calls; fastest to validate end-to-end.
4. **Embedder** (§7.4) — simplest LLM client; validates the `oai` + httptest
   pattern.
5. **Verifier** (§7.3) — text-only LLM; reuses the pattern.
6. **Extractor** (§7.2) — most complex (multimodal + error taxonomy); build last.
7. **N+1 fix** (§9) — independent of the clients; can be done in parallel with
   3–6.
8. **Wiring** (§8) — construct everything in `wire.go`, add `Start` in
   `main.go`, update `App.Close`.
9. **Full build + test** (§10.5) — `go build ./...`, `go vet ./...`,
   `go test ./...`.
10. **Smoke test** (§10.6) — manual, with real keys.

Each step is independently testable; steps 3–7 and 9 can be parallelized across
subagents if desired (they touch disjoint files).
