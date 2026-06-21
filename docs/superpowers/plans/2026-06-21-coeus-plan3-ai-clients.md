# Coeus Plan 3: Real AI Clients, Wiring, and N+1 Fix

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the four fake pipeline ports with real implementations (govips enhancer + Moonshot/Kimi vision extractor + DeepSeek text verifier + OpenAI embedder), wire the worker pool into the application lifecycle, and fix the N+1 job-status query in the image-list handler.

**Architecture:** Three LLM-style clients share one OpenAI-compatible client factory (`internal/ai/oai`) and are named by **role** (`extractor`/`verifier`/`embedder`/`enhancer`) — the vendor is a config detail. Each client is unit-tested with `httptest.NewServer` + fixture JSON (no live API calls). The enhancer is pure Go via govips (libvips CGO binding). Config renames vendor-based names to role-based names (`kimi`→`vision`, `deepseek`→`reviewer`) and adds required-API-key validation. The N+1 fix adds one bulk `FindJobStatusesBySession` call (`DISTINCT ON`) to replace the per-image loop. Wiring constructs everything in `wire.go` (including `vips.Startup`), calls `WorkerPool.Start` in `main.go`, and stops workers → libvips → DB in `App.Close`.

**Tech Stack:** Go 1.26, openai/openai-go (official SDK), davidbyttow/govips/v2 (libvips CGO), invopop/jsonschema, pgx/v5, Gin, httptest

**Spec:** `docs/superpowers/specs/2026-06-21-coeus-plan3-ai-clients-design.md` (Approved)

---

## File Structure

```
internal/
  ai/                        # NEW tree
    oai/
      client.go              # NewClient(baseURL, apiKey, timeout) → *openai.Client
    enhancer/
      enhancer.go            # govips image processing (ImageEnhancer port)
      enhancer_test.go       # JPEG/PNG/WebP round-trip, invalid bytes
    embedder/
      embedder.go            # OpenAI embeddings (AIEmbedder port)
      embedder_test.go       # httptest + dim check
    verifier/
      verifier.go            # DeepSeek text LLM (AIVerifier port)
      prompt.go              # verification system prompt (const)
      schema.go              # DTOs + fromPipeline/toPipeline mapping
      verifier_test.go       # httptest cases
    extractor/
      extractor.go           # Kimi vision LLM (AIExtractor port)
      prompt.go              # extraction system prompt (const)
      schema.go              # DTOs + toPipeline mapping + choice labeling
      extractor_test.go      # httptest + error taxonomy cases
  config/
    config.go                # MODIFY: rename Kimi→Vision, DeepSeek→Reviewer; validation
    config.yaml              # MODIFY: kimi→vision, deepseek→reviewer
    config_test.go           # MODIFY: renamed env vars + new validation subtests
  storage/
    ports.go                 # MODIFY: add FindJobStatusesBySession to JobQueue
    postgres/
      job_queue.go           # MODIFY: implement FindJobStatusesBySession
      job_queue_test.go      # MODIFY: add bulk-status test
  httpapi/handlers/
    images.go                # MODIFY: rewrite List to use bulk status lookup
    images_test.go           # MODIFY: fake + test for FindJobStatusesBySession
  app/
    wire.go                  # MODIFY: construct clients + Pipeline + WorkerPool + vips.Startup
  pipeline/
    worker.go                # (unchanged) WorkerPool.Start/Stop already exist
cmd/coeus/
  main.go                    # MODIFY: call application.WorkerPool.Start(ctx)
```

---

## Task 0: Prerequisites — libvips + Go Dependencies

govips is a CGO binding to libvips, which must be installed on the system before any govips code will build. The three new Go modules are pulled in once and shared by all client tasks.

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Install libvips system dependency**

macOS:
```bash
brew install vips pkg-config
```

Linux:
```bash
sudo apt install libvips-dev
```

Verify:
```bash
pkg-config --modversion vips
```
Expected: a version string (e.g. `8.16.x`).

- [ ] **Step 2: Add the three Go modules**

```bash
go get github.com/openai/openai-go github.com/davidbyttow/govips/v2 github.com/invopop/jsonschema
go mod tidy
```

- [ ] **Step 3: Verify the modules are present and the tree still builds**

Run: `go build ./...`
Expected: no output (success — the new packages are downloaded; nothing imports them yet).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add openai-go, govips, jsonschema for Plan 3"
```

---

## Task 1: Config Renames + API-Key Validation

Rename `KimiConfig` → `VisionConfig`, `DeepSeekConfig` → `ReviewerConfig`, the `AIConfig` field names (`kimi`→`vision`, `deepseek`→`reviewer`), the four env vars (`COEUS_AI_KIMI_*` → `COEUS_AI_VISION_*`, `COEUS_AI_DEEPSEEK_*` → `COEUS_AI_REVIEWER_*`), and the embedded YAML. Add required-API-key validation so `main.go` fails fast at startup.

This is a rename, so the TDD flow is: update tests/env-var usage first (they break the build), then rename in `config.go` to make them pass.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config.yaml`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Update tests to the renamed env vars (tests fail to compile)**

In `internal/config/config_test.go`, replace every `COEUS_AI_KIMI_API_KEY` with `COEUS_AI_VISION_API_KEY` and every `COEUS_AI_DEEPSEEK_API_KEY` with `COEUS_AI_REVIEWER_API_KEY`. This affects `TestLoadDefaults` and `TestEnvOverridesYAML`.

In `TestLoadDefaults`, the three `t.Setenv` calls become:
```go
t.Setenv("COEUS_AI_VISION_API_KEY", "vision-key")
t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
```

In `TestEnvOverridesYAML`, replace the two renamed lines:
```go
t.Setenv("COEUS_AI_VISION_API_KEY", "vision-key")
t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
```

- [ ] **Step 2: Add a missing-AI-key subtest to TestValidate_MissingSecrets**

Append a new subtest inside `TestValidate_MissingSecrets` in `internal/config/config_test.go`:

```go
	t.Run("missing ai api keys", func(t *testing.T) {
		t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
		t.Setenv("COEUS_JWT_SECRET", "test-secret")
		t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
		t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
		// vision key intentionally omitted

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		err = cfg.Validate()
		if err == nil {
			t.Fatal("Validate() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ai.vision.api_key") {
			t.Errorf("Validate() error = %q, expected to mention ai.vision.api_key", err.Error())
		}
	})
```

- [ ] **Step 3: Update TestValidate_WithSecrets to set all three keys**

In `internal/config/config_test.go`, replace the body of `TestValidate_WithSecrets` with:

```go
func TestValidate_WithSecrets(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_VISION_API_KEY", "vision-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
	t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: compile errors — `cfg.AI.Kimi undefined`, `cfg.AI.DeepSeek undefined` (structs not yet renamed), and the new validation test fails because `Validate()` does not yet require the vision key.

- [ ] **Step 5: Rename structs and AIConfig fields in config.go**

In `internal/config/config.go`, replace the `AIConfig`, `KimiConfig`, and `DeepSeekConfig` block (lines 46–64) with:

```go
type AIConfig struct {
	Vision   VisionConfig   `yaml:"vision"`
	Reviewer ReviewerConfig `yaml:"reviewer"`
	Embedder EmbedderConfig `yaml:"embedder"`
}

type VisionConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}

type ReviewerConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}
```

`EmbedderConfig` (lines 66–71) stays unchanged.

- [ ] **Step 6: Rename env-var reads in applyEnvOverrides**

In `internal/config/config.go`, replace the four Kimi/DeepSeek env reads (lines 110–121) with:

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
```

The two embedder reads (lines 122–127) stay unchanged.

- [ ] **Step 7: Add required-API-key validation to Validate()**

In `internal/config/config.go`, replace the `Validate` function (lines 141–151) with:

```go
// Validate checks that secrets required by the current plan are set.
// AI API keys are required so the app fails fast at startup rather than
// failing per-request inside the pipeline.
func (c *Config) Validate() error {
	if c.Postgres.DSN == "" {
		return fmt.Errorf("postgres.dsn is required (set COEUS_POSTGRES_DSN)")
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt.secret is required (set COEUS_JWT_SECRET)")
	}
	if c.AI.Vision.APIKey == "" {
		return fmt.Errorf("ai.vision.api_key is required (set COEUS_AI_VISION_API_KEY)")
	}
	if c.AI.Reviewer.APIKey == "" {
		return fmt.Errorf("ai.reviewer.api_key is required (set COEUS_AI_REVIEWER_API_KEY)")
	}
	if c.AI.Embedder.APIKey == "" {
		return fmt.Errorf("ai.embedder.api_key is required (set COEUS_AI_EMBEDDER_API_KEY)")
	}
	return nil
}
```

