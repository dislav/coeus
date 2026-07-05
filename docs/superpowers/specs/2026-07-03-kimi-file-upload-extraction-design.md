# Kimi file-upload extraction (Approach A) — Design

- **Status:** Approved (design)
- **Date:** 2026-07-03
- **Area:** `internal/ai/extractor`, `internal/pipeline`, `internal/config`
- **Supersedes:** base64-inline extraction path in `extractor.Extract`

## 1. Problem

Uploading exam images frequently fails with a transport error during the read
phase of the Moonshot/Kimi chat-completions call:

```
extract: Post "https://api.moonshot.ai/v1/chat/completions":
  read tcp 198.18.0.1:58474->240.0.0.25:443: read: connection reset by peer
```

### Root-cause analysis

1. **It is not a client timeout.** The vision HTTP timeout is `90s` and the
   error is `connection reset by peer`, not `deadline exceeded` / `timeout`.
   The connection is being killed mid-request, not given up on by the client.
2. **The addresses are reserved ranges.** `198.18.0.0/15` (benchmark) and
   `240.0.0.0/4` (reserved) are the signature of a transparent proxy / VPN in
   **fake-IP DNS mode** (Clash / ClashX / Surge / Stash on macOS). The
   connection is owned by the proxy core, which is what sends the RST.
3. **The reset happens during the read phase.** With `kimi-k2.6` the
   `thinking` capability is **enabled by default**, and large high-res exam
   images push processing into the tens of seconds. During that silent wait
   (request fully sent, no response bytes yet), the proxy's connection-idle
   limit trips and it resets the TCP connection.

The current code compounds this with two defects:
- The image is sent as a **base64 data URL inside the chat request body**, so
  the single POST is large and lives a long time.
- `extractWithRetries` runs its **3 attempts back-to-back with zero delay** —
  through a flaky proxy they fail together instead of recovering.

## 2. Goals & non-goals

**Goals**
- Eliminate (or drastically reduce) connection-reset failures on image
  extraction through a lossy proxy.
- Keep the existing `pipeline.AIExtractor` contract and return-shape semantics
  intact.
- Improve retry resilience for transient transport errors generally.

**Non-goals**
- No base64 fallback path (if upload fails, the pipeline retries upload).
- No file-listing / background cleanup cron.
- No new configuration for backoff parameters (hardcoded sensible constants).
- No changes to the verifier, embedder, enhancer, `oai.NewClient`, or storage
  layers. (The openai-go SDK already retries connection errors / 5xx **twice by
  default** — verified in `option/requestoption.go` — so no client change is
  needed; the substantive resilience fix is the pipeline-level backoff.)

## 3. Grounding: confirmed Moonshot/Kimi API facts

Source: official `platform.kimi.com` docs (`/api/chat`, `/api/files-upload`,
`/guide/use-kimi-vision-model`) plus the v1.12.0 `openai-go` module source.

- **Image upload:** `POST /v1/files` (multipart `file` + `purpose="image"`).
  Returns a `FileObject` `{ id, bytes, created_at, filename, purpose, status }`
  with `status:"ready"` for vision — **no parsing poll required** (polling is
  only for `purpose=file-extract`). Limits: 1000 files / 100 MB each / 10 GB
  total per user.
- **Reference by ID:** in the chat request, `image_url.url` accepts either a
  base64 data URL **or** `ms://<file_id>` (Moonshot Storage scheme). Using the
  file reference makes the chat request body tiny.
- **Model:** `kimi-k2.6` is vision-capable and supports `ms://` references for
  images.
- **`thinking`:** Moonshot-specific, enabled by default on `kimi-k2.6`; set
  `{"type":"disabled"}` to turn it off (cuts latency).
- **`temperature` / `top_p`:** fixed by the model for `k2.6`; setting them
  errors. The current code already omits them — must stay omitted.
- **Body size:** vision requests capped at 100 MB; image-count unlimited.

### openai-go v1.12.0 mechanisms (verified in module cache)
- `SetExtraFields(map[string]any)` is a **promoted method** on every generated
  request param (declared on `metadata` in the internal `packages/param`
  package; reachable without importing it because `ChatCompletionNewParams`
  embeds it by value) → lets us inject the non-standard `thinking` field into
  the typed `ChatCompletionNewParams`.
- `option.WithMaxRetries(int)` exists and the SDK **defaults to 2 retries** on
  connection errors / 5xx — so we do **not** change `oai.NewClient`. Resilience
  is added at the pipeline level (§4.4) instead.
- The typed Files API pins `purpose` to OpenAI-standard enums; Moonshot's
  `image` value is non-standard, so upload is implemented as a **raw
  `multipart/form-data` POST** with stdlib `net/http` (no SDK enum friction).

## 4. Design

### 4.1 Component changes