- [ ] **Step 8: Rename YAML keys in config.yaml**

In `internal/config/config.yaml`, replace the `ai:` block (lines 15–24) with:

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

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: all tests `PASS`, including the new `missing ai api keys` subtest.

- [ ] **Step 10: Verify the whole tree still builds**

Run: `go build ./...`
Expected: no output (success). No other package references `KimiConfig`/`DeepSeekConfig` — confirm with: `grep -rn "KimiConfig\|DeepSeekConfig\|cfg.AI.Kimi\|cfg.AI.DeepSeek" internal/ cmd/` → no matches.

- [ ] **Step 11: Commit**

```bash
git add internal/config/config.go internal/config/config.yaml internal/config/config_test.go
git commit -m "feat(config): rename kimi/deepseek to vision/reviewer, require AI keys"
```

---

## Task 2: The `oai` Wrapper

A single factory that removes SDK boilerplate from each LLM client. The three LLM-style clients (extractor, verifier, embedder) all take an OpenAI-compatible endpoint, so they share one constructor. The enhancer is pure Go and does **not** use `oai`.

**Files:**
- Create: `internal/ai/oai/client.go`

- [ ] **Step 1: Create the wrapper**

Create `internal/ai/oai/client.go`:

```go
// Package oai is a thin factory for OpenAI-compatible clients.
// All three LLM-style clients (extractor, verifier, embedder) take an
// OpenAI-compatible endpoint, so they share this constructor. The enhancer
// is pure Go and does not use it.
package oai

import (
	"net/http"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// NewClient builds an *openai.Client pointed at baseURL, authenticated with
// apiKey, with the given per-request HTTP timeout. An empty baseURL makes the
// SDK use its default (OpenAI) endpoint.
func NewClient(baseURL, apiKey string, timeout time.Duration) *openai.Client {
	return openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(&http.Client{Timeout: timeout}),
	)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/ai/oai/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/ai/oai/client.go
git commit -m "feat(ai/oai): add shared OpenAI-compatible client factory"
```

---

## Task 3: Enhancer (govips)

Deterministic Go image processing via govips (libvips). **No AI call.** The pipeline already treats enhancer failure as best-effort (`pipeline.go:95-97`), so `Enhance` returns `(nil, err)` on any failure — the pipeline falls back to the original bytes.

> **libvips lifecycle note:** govips requires a one-time `vips.Startup(nil)` before any image operation and `vips.Shutdown()` at exit. That lifecycle is wired in Task 8 (wire.go + App.Close), **not** inside the enhancer — the enhancer assumes libvips is already initialized. The test calls `vips.Startup`/`Shutdown` itself so it is self-contained.

**Files:**
- Create: `internal/ai/enhancer/enhancer.go`
- Create: `internal/ai/enhancer/enhancer_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ai/enhancer/enhancer_test.go`:

```go
package enhancer

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
	"log/slog"
	"io"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// encodePNG builds a small in-memory PNG for round-trip tests.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// toMime re-encodes the PNG source into the target MIME via govips so the
// enhancer receives the same container it would in production.
func toMime(t *testing.T, pngBytes []byte, mime string) []byte {
	t.Helper()
	img, err := vips.NewImageFromBuffer(pngBytes)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	defer img.Close()
	switch mime {
	case "image/jpeg":
		b, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: 92})
		if err != nil {
			t.Fatalf("export jpeg: %v", err)
		}
		return b
	case "image/png":
		b, _, err := img.ExportPng(&vips.PngExportParams{Compression: 6})
		if err != nil {
			t.Fatalf("export png: %v", err)
		}
		return b
	case "image/webp":
		b, _, err := img.ExportWebp(&vips.WebpExportParams{Quality: 92})
		if err != nil {
			t.Fatalf("export webp: %v", err)
		}
		return b
	default:
		t.Fatalf("unsupported test mime %q", mime)
		return nil
	}
}

// decodedDims re-decodes the enhancer output and returns its dimensions.
func decodedDims(t *testing.T, buf []byte) (int, int) {
	t.Helper()
	img, err := vips.NewImageFromBuffer(buf)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	defer img.Close()
	return img.Width(), img.Height()
}

func TestEnhancer_RoundTrip(t *testing.T) {
	vips.Startup(nil)
	defer vips.Shutdown()

	e := New(quietLogger())
	src := encodePNG(t, 8, 6)

	for _, mime := range []string{"image/jpeg", "image/png", "image/webp"} {
		t.Run(mime, func(t *testing.T) {
			in := toMime(t, src, mime)
			out, err := e.Enhance(t.Context(), in, mime)
			if err != nil {
				t.Fatalf("Enhance(%s): %v", mime, err)
			}
			if len(out) == 0 {
				t.Fatalf("output is empty")
			}
			if bytes.Equal(out, in) {
				t.Errorf("output identical to input — enhance is a no-op")
			}
			w, h := decodedDims(t, out)
			if w != 8 || h != 6 {
				t.Errorf("dims = %dx%d, want 8x6", w, h)
			}
		})
	}
}

func TestEnhancer_InvalidBytes(t *testing.T) {
	vips.Startup(nil)
	defer vips.Shutdown()

	e := New(quietLogger())
	_, err := e.Enhance(t.Context(), []byte("not an image"), "image/jpeg")
	if err == nil {
		t.Fatal("expected error for invalid bytes, got nil")
	}
}

func TestEnhancer_UnsupportedMime(t *testing.T) {
	vips.Startup(nil)
	defer vips.Shutdown()

	e := New(quietLogger())
	pngBytes := toMime(t, encodePNG(t, 4, 4), "image/png")
	_, err := e.Enhance(t.Context(), pngBytes, "application/pdf")
	if err == nil {
		t.Fatal("expected error for unsupported MIME, got nil")
	}
}
```

> **Note on `t.Context()`:** Go 1.24+ provides `testing.T.Context()` which returns a context canceled when the test finishes. If the toolchain in use does not have it, substitute `context.Background()` and add `"context"` to the imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ai/enhancer/ -v`
Expected: compile error — `undefined: New`, `undefined: Enhancer`.

- [ ] **Step 3: Implement the enhancer**

Create `internal/ai/enhancer/enhancer.go`:

```go
// Package enhancer implements pipeline.ImageEnhancer using govips (libvips).
// It applies deterministic contrast/gamma/sharpen adjustments and re-encodes
// the image to the same MIME the caller provided. It makes no AI calls.
package enhancer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Enhancer satisfies the port.
var _ pipeline.ImageEnhancer = (*Enhancer)(nil)

type Enhancer struct {
	log *slog.Logger
}

func New(log *slog.Logger) *Enhancer {
	if log == nil {
		log = slog.Default()
	}
	return &Enhancer{log: log}
}

// Enhance applies auto-rotate, +15% contrast, gamma 1.15, mild sharpen, then
// re-encodes to the same MIME. Any failure returns (nil, err); the pipeline
// falls back to the original bytes.
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

	// +15% contrast, pivoting around mid-gray 128:
	// 1.15 * (in - 128) + 128 = 1.15*in - 19.2
	if err := img.Linear1(1.15, -19.2); err != nil {
		return nil, fmt.Errorf("enhance: contrast: %w", err)
	}

	// Gamma 1.15 brightens midtones (govips: out = in^(1/exponent), >1 brightens).
	if err := img.Gamma(1.15); err != nil {
		return nil, fmt.Errorf("enhance: gamma: %w", err)
	}

	// Mild sharpen for text edge crispness (sigma=0.5, x1=1.0, m2=2.0).
	if err := img.Sharpen(0.5, 1.0, 2.0); err != nil {
		return nil, fmt.Errorf("enhance: sharpen: %w", err)
	}

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

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ai/enhancer/ -v`
Expected: all tests `PASS` — `RoundTrip/image_jpeg`, `RoundTrip/image_png`, `RoundTrip/image_webp`, `InvalidBytes`, `UnsupportedMime`.

> If govips reports `VipsForeignLoad: buffer is not in a known format` for the WebP source, confirm libvips was built with WebP support (`pkg-config --modversion vips` and `vips --vips-config | grep webp`). The Homebrew formula includes it by default.

- [ ] **Step 5: Commit**

```bash
git add internal/ai/enhancer/enhancer.go internal/ai/enhancer/enhancer_test.go
git commit -m "feat(ai/enhancer): add govips image enhancer"
```

---

## Task 4: Embedder (OpenAI Embeddings)

Simplest LLM client — validates the `oai` + httptest pattern used by the verifier and extractor. Returns `[]float32` (pgvector wants float32; the SDK returns `[]float64`).

**Files:**
- Create: `internal/ai/embedder/embedder.go`
- Create: `internal/ai/embedder/embedder_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ai/embedder/embedder_test.go`:

```go
package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// embeddingsServer returns a canned /embeddings response with `dim` floats.
func embeddingsServer(t *testing.T, dim int, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		// Build a `dim`-length vector inline.
		vals := make([]string, dim)
		for i := range vals {
			vals[i] = fmt.Sprintf("0.%03d", i%1000)
		}
		resp := strings.Replace(body, "__VECTORS__", strings.Join(vals, ","), 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
}

func testCfg(baseURL string) config.EmbedderConfig {
	return config.EmbedderConfig{
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   "text-embedding-3-small",
		Dim:     1536,
	}
}

const happyEmbeddingsBody = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[__VECTORS__]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":4,"total_tokens":4}}`

func TestEmbedder_HappyPath(t *testing.T) {
	srv := embeddingsServer(t, 1536, http.StatusOK, happyEmbeddingsBody)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	vec, err := e.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 1536 {
		t.Fatalf("len(vec) = %d, want 1536", len(vec))
	}
	// float32 cast sanity
	if got := float64(vec[0]); got < -1 || got > 1 {
		t.Errorf("vec[0] = %v, out of [-1,1]", got)
	}
}

func TestEmbedder_TransportError(t *testing.T) {
	srv := embeddingsServer(t, 0, http.StatusInternalServerError, `{"error":"down"}`)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}

func TestEmbedder_DimensionMismatch(t *testing.T) {
	// Server returns 10-dim vector but cfg.Dim is 1536.
	srv := embeddingsServer(t, 10, http.StatusOK, happyEmbeddingsBody)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on dim mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error = %q, expected to mention dimension", err.Error())
	}
}

func TestEmbedder_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

func TestEmbedder_EmptyText(t *testing.T) {
	srv := embeddingsServer(t, 1536, http.StatusOK, happyEmbeddingsBody)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	// The guard rejects empty input before any network call.
	_, err := e.Embed(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
}

// Quiet the unused-import linter when json is only used in package-level fixtures.
var _ = json.RawMessage(nil)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ai/embedder/ -v`
Expected: compile error — `undefined: New`, `undefined: Embedder`.

- [ ] **Step 3: Implement the embedder**

Create `internal/ai/embedder/embedder.go`:

```go
// Package embedder implements pipeline.AIEmbedder using the OpenAI
// embeddings API via the shared oai client factory.
package embedder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/openai/openai-go"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// embedderDefaultTimeout caps the per-request HTTP timeout. EmbedderConfig has
// no Timeout field (intentionally), so the constant lives here.
const embedderDefaultTimeout = 30 * time.Second

// Compile-time guarantee that Embedder satisfies the port.
var _ pipeline.AIEmbedder = (*Embedder)(nil)

type Embedder struct {
	client *openai.Client
	dim    int
	model  string
	log    *slog.Logger
}

func New(cfg config.EmbedderConfig, log *slog.Logger) *Embedder {
	if log == nil {
		log = slog.Default()
	}
	return &Embedder{
		client: oai.NewClient(cfg.BaseURL, cfg.APIKey, embedderDefaultTimeout),
		dim:    cfg.Dim,
		model:  cfg.Model,
		log:    log,
	}
}

// Embed calls the embeddings endpoint and returns a float32 vector. On any
// failure (transport, malformed response, dimension mismatch, empty input) it
// returns (nil, err) — the pipeline skips semantic dedup for that question.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) == 0 {
		return nil, fmt.Errorf("embed: empty input")
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: openai.EmbeddingNewParamsInputUnion(StringInput{text}),
	}

	resp, err := e.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("embed: empty response data")
	}

	in := resp.Data[0].Embedding
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}

	if e.dim > 0 && len(out) != e.dim {
		return nil, fmt.Errorf("embed: dimension mismatch: got %d, want %d", len(out), e.dim)
	}
	return out, nil
}
```

- [ ] **Step 4: Add the EmbeddingNewParamsInput union helper**

> **SDK-version note:** `openai.EmbeddingNewParamsInputUnion` is a generated tagged union. The exact discriminator field name varies across openai-go patch versions. The implementation above uses a tiny local adapter `StringInput` so the SDK-version detail is isolated to one file. Create the adapter alongside the embedder:

Create `internal/ai/embedder/input.go`:

```go
package embedder

// StringInput adapts a plain string into the openai-go embedding input union.
//
// openai-go represents the `input` parameter of /embeddings as a generated
// tagged union (EmbeddingNewParamsInputUnion) whose exact discriminator field
// name varies across SDK patch versions. This file is the single place that
// needs to be reconciled with the pinned openai-go version.
//
// To discover the correct field for the installed version:
//   grep -rn "EmbeddingNewParamsInputUnion" $(go env GOMODCACHE)/github.com/openai/openai-go*
// then set `FromString` to match. The wire requirement is simply
// {"model":"<model>","input":"<text>"}.
type StringInput struct {
	Text string
}

// FromString sets the union to the given string. The concrete body matches the
// pinned SDK; if the generated union field is named differently, update only
// this method.
func (s StringInput) FromString() openai.EmbeddingNewParamsInputUnion {
	return openai.EmbeddingNewParamsInputUnion{
		OfString: openai.String(s.Text),
	}
}
```

> If the installed SDK's union discriminator is not `OfString`, run the `grep` shown in the file comment and update only the `FromString` body. The rest of the embedder is unaffected.

> If `openai.String` is not the correct helper either, the executor should inspect the union type's godoc and use the SDK-provided string setter. This is the single SDK-version reconciliation point for the embedder.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/ai/embedder/ -v`
Expected: all tests `PASS` — `HappyPath`, `TransportError`, `DimensionMismatch`, `MalformedJSON`, `EmptyText`.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/embedder/
git commit -m "feat(ai/embedder): add OpenAI embeddings client"
```

---

## Task 5: Verifier (DeepSeek Text LLM)

Text-only LLM via OpenAI-compatible Chat Completions. Reuses the `oai` + httptest pattern. The verifier has **no** retry path — any error is best-effort (the pipeline logs a warning and leaves questions in `moderation`).

**Files:**
- Create: `internal/ai/verifier/prompt.go`
- Create: `internal/ai/verifier/schema.go`
- Create: `internal/ai/verifier/verifier.go`
- Create: `internal/ai/verifier/verifier_test.go`

- [ ] **Step 1: Create the prompt**

Create `internal/ai/verifier/prompt.go`:

```go
package verifier

// systemPrompt is the verification contract: structural validation, answer
// correctness checks, and confidence re-evaluation. Based on the
// verify-extracted-questions skill, extended with `tags` per question. The
// verifier returns the full questions array plus a `_verification` summary.
const systemPrompt = `You are an answer-checking reviewer. You receive a JSON object containing
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

[Reference: skills/verify-extracted-questions/SKILL.md for the full specification]`
```

- [ ] **Step 2: Create the schema and DTOs**

Create `internal/ai/verifier/schema.go`:

```go
package verifier

import (
	"encoding/json"

	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

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
	Confidence      float64     `json:"confidence"`
	Explanation     string      `json:"explanation"`
	Tags            []string    `json:"tags,omitempty"`
}

// verificationResponse is the top-level object the model returns.
type verificationResponse struct {
	Verification json.RawMessage       `json:"_verification"`
	Questions    []verifiedQuestionDTO `json:"questions"`
}

// fromPipeline converts the pipeline's ExtractedQuestion slice into the skill's
// questionDTO shape: Choices ([]Answer with ID+Text) → plain strings,
// Answers ({ID,Text}) → {id,value}. pipeline "Text" → skill "question".
func fromPipeline(questions []pipeline.ExtractedQuestion) verificationInput {
	out := verificationInput{Questions: make([]questionDTO, len(questions))}
	for i, q := range questions {
		choices := make([]string, len(q.Choices))
		for j, c := range q.Choices {
			choices[j] = c.Text
		}
		answers := make([]answerDTO, len(q.Answers))
		for j, a := range q.Answers {
			answers[j] = answerDTO{ID: a.ID, Value: a.Text}
		}
		out.Questions[i] = questionDTO{
			Number:          q.Number,
			Question:        q.Text,
			MultipleCorrect: q.MultipleCorrect,
			Choices:         choices,
			Answers:         answers,
			Confidence:      q.Confidence,
			Tags:            q.Tags,
		}
	}
	return out
}