| File | Change |
|------|--------|
| `internal/ai/extractor/extractor.go` | `Extract` rewritten to upload → chat(`ms://`) → parse. New fields on `Extractor`: `baseURL`, `apiKey`, `httpClient` (`*http.Client` with `cfg.Timeout`) for raw file ops, and `thinking bool` (from `config.VisionConfig`). `New` populates them. |
| `internal/ai/extractor/imagepart.go` | Generalize `imageURLPart` to accept any URL string (used for `ms://<file_id>`). |
| `internal/ai/extractor/files.go` *(new)* | `uploadImage(ctx, image, mime) (fileID string, err error)` — raw multipart `POST {baseURL}/files` with `purpose=image`; **any non-2xx is returned as a transport error** (see §4.3). `deleteFile(ctx, fileID) error` — raw `DELETE {baseURL}/files/{id}`. stdlib only. |
| `internal/pipeline/pipeline.go` | `extractWithRetries` gains exponential backoff + jitter between attempts. New unexported `backoff func(attempt int) time.Duration` field on `Pipeline` (default `defaultBackoff`), injectable in tests; the pure `defaultBackoff(attempt)` helper lives here too. |
| `internal/config/config.go`, `internal/config/config.yaml` | `VisionConfig.Thinking bool` (default `false`) + env override `COEUS_AI_VISION_THINKING`. |

### 4.2 `Extract` data flow (new)

```
Extract(ctx, image, mime) (pipeline.ExtractResult, error):
  1. if ctx.Err() != nil → return (zero, "extract: %w")
  2. fileID, err := e.uploadImage(ctx, image, mime)
        err != nil → return (zero, "extract: upload: %w")    // transport → retried
  3. defer e.deleteFile(ctx, fileID)  // best-effort; see "Cleanup context" note below
  4. params := ChatCompletionNewParams{
          Model: e.model,
          Messages: [ SystemMessage(systemPrompt),
                      UserMessage([ TextContentPart(schema text),
                                    imageURLPart("ms://"+fileID) ]) ],
          ResponseFormat: json_object,
          // temperature / top_p intentionally NOT set (k2.6 fixed)
     }
     if !e.thinking:
         params.SetExtraFields({"thinking": {"type": "disabled"}})
  5. completion, err := e.client.Chat.Completions.New(ctx, params)   // typed, same transport
        err != nil → return (zero, "extract: %w")            // transport → retried
  6. cleaned := stripCodeFence(completion.Choices[0].Message.Content)
     json.Unmarshal(cleaned → &extractionResponse)
        err != nil → return (zero, "extract: parse model JSON: %w")  // retried
     return toPipeline(resp), nil
```

Notes:
- Upload is **per attempt** (inside `Extract`). Each of the 3 retries re-uploads
  and — via the deferred delete — frees its own file, so no orphans accumulate
  across retries.
- **Cleanup context:** the deferred delete must run even when the request ctx is
  cancelled or expired (otherwise a request timeout leaks the uploaded file).
  `deleteFile` therefore derives its own context —
  `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` — so it is
  decoupled from the caller's cancellation but still bounded. The short 5s cap
  matters because the DELETE travels through the same lossy proxy that may RST
  it; this bounds how long any single cleanup can stall the worker.
  (`WithoutCancel` is available in Go 1.21+; this repo is on 1.26.3.)
- Filename for the upload is derived from the MIME type
  (`image/jpeg`→`.jpg`, `image/png`→`.png`, `image/webp`→`.webp`); Moonshot
  returns the `id` synchronously.
- `imageURLPart` already takes a URL string; only its documented intent changes.

### 4.3 Error handling & return-shape contract (preserved)

The current `Extract` doc-comment contract is kept exactly:
- **transport failure** (upload error, chat HTTP/network error, JSON-parse
  failure) → `(zero, err)` → retried by `extractWithRetries`.
- **content failure** (`error != nil` in model JSON) → `(result, nil)` →
  handled by pipeline code (terminal or retried per code).
- **success** → `(result, nil)`.

`deleteFile` errors are **logged at Warn and never returned**. The delete is
deferred so it runs on both success and chat failure, preventing orphaned files
on failed attempts.

**Upload non-2xx handling (deliberate decision):** `uploadImage` returns *every*
non-2xx response as a transport error, so the pipeline retries it. This is
uniform with how chat-call errors are already treated and keeps the contract
simple. Terminal-looking 4xx are practically unreachable in normal operation —
401/403 (auth) are pre-validated by `config.Validate()` at startup, and 413
(payload too large) cannot occur because uploads are capped at 10 MB vs. the
100 MB file limit — so the cost of retrying them is acceptable. (A revoked key
would fail all attempts regardless of classification, since the chat call would
401 too.)

### 4.4 Retry / backoff (`extractWithRetries`)

Replace the current zero-delay loop with exponential backoff + jitter. To keep
it unit-testable without real sleeping, the `Pipeline` gains an unexported
`backoff func(attempt int) time.Duration` field, set to `defaultBackoff` in the
constructor and overridden in tests; `defaultBackoff` is a pure package func:

```
func defaultBackoff(attempt int) time.Duration
    // base=1s, cap=8s, factor=2:  1s, 2s, 4s, 8s, 8s...
    // ±20% jitter
```

- Applied between every retried attempt (transport errors and the retryable
  content codes `unreadable_image` / `no_questions_found`). In tests the field
  is replaced with e.g. `func(int) time.Duration { return 1*time.Millisecond }`.
- The sleep honors `ctx` — if `ctx` is cancelled mid-backoff, return early.
- `partial_extraction` and unknown codes remain terminal (no retry, no delay).

### 4.5 Configuration

```yaml
ai:
  vision:
    model: "kimi-k2.6"
    timeout: 90s
    thinking: false   # k2.6 defaults to enabled; disabled cuts latency
```

- `VisionConfig.Thinking bool`, zero-value `false` (disabled) = new default.
- Env override `COEUS_AI_VISION_THINKING=true` re-enables thinking (useful if
  extraction quality drops on messy handwriting). Parsed in `applyEnvOverrides`
  following the existing boolean pattern (`true|false|1|0`, matching
  `COEUS_CORS_ALLOW_CREDENTIALS`).

## 5. Testing

All new/changed tests are `-short`-friendly (no Docker, no real Moonshot API).
The httptest server routes by `r.URL.Path` (`/v1/files`, `/v1/files/{id}`,
`/v1/chat/completions`) and captures request bodies for assertion.

- **`internal/ai/extractor/extractor_test.go`** (httptest server):
  - Happy path: server handles `POST /v1/files` → returns `{id}`, then the chat
    call → asserts the captured chat request body (unmarshalled into
    `map[string]any`) contains `"ms://<id>"` and `"thinking":{"type":"disabled"}`;
    returns parsed questions via the existing response schema.
  - Upload failure (e.g. 500 from `/v1/files`) → returns a wrapped transport
    error and the chat endpoint is never hit (handler counter stays 0).
  - `DELETE /v1/files/{id}` is recorded after completion, including when the
    chat call fails.
  - Thinking-enabled path: build `Extractor` with `thinking=true` and assert the
    chat body has **no** `thinking` key.
- **`internal/ai/extractor/files_test.go`** *(new)*:
  - multipart shape: parse the upload body with `mime/multipart.NewReader`,
    assert fields `purpose=image` + `file`, and the filename extension derived
    from MIME.
  - DELETE path against httptest, in isolation.
  - **Cleanup-on-cancel:** cancel the caller `ctx`, then call `Extract` (with a
    chat handler that blocks until the ctx is done) and assert the DELETE still
    reaches the server — verifying the `WithoutCancel` cleanup context.
- **`internal/pipeline`**:
  - unit-test the pure `defaultBackoff` (monotonic, bounded by the 8s cap, jitter
    within ±20%).
  - retry-spacing test using a stub `AIExtractor` that fails N times, with the
    `Pipeline.backoff` field injected to ~1ms, asserting attempt count and a
    bounded total elapsed time.
  - backoff aborts early when `ctx` is cancelled mid-sleep.

## 6. Risks & mitigations

- **`DELETE /v1/files/{id}` support:** not explicitly present in the fetched
  OpenAPI but standard for OpenAI-compatible APIs. Best-effort + logged, so a
  rejection is benign (files accumulate toward the 1000 cap). Will verify
  against the live API during implementation; if unsupported, document a
  follow-up for periodic cleanup.
- **Thinking-off quality:** structured JSON extraction should not need
  chain-of-thought, but if accuracy drops, flip `COEUS_AI_VISION_THINKING=true`
  to re-enable instantly — no code change.
- **Proxy idle timeout (operational, out of code scope):** this change mitigates
  the proxy-reset mechanism by shortening the idle window; raising the local
  proxy's connection-idle timeout is a complementary operational step the user
  may take independently.
- **Upload & DELETE also traverse the proxy:** the upload POST carries the raw
  image bytes through the same lossy proxy that RSTs the chat call. The upload
  is still favored over inline base64 because (a) it has no `thinking`-latency
  on the server, and (b) Moonshot processes a server-side file more efficiently.
  Upload errors are retried by the pipeline. The DELETE cleanup is bounded to
  5s (§4.2) so a proxy RST during cleanup cannot stall a worker.
- **Per-attempt re-upload cost:** a full retry re-uploads the image (≤ 2 calls
  per attempt, ≤ 6 for all 3 attempts). Peak simultaneous uploaded files under
  the default 4-worker pool is `4 workers × 3 attempts = 12`, far below the
  1000-file cap. Acceptable.

## 7. Open questions

None blocking. The single thing to confirm during implementation is whether
Moonshot accepts `DELETE /v1/files/{id}` (see Risks).