// toPipeline maps the model output by position (index = position in the array,
// matching the input order). Only Confidence and Explanation are consumed into
// the pipeline's VerifiedQuestion; the rest is for the model's internal
// consistency and is captured in the raw Report. An i >= inputCount guard
// prevents mapping beyond the input range.
func toPipeline(r verificationResponse, inputCount int) pipeline.VerifyResult {
	out := pipeline.VerifyResult{Report: r.Verification}
	for i, q := range r.Questions {
		if i >= inputCount {
			break
		}
		out.Summary.Results = append(out.Summary.Results, pipeline.VerifiedQuestion{
			Index:       i,
			Confidence:  q.Confidence,
			Explanation: q.Explanation,
		})
	}
	return out
}
```

- [ ] **Step 3: Write the failing tests**

Create `internal/ai/verifier/verifier_test.go`:

```go
package verifier

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// chatServer returns a canned chat-completion whose first choice's message
// content is `content`.
func chatServer(t *testing.T, status int, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		body := map[string]any{
			"id": "chatcmpl-test",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func testCfg(baseURL string) config.ReviewerConfig {
	return config.ReviewerConfig{
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   "deepseek-v4-pro",
	}
}

func sampleInput() []pipeline.ExtractedQuestion {
	return []pipeline.ExtractedQuestion{
		{Number: 1, Text: "What is 2+2?", Choices: []pipeline.Answer{{"A", "3"}, {"B", "4"}},
			Answers: []pipeline.Answer{{"B", "4"}}, Confidence: 0.95, Tags: []string{"math"}},
		{Number: 2, Text: "Capital of France?", Choices: []pipeline.Answer{{"A", "London"}, {"B", "Paris"}},
			Answers: []pipeline.Answer{{"B", "Paris"}}, Confidence: 0.90, Tags: []string{"geography"}},
	}
}

func TestVerifier_HappyPath(t *testing.T) {
	content := `{
		"_verification": {"timestamp":"2026-06-21T00:00:00Z","questions_verified":2,"summary":"ok"},
		"questions": [
			{"number":1,"question":"What is 2+2?","multiple_correct":false,"choices":["3","4"],"answers":[{"id":"B","value":"4"}],"confidence":0.92,"explanation":"correct","tags":["math"]},
			{"number":2,"question":"Capital of France?","multiple_correct":false,"choices":["London","Paris"],"answers":[{"id":"B","value":"Paris"}],"confidence":0.40,"explanation":"[VERIFICATION FLAG]\nOriginal answer: Paris","tags":["geography"]}
		]
	}`
	srv := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	v := New(testCfg(srv.URL), quietLogger())
	res, err := v.Verify(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(res.Summary.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(res.Summary.Results))
	}
	if res.Summary.Results[0].Index != 0 || res.Summary.Results[1].Index != 1 {
		t.Errorf("indices = %d,%d, want 0,1", res.Summary.Results[0].Index, res.Summary.Results[1].Index)
	}
	if res.Summary.Results[0].Confidence != 0.92 {
		t.Errorf("q0 confidence = %v, want 0.92", res.Summary.Results[0].Confidence)
	}
	if res.Summary.Results[1].Confidence != 0.40 {
		t.Errorf("q1 confidence = %v, want 0.40", res.Summary.Results[1].Confidence)
	}
	if res.Report == nil {
		t.Fatal("Report (_verification) should be the raw bytes")
	}
	var rep map[string]any
	if err := json.Unmarshal(res.Report, &rep); err != nil {
		t.Fatalf("Report not valid JSON: %v", err)
	}
	if rep["summary"] != "ok" {
		t.Errorf("report summary = %v, want ok", rep["summary"])
	}
}

func TestVerifier_FewerQuestionsReturned(t *testing.T) {
	content := `{
		"_verification": {"questions_verified":1},
		"questions": [
			{"number":1,"question":"What is 2+2?","confidence":0.9,"explanation":"ok"}
		]
	}`
	srv := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	v := New(testCfg(srv.URL), quietLogger())
	res, err := v.Verify(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(res.Summary.Results) != 1 {
		t.Fatalf("results = %d, want 1 (only index 0)", len(res.Summary.Results))
	}
	if res.Summary.Results[0].Index != 0 {
		t.Errorf("index = %d, want 0", res.Summary.Results[0].Index)
	}
}

func TestVerifier_TransportError(t *testing.T) {
	srv := chatServer(t, http.StatusInternalServerError, "")
	defer srv.Close()

	v := New(testCfg(srv.URL), quietLogger())
	_, err := v.Verify(context.Background(), sampleInput())
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}

func TestVerifier_MalformedJSON(t *testing.T) {
	srv := chatServer(t, http.StatusOK, "this is not json at all")
	defer srv.Close()

	v := New(testCfg(srv.URL), quietLogger())
	_, err := v.Verify(context.Background(), sampleInput())
	if err == nil {
		t.Fatal("expected error on malformed content, got nil")
	}
}

func TestVerifier_FencedJSON(t *testing.T) {
	content := "```json\n" + `{"_verification":{"questions_verified":1},"questions":[{"number":1,"question":"q","confidence":0.9,"explanation":"ok"}]}` + "\n```"
	srv := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	v := New(testCfg(srv.URL), quietLogger())
	res, err := v.Verify(context.Background(), sampleInput()[:1])
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(res.Summary.Results) != 1 {
		t.Fatalf("results = %d, want 1 (fence stripped)", len(res.Summary.Results))
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/ai/verifier/ -v`
Expected: compile error — `undefined: New`, `undefined: Verifier`.

- [ ] **Step 5: Implement the verifier**

Create `internal/ai/verifier/verifier.go`:

```go
// Package verifier implements pipeline.AIVerifier using a text LLM via the
// OpenAI-compatible Chat Completions API (DeepSeek by default).
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Verifier satisfies the port.
var _ pipeline.AIVerifier = (*Verifier)(nil)

type Verifier struct {
	client *openai.Client
	model  string
	log    *slog.Logger
}

func New(cfg config.ReviewerConfig, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{
		client: oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout),
		model:  cfg.Model,
		log:    log,
	}
}

// Verify sends the extracted questions to the reviewer model and returns the
// adjusted confidences + explanations plus the raw _verification report.
// Any error is returned to the caller; the pipeline treats it as best-effort.
func (v *Verifier) Verify(ctx context.Context, questions []pipeline.ExtractedQuestion) (pipeline.VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err)
	}

	userPayload, err := json.Marshal(fromPipeline(questions))
	if err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: marshal input: %w", err)
	}
	userMsg := "Verify the following extracted questions:\n```json\n" + string(userPayload) + "\n```"

	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(v.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMsg),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: openai.ResponseFormatJSONObject{},
		},
	}

	completion, err := v.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err)
	}
	if len(completion.Choices) == 0 {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: empty choices")
	}

	raw := completion.Choices[0].Message.Content
	cleaned := stripCodeFence(raw)

	var resp verificationResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: parse model JSON: %w", err)
	}
	return toPipeline(resp, len(questions)), nil
}

// stripCodeFence removes a leading ```json / ``` and trailing ``` fence if the
// model wrapped its JSON despite the json_object response format. Best-effort.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	// Drop the trailing fence.
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
```

> **SDK-version note:** the request-building helpers `openai.SystemMessage` / `openai.UserMessage` and the `OfJSONObject` union field are provided by openai-go as convenience constructors; their exact names vary across patch versions. If the build fails here, run:
> `grep -rn "func SystemMessage\|ResponseFormatJSONObject\|ChatCompletionNewParamsResponseFormatUnion" $(go env GOMODCACHE)/github.com/openai/openai-go*`
> and adjust the `params` construction to match the installed version. The wire requirement is a POST to `/chat/completions` with `messages` (system + user) and `response_format: {type: "json_object"}`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/ai/verifier/ -v`
Expected: all tests `PASS` — `HappyPath`, `FewerQuestionsReturned`, `TransportError`, `MalformedJSON`, `FencedJSON`.

- [ ] **Step 7: Commit**

```bash
git add internal/ai/verifier/
git commit -m "feat(ai/verifier): add DeepSeek text verification client"
```

---

## Task 6: Extractor (Kimi Vision LLM)

Most complex client — multimodal (image data URL) + the extraction error taxonomy that the pipeline's retry layer keys off of. Built last because it reuses the pattern from Tasks 4–5.

**Files:**
- Create: `internal/ai/extractor/prompt.go`
- Create: `internal/ai/extractor/schema.go`
- Create: `internal/ai/extractor/extractor.go`
- Create: `internal/ai/extractor/extractor_test.go`

- [ ] **Step 1: Create the prompt**

Create `internal/ai/extractor/prompt.go`:

```go
package extractor

// systemPrompt is the extraction contract: analyze the exam image and emit one
// JSON object with a `questions` array. Based on the extract-questions-from-image
// skill, extended with `tags` (subject classifiers) needed by the pipeline.
const systemPrompt = `You are an exam-image OCR and parsing engine. Analyze the image and extract
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

[Reference: skills/extract-questions-from-image/SKILL.md for the full specification]`
```

- [ ] **Step 2: Create the schema, DTOs, and choice-labeling helpers**

Create `internal/ai/extractor/schema.go`:

```go
package extractor

import (
	"strconv"

	"github.com/invopop/jsonschema"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// answerDTO mirrors the extraction output's answer shape.
type answerDTO struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// questionDTO matches the extract-questions-from-image skill wire format:
// choices are plain strings (labels stripped) and answers are {id,value}.
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

// extractionSchemaJSON is the JSON Schema for extractionResponse, rendered once
// at init and embedded in the user prompt as a format reminder.
var extractionSchemaJSON = schemaOf(extractionResponse{})

func schemaOf(v any) string {
	r := jsonschema.Reflector{DoNotReference: true}
	s := r.Reflect(v)
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// toPipeline maps the skill response to pipeline types.
//
// The skill returns choices as plain strings and answers as {id,value}. The
// pipeline's ExtractedQuestion.Choices is []Answer (with ID+Text). So we:
//  1. detect the labeling pattern from answer IDs (letters → "letter",
//     numbers → "number");
//  2. assign sequential IDs to choices based on that pattern;
//  3. map answer {id,value} to pipeline Answer{ID,Text};
//  4. map the skill's "message" to the pipeline's ExtractionError.Detail.
func toPipeline(r extractionResponse) pipeline.ExtractResult {
	res := pipeline.ExtractResult{}
	if r.Error != nil {
		res.Error = &pipeline.ExtractionError{
			Code:   r.Error.Code,
			Detail: r.Error.Message,
		}
	}
	res.Questions = make([]pipeline.ExtractedQuestion, len(r.Questions))
	for i, q := range r.Questions {
		labeling := detectChoiceLabeling(q.Answers)
		res.Questions[i] = pipeline.ExtractedQuestion{
			Number:          q.Number,
			Text:            q.Question,
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

// mapAnswers converts skill {id,value} pairs to pipeline {ID,Text}.
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
// For >26 letter-labeled choices this wraps past Z; acceptable for MVP — real
// exams rarely exceed ~10 choices.
func labelFor(i int, labeling string) string {
	switch labeling {
	case "number":
		return strconv.Itoa(i + 1)
	default:
		return string(rune('A' + i))
	}
}
```

- [ ] **Step 3: Write the failing tests**

Create `internal/ai/extractor/extractor_test.go`:

```go
package extractor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// chatServer returns a canned chat-completion whose first choice's content is
// `content`. It also captures the last request body for assertions.
func chatServer(t *testing.T, status int, content string) (*httptest.Server, *string) {
	t.Helper()
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		lastBody = string(buf[:n])
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		body := map[string]any{
			"id": "chatcmpl-test",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	return srv, &lastBody
}

func testCfg(baseURL string, timeout time.Duration) config.VisionConfig {
	return config.VisionConfig{
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   "kimi-k2.7",
		Timeout: timeout,
	}
}

func TestExtractor_HappyPath(t *testing.T) {
	content := `{
		"questions": [
			{"number":1,"question":"Capital of France?","multiple_correct":false,
			 "choices":["London","Paris"],"answers":[{"id":"B","value":"Paris"}],
			 "confidence":0.9,"explanation":"known fact","tags":["geography"]}
		]
	}`
	srv, body := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("fake-image"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(res.Questions))
	}
	q := res.Questions[0]
	if q.Text != "Capital of France?" {
		t.Errorf("Text = %q, want question mapped from 'question'", q.Text)
	}
	if len(q.Choices) != 2 {
		t.Fatalf("choices = %d, want 2", len(q.Choices))
	}
	if q.Choices[0].ID != "A" || q.Choices[0].Text != "London" {
		t.Errorf("choice[0] = {%s,%s}, want {A,London}", q.Choices[0].ID, q.Choices[0].Text)
	}
	if q.Choices[1].ID != "B" || q.Choices[1].Text != "Paris" {
		t.Errorf("choice[1] = {%s,%s}, want {B,Paris}", q.Choices[1].ID, q.Choices[1].Text)
	}
	if len(q.Answers) != 1 || q.Answers[0].ID != "B" || q.Answers[0].Text != "Paris" {
		t.Errorf("answers = %+v, want [{B,Paris}]", q.Answers)
	}
	if len(q.Tags) != 1 || q.Tags[0] != "geography" {
		t.Errorf("tags = %v, want [geography]", q.Tags)
	}
	if res.Error != nil {
		t.Errorf("unexpected extraction error: %+v", res.Error)
	}
	// Request shape: model + image_url part + json_object response_format.
	if !strings.Contains(*body, `"image_url"`) {
		t.Errorf("request body missing image_url part:\n%s", *body)
	}
	if !strings.Contains(*body, `"json_object"`) {
		t.Errorf("request body missing json_object response_format:\n%s", *body)
	}
	if !strings.Contains(*body, `"kimi-k2.7"`) {
		t.Errorf("request body missing model:\n%s", *body)
	}
}

func TestExtractor_UnreadableImage(t *testing.T) {
	content := `{"error":{"code":"unreadable_image","message":"blurry"}}`
	srv, _ := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("blur"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v (content errors are not Go errors)", err)
	}
	if res.Error == nil || res.Error.Code != "unreadable_image" {
		t.Fatalf("error = %+v, want code unreadable_image", res.Error)
	}
	if res.Error.Detail != "blurry" {
		t.Errorf("detail = %q, want 'blurry' (mapped from message)", res.Error.Detail)
	}
}

func TestExtractor_NoQuestions(t *testing.T) {
	content := `{"error":{"code":"no_questions_found","message":"blank image"}}`
	srv, _ := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("blank"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Error == nil || res.Error.Code != "no_questions_found" {
		t.Fatalf("error = %+v, want code no_questions_found", res.Error)
	}
}

func TestExtractor_PartialExtraction(t *testing.T) {
	content := `{
		"questions": [{"number":1,"question":"q1","choices":["a"],"answers":[{"id":"A","value":"a"}],"confidence":0.6,"tags":["math"]}],
		"error": {"code":"partial_extraction","message":"1 of 2","questions_extracted":1,"questions_expected":2}
	}`
	srv, _ := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Questions) != 1 {
		t.Fatalf("questions = %d, want 1 (partial keeps questions)", len(res.Questions))
	}
	if res.Error == nil || res.Error.Code != "partial_extraction" {
		t.Fatalf("error = %+v, want code partial_extraction", res.Error)
	}
}

func TestExtractor_NumberLabeling(t *testing.T) {
	content := `{
		"questions": [{"number":1,"question":"q","choices":["one","two"],
		 "answers":[{"id":"2","value":"two"}],"confidence":0.8,"tags":["math"]}]
	}`
	srv, _ := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, _ := e.Extract(context.Background(), []byte("img"), "image/png")
	if len(res.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(res.Questions))
	}
	q := res.Questions[0]
	if q.Choices[0].ID != "1" || q.Choices[1].ID != "2" {
		t.Errorf("number-labeled IDs = %s,%s, want 1,2", q.Choices[0].ID, q.Choices[1].ID)
	}
}

func TestExtractor_TransportError(t *testing.T) {
	srv, _ := chatServer(t, http.StatusInternalServerError, "")
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected transport error on HTTP 500, got nil")
	}
	if len(res.Questions) != 0 {
		t.Errorf("questions = %d, want 0 on transport error", len(res.Questions))
	}
}

func TestExtractor_MalformedJSON(t *testing.T) {
	srv, _ := chatServer(t, http.StatusOK, "totally not json")
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	_, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected parse error on malformed JSON, got nil")
	}
}

func TestExtractor_FencedJSON(t *testing.T) {
	content := "```json\n" + `{"questions":[{"number":1,"question":"q","choices":["a"],"answers":[{"id":"A","value":"a"}],"confidence":0.8,"tags":["x"]}]}` + "\n```"
	srv, _ := chatServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v (fence should be stripped)", err)
	}
	if len(res.Questions) != 1 {
		t.Fatalf("questions = %d, want 1 (fence stripped)", len(res.Questions))
	}
}

func TestExtractor_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 50*time.Millisecond), quietLogger())
	_, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/ai/extractor/ -v`
Expected: compile error — `undefined: New`, `undefined: Extractor`.

- [ ] **Step 5: Implement the extractor**

Create `internal/ai/extractor/extractor.go`:

```go
// Package extractor implements pipeline.AIExtractor using a vision LLM via the
// OpenAI-compatible Chat Completions API (Moonshot/Kimi by default). The image
// is sent as a base64 data URL in a multimodal user message.
package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Extractor satisfies the port.
var _ pipeline.AIExtractor = (*Extractor)(nil)

type Extractor struct {
	client *openai.Client
	model  string
	log    *slog.Logger
}

func New(cfg config.VisionConfig, log *slog.Logger) *Extractor {
	if log == nil {
		log = slog.Default()
	}
	return &Extractor{
		client: oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout),
		model:  cfg.Model,
		log:    log,
	}
}

// Extract sends the image to the vision model and returns an ExtractResult.
//
// Return-shape contract (consumed by pipeline.extractWithRetries):
//   - transport failure (HTTP/network/timeout)      → (zero, err)        [retried]
//   - content failure (Error != nil in model JSON) → (result, nil)      [by code]
//   - success                                       → (result, nil)
//
// "parse model JSON" failure is treated as transport-class so the pipeline
// retries — a prose-wrapped response may succeed on the next attempt.
func (e *Extractor) Extract(ctx context.Context, image []byte, mime string) (pipeline.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}

	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)
	userText := "Extract all questions from this exam image. Respond as a JSON object matching this schema:\n\n" + extractionSchemaJSON

	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(e.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(
				"```schema\n" + userText + "\n```\n\nNow extract from this image:",
				imageURLPart(dataURL),
			),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: openai.ResponseFormatJSONObject{},
		},
	}

	completion, err := e.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}
	if len(completion.Choices) == 0 {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: empty choices")
	}

	raw := completion.Choices[0].Message.Content
	cleaned := stripCodeFence(raw)

	var resp extractionResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: parse model JSON: %w", err)
	}
	return toPipeline(resp), nil
}

// stripCodeFence removes a leading ```json / ``` and trailing ``` fence if the
// model wrapped its JSON despite the json_object response format. Best-effort.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
```

- [ ] **Step 6: Add the image_url content-part adapter**

> **SDK-version note:** like the embedder input union, the openai-go multimodal content-part union (`ChatCompletionContentPartUnionParam`) has a version-dependent discriminator. This adapter isolates that detail to one file. The wire requirement is a message part of type `image_url` whose `url` is the data URL.

Create `internal/ai/extractor/imagepart.go`:

```go
package extractor

import (
	"github.com/openai/openai-go"
)

// imageURLPart builds the image_url content part carrying the data URL.
//
// openai-go's ChatCompletionContentPartUnionParam is a generated tagged union
// whose exact discriminator field varies across SDK patch versions. If the
// build fails here, discover the correct field with:
//   grep -rn "ChatCompletionContentPartUnionParam\|ImageURL\b" \
//       $(go env GOMODCACHE)/github.com/openai/openai-go*
// and adjust this function. The wire requirement is a part of type "image_url"
// whose url field is the data URL string.
func imageURLPart(dataURL string) openai.ChatCompletionContentPartUnionParam {
	return openai.ChatCompletionContentPartUnionParam{
		OfChatCompletionContentPart: openai.ChatCompletionContentPart{
			Type: "image_url",
			ImageURL: openai.ChatCompletionContentPartImageImageURL{
				URL: dataURL,
			},
		},
	}
}
```

> If the installed SDK exposes a convenience constructor (e.g. `openai.ImageURLPart(dataURL)`), prefer it and reduce `imageURLPart` to a one-line wrapper. Confirm via the `grep` shown in the comment.

> **Note on `openai.UserMessage`:** the second argument to `openai.UserMessage` in the extractor (`imageURLPart(dataURL)`) assumes the SDK's `UserMessage` variadic accepts content parts. If the installed version's `UserMessage` only accepts a string, build the user message manually via `openai.ChatCompletionMessageParamUnion{...}` with `MultiContent`. The wire requirement is one user message containing a text part and an `image_url` part.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/ai/extractor/ -v`
Expected: all tests `PASS` — `HappyPath`, `UnreadableImage`, `NoQuestions`, `PartialExtraction`, `NumberLabeling`, `TransportError`, `MalformedJSON`, `FencedJSON`, `Timeout`.

- [ ] **Step 8: Commit**

```bash
git add internal/ai/extractor/
git commit -m "feat(ai/extractor): add Kimi vision extraction client"
```

---

## Task 7: N+1 Query Fix (Independent of Clients)

Replace the per-image `FindByImageID` loop in `ImageHandler.List` with one bulk `FindJobStatusesBySession` call. Adds a `JobQueue` interface method (breaking change for all implementations — the compiler flags every straggler).

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/job_queue.go`
- Modify: `internal/storage/postgres/job_queue_test.go`
- Modify: `internal/httpapi/handlers/images.go`
- Modify: `internal/httpapi/handlers/images_test.go`

### Part A: JobQueue.FindJobStatusesBySession

- [ ] **Step 1: Write the failing storage test**

Append to `internal/storage/postgres/job_queue_test.go`:

```go
func TestJobQueue_FindJobStatusesBySession(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "statuses@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	sess2, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	imgA, _ := imgRepo.Create(ctx, sess.ID, []byte("a"), "image/jpeg", 1, 1)
	imgB, _ := imgRepo.Create(ctx, sess.ID, []byte("b"), "image/jpeg", 1, 1)
	imgOther, _ := imgRepo.Create(ctx, sess2.ID, []byte("c"), "image/jpeg", 1, 1)

	jq.Enqueue(ctx, imgA, sess.ID)
	jq.Enqueue(ctx, imgB, sess.ID)
	jq.Enqueue(ctx, imgOther, sess2.ID)

	// Mark imgA's job as done via Claim+Complete.
	claimed, _ := jq.Claim(ctx)
	if claimed != nil {
		jq.Complete(ctx, claimed.ID)
	}

	statuses, err := jq.FindJobStatusesBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("FindJobStatusesBySession: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("len = %d, want 2 (only sess.ID's images)", len(statuses))
	}
	if statuses[imgA] != domain.JobStatusDone {
		t.Errorf("imgA status = %q, want done", statuses[imgA])
	}
	if statuses[imgB] != domain.JobStatusPending {
		t.Errorf("imgB status = %q, want pending", statuses[imgB])
	}
	if _, present := statuses[imgOther]; present {
		t.Errorf("imgOther (different session) leaked into map")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue_FindJobStatusesBySession -v`
Expected: compile error — `jq.FindJobStatusesBySession undefined`.

- [ ] **Step 3: Add the method to the JobQueue interface**

In `internal/storage/ports.go`, replace the `JobQueue` interface (lines 69–77) with:

```go
// JobQueue manages the Postgres-backed job queue.
type JobQueue interface {
	Enqueue(ctx context.Context, imageID, sessionID string) (string, error)
	Claim(ctx context.Context) (*domain.Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id, errMsg string) error
	ReaperReclaim(ctx context.Context, staleThreshold time.Duration, maxAttempts int) (reclaimed int, failed int, err error)
	FindByImageID(ctx context.Context, imageID string) (*domain.Job, error)
	FindJobStatusesBySession(ctx context.Context, sessionID string) (map[string]string, error)
}
```

- [ ] **Step 4: Implement the method in postgres JobQueue**

Add at the end of `internal/storage/postgres/job_queue.go` (after `FindByImageID`):

```go
// FindJobStatusesBySession returns imageID → status for the newest job of each
// image in the session. DISTINCT ON (image_id) ... ORDER BY image_id, queued_at
// DESC makes the result deterministic even if an image was re-enqueued.
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

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/storage/postgres/ -run TestJobQueue_FindJobStatusesBySession -v`
Expected: `PASS` (Testcontainers spins up).

- [ ] **Step 6: Commit**

```bash
git add internal/storage/ports.go internal/storage/postgres/job_queue.go internal/storage/postgres/job_queue_test.go
git commit -m "feat(storage): add FindJobStatusesBySession bulk job-status lookup"
```

### Part B: Handler rewrite + fakes

- [ ] **Step 7: Add the new method to the handler test fake**

The interface change broke `fakeJobQueueForImages` in `internal/httpapi/handlers/images_test.go`. Add the method and a `statuses` field so the List test can seed it.

In `internal/httpapi/handlers/images_test.go`, replace the `fakeJobQueueForImages` struct definition and its `FindByImageID` method (lines 63–84) with:

```go
type fakeJobQueueForImages struct {
	enqueued   bool
	imageID    string
	sessionID  string
	jobByImage *domain.Job
	statuses   map[string]string
}

func (q *fakeJobQueueForImages) Enqueue(_ context.Context, imageID, sessionID string) (string, error) {
	q.enqueued = true
	q.imageID = imageID
	q.sessionID = sessionID
	return "job-new", nil
}
func (q *fakeJobQueueForImages) Claim(context.Context) (*domain.Job, error) { return nil, nil }
func (q *fakeJobQueueForImages) Complete(context.Context, string) error     { return nil }
func (q *fakeJobQueueForImages) Fail(context.Context, string, string) error { return nil }
func (q *fakeJobQueueForImages) ReaperReclaim(context.Context, time.Duration, int) (reclaimed int, failed int, err error) {
	return 0, 0, nil
}
func (q *fakeJobQueueForImages) FindByImageID(_ context.Context, _ string) (*domain.Job, error) {
	return q.jobByImage, nil
}
func (q *fakeJobQueueForImages) FindJobStatusesBySession(_ context.Context, _ string) (map[string]string, error) {
	return q.statuses, nil
}
```

- [ ] **Step 8: Update TestImageHandler_List to use the bulk map**

In `internal/httpapi/handlers/images_test.go`, replace the body of `TestImageHandler_List` (lines 168–193) with:

```go
func TestImageHandler_List(t *testing.T) {
	imgRepo := &fakeImageRepoFull{
		list: []*domain.Image{
			{ID: "img-1", Mime: "image/png", Width: 100, Height: 200, CreatedAt: "2026-06-20T12:00:00Z"},
		},
	}
	jq := &fakeJobQueueForImages{
		statuses: map[string]string{"img-1": domain.JobStatusDone},
	}
	uploadCfg := config.UploadConfig{}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/sessions/sess-1/images", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp dto.ImageListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Data))
	}
	if resp.Data[0].JobStatus != domain.JobStatusDone {
		t.Errorf("job_status = %q, want done", resp.Data[0].JobStatus)
	}
}
```

- [ ] **Step 9: Rewrite ImageHandler.List to use the bulk lookup**

In `internal/httpapi/handlers/images.go`, replace the entire `List` method (lines 92–127) with:

```go
// List returns all images for the current session along with their job status.
// Job status is fetched in one bulk query (not per-image) to avoid N+1.
func (h *ImageHandler) List(c *gin.Context) {
	sess, exists := c.Get("session")
	if !exists {
		c.JSON(http.StatusInternalServerError, errorResponse(domain.NewError("internal", "session missing")))
		return
	}
	session := sess.(*domain.Session)

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
}
```

- [ ] **Step 10: Run the handler tests to verify they pass**

Run: `go test ./internal/httpapi/handlers/ -run TestImageHandler -v`
Expected: all 3 tests `PASS`.

- [ ] **Step 11: Verify the rest of the tree still compiles**

Run: `go build ./...`
Expected: no output. If any other `storage.JobQueue` fake is missing the new method, the compiler points at it — add `FindJobStatusesBySession` returning `(nil, nil)` or an empty map.

> The pipeline test fakes in `internal/pipeline/pipeline_test.go` use `fakeJobQueue` which embeds the interface only by implementing each method (not via embedding), so the compiler will flag it. Add this method to `fakeJobQueue` in `internal/pipeline/pipeline_test.go`:

```go
func (q *fakeJobQueue) FindJobStatusesBySession(context.Context, string) (map[string]string, error) {
	return nil, nil
}
```

And to `fakeJobQueue` in `internal/pipeline/worker_test.go` if a separate one exists there (grep: `grep -rn "func (.*) FindByImageID" internal/`).

- [ ] **Step 12: Commit**

```bash
git add internal/httpapi/handlers/images.go internal/httpapi/handlers/images_test.go internal/pipeline/pipeline_test.go
git commit -m "perf(httpapi): use bulk job-status lookup in image list (N+1 fix)"
```

---

## Task 8: Application Wiring (wire.go + main.go + App.Close)

Construct all four clients + `Pipeline` + `WorkerPool` in `wire.go` (including `vips.Startup`), call `WorkerPool.Start` in `main.go`, and stop workers → libvips → DB in `App.Close`.

**Files:**
- Modify: `internal/app/wire.go`
- Modify: `cmd/coeus/main.go`

- [ ] **Step 1: Replace wire.go with the wired version**

Replace the entire contents of `internal/app/wire.go` with:

```go
package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/ai/embedder"
	"github.com/vlgrigoriev/coeus/internal/ai/enhancer"
	"github.com/vlgrigoriev/coeus/internal/ai/extractor"
	"github.com/vlgrigoriev/coeus/internal/ai/verifier"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

type App struct {
	Config       *config.Config
	Pool         *pgxpool.Pool
	UserRepo     *postgres.UserRepo
	SessionRepo  *postgres.SessionRepo
	ImageRepo    *postgres.ImageRepo
	QuestionRepo *postgres.QuestionRepo
	JobQueue     *postgres.JobQueue
	JWTMgr       *auth.JWTManager
	Server       *httpapi.Server
	WorkerPool   *pipeline.WorkerPool
}

func Build(ctx context.Context, cfg *config.Config) (*App, error) {
	log := slog.Default()

	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("build pool: %w", err)
	}

	if err := postgres.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	userRepo := postgres.NewUserRepo(pool)
	sessionRepo := postgres.NewSessionRepo(pool)
	imageRepo := postgres.NewImageRepo(pool)
	questionRepo := postgres.NewQuestionRepo(pool)
	jobQueue := postgres.NewJobQueue(pool)
	jwtMgr := auth.NewJWTManager(cfg.JWT)

	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)

	// Initialize libvips before any govips operation (enhancer).
	vips.Startup(nil)

	enh := enhancer.New(log)
	ext := extractor.New(cfg.AI.Vision, log)
	ver := verifier.New(cfg.AI.Reviewer, log)
	emb := embedder.New(cfg.AI.Embedder, log)

	pip := pipeline.NewPipeline(imageRepo, questionRepo, jobQueue,
		enh, ext, ver, emb, cfg.Pipeline, log)

	wp := pipeline.NewWorkerPool(jobQueue, pip,
		cfg.Workers, cfg.Pipeline, cfg.Postgres.DSN, log)

	return &App{
		Config: cfg, Pool: pool,
		UserRepo: userRepo, SessionRepo: sessionRepo,
		ImageRepo: imageRepo, QuestionRepo: questionRepo,
		JobQueue: jobQueue, JWTMgr: jwtMgr, Server: server,
		WorkerPool: wp,
	}, nil
}

// Close stops workers first (so in-flight jobs finish while the DB is reachable),
// then shuts down libvips, then closes the DB pool.
func (a *App) Close() {
	if a.WorkerPool != nil {
		a.WorkerPool.Stop()
	}
	vips.Shutdown()
	if a.Pool != nil {
		a.Pool.Close()
	}
}
```

- [ ] **Step 2: Add WorkerPool.Start to main.go**

In `cmd/coeus/main.go`, add one line after `defer application.Close()` (line 36) and before the `slog.Info("coeus started", ...)` call (line 38). The surrounding block becomes:

```go
	defer application.Close()

	application.WorkerPool.Start(ctx)

	slog.Info("coeus started", "addr", cfg.Server.Addr)
```

- [ ] **Step 3: Verify the full build**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 4: Run go vet**

Run: `go vet ./...`
Expected: no output (clean).

- [ ] **Step 5: Commit**

```bash
git add internal/app/wire.go cmd/coeus/main.go
git commit -m "feat(app): wire AI clients, pipeline, worker pool, libvips lifecycle"
```

---

## Task 9: Full Build + Test Verification

Confirm the whole tree builds, vets clean, and all tests pass (including the existing pipeline tests which must be unaffected by the real-client work).

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 2: Vet everything**

Run: `go vet ./...`
Expected: no output (clean).

- [ ] **Step 3: Run short tests (unit + AI clients + handlers)**

Run: `go test -short ./...`
Expected: all packages `PASS`, no failures.

- [ ] **Step 4: Run full pipeline tests (with Testcontainers)**

Run: `go test ./internal/pipeline/ ./internal/storage/postgres/ -timeout 180s`
Expected: all tests `PASS` (Testcontainers spins up Postgres; may take 30–60s).

- [ ] **Step 5: Confirm interface-satisfaction assertions compiled**

The four `var _ pipeline.AI<Name> = (*<Name>)(nil)` lines in each client package are compile-time checks. If the build in Step 1 succeeded, they held. No extra action.

- [ ] **Step 6: Final commit if anything was touched during verification**

If `go mod tidy` or vet fixes changed anything:
```bash
git add -A
git commit -m "chore: tidy after Plan 3 integration"
```

---

## Task 10: Manual Smoke Test (Documented, Not Automated)

Not part of the automated suite. The executor documents the outcome; it is not enforced by CI.

- [ ] **Step 1: Export real keys**

```bash
export COEUS_POSTGRES_DSN="postgres://..."
export COEUS_JWT_SECRET="..."
export COEUS_AI_VISION_API_KEY="..."      # Moonshot/Kimi
export COEUS_AI_VISION_BASE_URL="https://api.moonshot.cn/v1"
export COEUS_AI_REVIEWER_API_KEY="..."    # DeepSeek
export COEUS_AI_REVIEWER_BASE_URL="https://api.deepseek.com/v1"
export COEUS_AI_EMBEDDER_API_KEY="..."     # OpenAI
```

- [ ] **Step 2: Start the app and upload a sample exam image**

```bash
go run ./cmd/coeus
```
In another terminal: create a session (`POST /api/v1/sessions`), then `POST /api/v1/sessions/:id/images` with a real exam image.

- [ ] **Step 3: Watch the pipeline run end-to-end**

Logs should show: worker claims job → enhance → extract → per-question embed/verify → job done.

- [ ] **Step 4: Verify results via the API**

`GET /api/v1/sessions/:id/images` → `job_status: "done"`.
Query the `questions` / `session_questions` tables to confirm extracted questions, the `verification_report` JSON on the image, and populated embeddings.

- [ ] **Step 5: Record the outcome**

Append a one-line result to the PR description (e.g. "Smoke test passed 2026-06-21: 1 image → 3 questions extracted, verified, embedded").

---

## Self-Review

### Spec coverage

| Spec § | Requirement | Task |
|---|---|---|
| 4.1 | Role-based package layout (`internal/ai/{oai,enhancer,extractor,verifier,embedder}`) | Tasks 2–6 |
| 4.2 | `oai.NewClient(baseURL, apiKey, timeout)` shared factory | Task 2 |
| 5.1 | Rename `KimiConfig`→`VisionConfig`, `DeepSeekConfig`→`ReviewerConfig`, AIConfig fields | Task 1 |
| 5.2 | Env var renames (`COEUS_AI_VISION_*`, `COEUS_AI_REVIEWER_*`) | Task 1 |
| 5.3 | Embedded YAML `kimi`→`vision`, `deepseek`→`reviewer` | Task 1 |
| 5.4 | Required-API-key validation in `Validate()` | Task 1 |
| 5.5 | Embedder timeout constant (no config struct change) | Task 4 (`embedderDefaultTimeout`) |
| 6 | go.mod deps: openai-go, govips, jsonschema | Task 0 |
| 7.1 | Enhancer (govips: AutoRotate, Linear1, Gamma, Sharpen, same-MIME encode) | Task 3 |
| 7.2 | Extractor (vision LLM, multimodal data URL, JSON object response, error taxonomy) | Task 6 |
| 7.2.2 | Extraction system prompt (const) | Task 6 |
| 7.2.3 | Extraction DTOs + `toPipeline` + choice labeling | Task 6 |
| 7.2 | Defensive fence-stripping + parse-error-as-transport | Task 6 (`stripCodeFence`) |
| 7.3 | Verifier (text LLM, JSON object response, no retry) | Task 5 |
| 7.3.2 | Verification system prompt (const) | Task 5 |
| 7.3.3 | Verification DTOs + `fromPipeline`/`toPipeline` by position | Task 5 |
| 7.4 | Embedder (embeddings API, `[]float64`→`[]float32`, dim check) | Task 4 |
| 8.1 | wire.go constructs all clients + Pipeline + WorkerPool + `vips.Startup` | Task 8 |
| 8.2 | main.go calls `WorkerPool.Start(ctx)` | Task 8 |
| 8.3 | App.Close: WorkerPool.Stop → vips.Shutdown → Pool.Close | Task 8 |
| 8.4 | Shutdown order (HTTP → cancel → Close) — already correct, no change | Task 8 (verified) |
| 9.2 | `FindJobStatusesBySession` (`DISTINCT ON`) + handler rewrite | Task 7 |
| 9.3 | Interface + impl + test + handler + handler test touch points | Task 7 |
| 10.1 | httptest + fixture JSON per client | Tasks 4–6 |
| 10.2 | Config tests updated + missing-key subtests | Task 1 |
| 10.3 | N+1 fix tests (Testcontainers + handler fake) | Task 7 |
| 10.4 | Existing pipeline tests unaffected | Task 9 (verify) |
| 10.5 | `go build`, `go vet`, `go test ./...` | Task 9 |
| 10.6 | Manual smoke test | Task 10 |
| 13 | Dependency-respecting implementation order | Tasks 0→10 |

### Placeholder scan

- No TBD / TODO / "implement later" in any step.
- Every code step shows the complete implementation.
- Every test step shows real assertions.
- The two SDK-version reconciliation points (embedder input union, extractor image-part union, verifier/extractor message constructors) carry an explicit discovery `grep` and a stated wire requirement — these are genuine external-version dependencies, not placeholders. Each is isolated in its own file (`input.go`, `imagepart.go`) so a version bump touches one function.

### Type consistency

- `config.VisionConfig` / `config.ReviewerConfig` / `config.EmbedderConfig` — used identically in config.go, the client constructors (`New(cfg config.VisionConfig, …)` etc.), and the wiring (`cfg.AI.Vision`, `cfg.AI.Reviewer`, `cfg.AI.Embedder`).
- `pipeline.ImageEnhancer` / `AIExtractor` / `AIVerifier` / `AIEmbedder` — ports are unchanged from Plan 2; each client declares `var _ pipeline.AI<Name> = (*<Name>)(nil)`.
- `pipeline.ExtractResult` / `ExtractionError` / `VerifyResult` / `VerifiedQuestion` / `Answer` / `ExtractedQuestion` — consumed identically by the clients' `toPipeline`/`fromPipeline` and by the existing `pipeline.go`.
- Extraction error codes (`unreadable_image`, `no_questions_found`, `partial_extraction`) — produced by the extractor (Task 6) and consumed by `pipeline.extractWithRetries` (unchanged); values match `pipeline.ExtractionCode*` constants.
- `oai.NewClient(baseURL, apiKey string, timeout time.Duration) *openai.Client` — signature matches all three call sites (extractor, verifier, embedder).
- `pipeline.NewWorkerPool(jobs, pipeline, workersCfg, pipelineCfg, dsn, log)` — 6-arg signature matches worker.go and the wiring call in Task 8.
- `WorkerPool.Start(ctx)` / `WorkerPool.Stop()` — match worker.go and the main.go/Close call sites in Task 8.
- `JobQueue.FindJobStatusesBySession(ctx, sessionID) (map[string]string, error)` — matches ports.go, job_queue.go, and both fakes (handler test + pipeline test).
- `Embedder.Embed` returns `[]float32`; `FindSemantic` expects `[]float32` — consistent with pipeline.go.
