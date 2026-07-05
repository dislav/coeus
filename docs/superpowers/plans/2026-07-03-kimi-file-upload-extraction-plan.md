# Kimi file-upload extraction (Approach A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace base64-inline image extraction with Moonshot file-upload + `ms://` reference, disable `thinking` by default, and add exponential backoff between extraction retries — eliminating connection-reset failures through lossy proxies.

**Architecture:** `Extract` now uploads the image via a raw multipart `POST {baseURL}/files`, references it as `ms://<file_id>` in a tiny chat-completions call, and best-effort `DELETE`s it afterward (using a context decoupled from caller cancellation). The pipeline's `extractWithRetries` sleeps with an injectable, jittered exponential backoff between attempts (honoring context cancellation). A new `VisionConfig.Thinking` flag (default `false`) drives a `thinking:{type:disabled}` extra field on the chat params.

**Tech Stack:** Go 1.26.3, stdlib `net/http` + `mime/multipart` for file ops, `github.com/openai/openai-go` v1.12.0 (typed params, `SetExtraFields` for the non-standard `thinking` field), `math/rand/v2` for jitter, `httptest` for all tests.

**Source of truth:** `docs/superpowers/specs/2026-07-03-kimi-file-upload-extraction-design.md` (approved). This plan implements it verbatim — do not re-open any decision.

**Verification gate (this plan):** `go test -short ./...` and `go vet ./...`. **Do NOT run the integration tests** (`internal/storage/postgres/`, `internal/pipeline/` without `-short`) — they need Docker. **CGO + libvips** are required for a full `go build ./...`; if a full build fails locally due to missing libvips headers, that is an environment issue — fall back to per-package `go vet`/`go build` on the touched packages (`./internal/config/`, `./internal/ai/extractor/`, `./internal/pipeline/`, `./internal/app/`) and note it.

**Commits:** Each task ends with a commit step following repo conventions (`feat:`/`refactor:`/`test:` prefixes). `*.md` and `docs/` are gitignored — only the Go files are committed.

---

## File Structure

| File | Responsibility | Action |
|------|----------------|--------|
| `internal/config/config.go` | Add `VisionConfig.Thinking bool`; parse `COEUS_AI_VISION_THINKING` env (true\|false\|1\|0). | Modify |
| `internal/config/config.yaml` | Add `thinking: false` default under `ai.vision`. | Modify |
| `internal/config/config_test.go` | Table test for the env parse + invalid-value error. | Create |
| `internal/ai/extractor/files.go` | `uploadImage` (raw multipart POST), `deleteFile` (raw DELETE), `filenameForMime` helper. stdlib only. | Create |
| `internal/ai/extractor/files_test.go` | multipart shape, MIME→ext, isolated DELETE, cleanup-on-cancel. | Create |
| `internal/ai/extractor/extractor.go` | New `Extractor` fields; rewrite `Extract` (upload → chat(`ms://`) → parse); inject `thinking:disabled`. | Modify |
| `internal/ai/extractor/imagepart.go` | Generalize doc + param name to any URL string. | Modify |
| `internal/ai/extractor/extractor_test.go` | Replace single-path `chatServer` with a routing `kimiServer`; add upload-failure, DELETE-recorded, thinking-enabled cases; update happy path to assert `ms://` + `thinking`. | Modify |
| `internal/pipeline/pipeline.go` | `defaultBackoff` pure func + `backoff` field on `Pipeline`; wire jittered sleep into `extractWithRetries`. | Modify |
| `internal/pipeline/pipeline_test.go` | Pure backoff bounds; retry-spacing with injected backoff + `flakyExtractor`; backoff abort on cancelled ctx. | Modify |
| `internal/app/wire.go` | No code change expected (`extractor.New` signature unchanged); verify it still compiles. | Verify |

---

## Task 1: Config — `VisionConfig.Thinking` + env override

**Files:**
- Modify: `internal/config/config.go` (`VisionConfig` struct ~line 65-70; `applyEnvOverrides` ~line 126-128)
- Modify: `internal/config/config.yaml` (~line 22-25, `ai.vision`)
- Test: `internal/config/config_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import "testing"

func TestApplyEnvOverrides_VisionThinking(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"true", "true", true},
		{"one", "1", true},
		{"TRUE", "TRUE", true},
		{"false", "false", false},
		{"zero", "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COEUS_AI_VISION_THINKING", tt.in)
			var cfg Config
			if err := applyEnvOverrides(&cfg); err != nil {
				t.Fatalf("applyEnvOverrides: %v", err)
			}
			if cfg.AI.Vision.Thinking != tt.want {
				t.Errorf("Thinking = %v, want %v", cfg.AI.Vision.Thinking, tt.want)
			}
		})
	}
}

func TestApplyEnvOverrides_VisionThinkingInvalid(t *testing.T) {
	t.Setenv("COEUS_AI_VISION_THINKING", "yes")
	var cfg Config
	if err := applyEnvOverrides(&cfg); err == nil {
		t.Fatal("expected error for invalid COEUS_AI_VISION_THINKING, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -short ./internal/config/`
Expected: FAIL — `cfg.AI.Vision.Thinking` undefined (field does not exist yet).

- [ ] **Step 3: Add the field and the env parse**

In `internal/config/config.go`, change `VisionConfig` to (note the re-aligned type column):

```go
type VisionConfig struct {
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`
	Timeout  time.Duration `yaml:"timeout"`
	Thinking bool          `yaml:"thinking"`
}
```

In `applyEnvOverrides`, insert this block immediately after the `COEUS_AI_VISION_BASE_URL` block (after the existing `cfg.AI.Vision.BaseURL = v` line):

```go
	if v := os.Getenv("COEUS_AI_VISION_THINKING"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			cfg.AI.Vision.Thinking = true
		case "false", "0":
			cfg.AI.Vision.Thinking = false
		default:
			return fmt.Errorf("invalid COEUS_AI_VISION_THINKING %q: expected true|false|1|0", v)
		}
	}
```

- [ ] **Step 4: Add the YAML default**

In `internal/config/config.yaml`, under `ai.vision`, add `thinking: false`:

```yaml
ai:
  vision:
    model: "kimi-k2.6"
    timeout: 90s
    thinking: false   # k2.6 defaults to enabled; disabled cuts latency
```

- [ ] **Step 5: Run test to verify it passes + vet**

Run: `go test -short ./internal/config/`
Expected: PASS (2 subtests + the invalid case).

Run: `go vet ./internal/config/`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config.yaml internal/config/config_test.go
git commit -m "feat(config): add ai.vision.thinking flag + COEUS_AI_VISION_THINKING env"
```

---

## Task 2: `files.go` — raw upload / delete / MIME helper

**Files:**
- Create: `internal/ai/extractor/files.go`
- Test: `internal/ai/extractor/files_test.go` (create)

> **Note:** The existing `New` constructor does not yet populate the `baseURL`/`apiKey`/`httpClient` fields this task needs — those are added in Task 3. To make `files.go` independently testable, Task 2 introduces the methods on `*Extractor` but the tests construct an `Extractor` via `New` and rely on the *current* struct. Because Task 3 immediately follows and adds the fields, the simplest ordering is: **add the struct fields + populate them in `New` as part of Task 2** (a small mechanical edit), then Task 3 changes only `Extract` + imports. If you prefer, do the field-add here so `files.go` compiles and tests green before Task 3.

- [ ] **Step 1: Add the `Extractor` fields + populate them in `New`**

In `internal/ai/extractor/extractor.go`, update the struct and constructor:

```go
type Extractor struct {
	client     *openai.Client
	model      string
	baseURL    string
	apiKey     string
	httpClient *http.Client
	thinking   bool
	log        *slog.Logger
}

func New(cfg config.VisionConfig, log *slog.Logger) *Extractor {
	if log == nil {
		log = slog.Default()
	}
	return &Extractor{
		client:     oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout),
		model:      cfg.Model,
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		thinking:   cfg.Thinking,
		log:        log,
	}
}
```

Add `"net/http"` to the import block of `extractor.go`. (Leave `Extract` unchanged for now — Task 3 rewrites it. The `encoding/base64` import is still used by the current `Extract`, so keep it until Task 3.)

- [ ] **Step 2: Write the failing tests**

Create `internal/ai/extractor/files_test.go`:

```go
package extractor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFilenameForMime(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/jpeg", "image.jpg"},
		{"image/png", "image.png"},
		{"image/webp", "image.webp"},
		{"image/gif", "image.bin"}, // unknown → default
		{"", "image.bin"},
	}
	for _, tt := range tests {
		if got := filenameForMime(tt.mime); got != tt.want {
			t.Errorf("filenameForMime(%q) = %q, want %q", tt.mime, got, tt.want)
		}
	}
}

func TestUploadImage_MultipartShape(t *testing.T) {
	var (
		gotPurpose  string
		gotFilename string
		fileBytes   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotPurpose = r.FormValue("purpose")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			return
		}
		defer f.Close()
		gotFilename = hdr.Filename
		fileBytes, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-1", "status": "ready"})
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 10*time.Second), quietLogger())
	id, err := e.uploadImage(context.Background(), []byte("PNGDATA"), "image/png")
	if err != nil {
		t.Fatalf("uploadImage: %v", err)
	}
	if id != "file-1" {
		t.Errorf("id = %q, want file-1", id)
	}
	if gotPurpose != "image" {
		t.Errorf("purpose = %q, want image", gotPurpose)
	}
	if gotFilename != "image.png" {
		t.Errorf("filename = %q, want image.png", gotFilename)
	}
	if string(fileBytes) != "PNGDATA" {
		t.Errorf("file bytes = %q, want PNGDATA", string(fileBytes))
	}
}

func TestUploadImage_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 10*time.Second), quietLogger())
	_, err := e.uploadImage(context.Background(), []byte("x"), "image/png")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500, got: %v", err)
	}
}

func TestDeleteFile_Isolation(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 10*time.Second), quietLogger())
	if err := e.deleteFile(context.Background(), "file-9"); err != nil {
		t.Fatalf("deleteFile: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/files/file-9" {
		t.Errorf("path = %q, want /files/file-9", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q, want Bearer test-key", gotAuth)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -short ./internal/ai/extractor/`
Expected: FAIL — `filenameForMime`, `e.uploadImage`, `e.deleteFile` undefined.

- [ ] **Step 4: Implement `files.go`**

Create `internal/ai/extractor/files.go`:

```go
package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// filenameForMime derives an upload filename from the image MIME type. Moonshot
// only needs a reasonable extension; the base name is arbitrary.
func filenameForMime(mime string) string {
	ext := ".bin"
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	}
	return "image" + ext
}

// uploadImage POSTs the raw image bytes to {baseURL}/files as multipart
// form-data with purpose=image and returns the Moonshot file id. Any non-2xx
// response is returned as a transport error so the pipeline retries it.
func (e *Extractor) uploadImage(ctx context.Context, image []byte, mime string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("purpose", "image"); err != nil {
		return "", fmt.Errorf("upload: write purpose: %w", err)
	}
	part, err := mw.CreateFormFile("file", filenameForMime(mime))
	if err != nil {
		return "", fmt.Errorf("upload: create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(image)); err != nil {
		return "", fmt.Errorf("upload: copy bytes: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("upload: close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/files", &buf)
	if err != nil {
		return "", fmt.Errorf("upload: new request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("upload: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var fr struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return "", fmt.Errorf("upload: decode response: %w", err)
	}
	if fr.ID == "" {
		return "", fmt.Errorf("upload: empty file id in response")
	}
	return fr.ID, nil
}

// deleteFile removes an uploaded file via DELETE {baseURL}/files/{id}.
// Best-effort: callers log the returned error and never propagate it.
func (e *Extractor) deleteFile(ctx context.Context, fileID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+"/files/"+fileID, nil)
	if err != nil {
		return fmt.Errorf("delete file %s: new request: %w", fileID, err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", fileID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("delete file %s: unexpected status %d", fileID, resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -short ./internal/ai/extractor/`
Expected: PASS — the new `files_test.go` cases pass; the existing `extractor_test.go` cases still pass (because `Extract` is still the base64 path). Note: the existing `chatServer`-based tests still work because `Extract` has not been rewritten yet.

Run: `go vet ./internal/ai/extractor/`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/extractor/files.go internal/ai/extractor/files_test.go internal/ai/extractor/extractor.go
git commit -m "feat(extractor): add raw file upload/delete helpers + extractor fields"
```

---

## Task 3: Rewrite `Extract` (upload → chat(`ms://`) → parse) + `thinking`

**Files:**
- Modify: `internal/ai/extractor/extractor.go` (`Extract` body, imports, package doc comment)
- Modify: `internal/ai/extractor/imagepart.go` (doc + param name)
- Modify: `internal/ai/extractor/extractor_test.go` (routing server + new assertions)

> **Error-handling contract (from spec §4.3 — preserve verbatim):**
> - **transport failure** (upload error, chat HTTP/network error, JSON-parse failure) → `(zero, err)` → retried by `extractWithRetries`.
> - **content failure** (`error != nil` in model JSON) → `(result, nil)` → handled by pipeline code.
> - **success** → `(result, nil)`.
> - `deleteFile` errors are **logged at Warn and never returned**. The delete is deferred so it runs on both success and chat failure.
> - **Upload non-2xx is a transport error** (retried) — uniform with chat errors.
>
> **Cleanup context:** the deferred delete derives its own context via `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)`, so it runs even when the caller ctx is cancelled/expired, but is bounded to 5s. (`context.WithoutCancel` is Go 1.21+; repo is on 1.26.3.)
>
> **Open implementation question (note only — already handled):** whether Moonshot accepts `DELETE /v1/files/{id}` is unconfirmed. It is best-effort + logged (see Risks in spec). No code action needed; the fallback is already in place.

- [ ] **Step 1: Update `imagepart.go` (generalize to any URL)**

Replace the contents of `internal/ai/extractor/imagepart.go` with:

```go
package extractor

import (
	"github.com/openai/openai-go"
)

// imageURLPart builds the image_url content part carrying a URL. The URL may be
// a base64 data URL or a Moonshot Storage reference of the form "ms://<file_id>".
//
// openai-go's ChatCompletionContentPartUnionParam is a generated tagged union
// whose exact discriminator field varies across SDK patch versions. If the
// build fails here, discover the correct field with:
//
//	grep -rn "ChatCompletionContentPartUnionParam\|ImageURL\b" \
//	    $(go env GOMODCACHE)/github.com/openai/openai-go*
//
// and adjust this function. The wire requirement is a part of type "image_url"
// whose url field is the given URL string.
func imageURLPart(url string) openai.ChatCompletionContentPartUnionParam {
	return openai.ImageContentPart(
		openai.ChatCompletionContentPartImageImageURLParam{
			URL: url,
		},
	)
}
```

- [ ] **Step 2: Write the new/failing tests first (rewrite the test helper + assertions)**

Open `internal/ai/extractor/extractor_test.go`. Replace the `chatServer` helper (lines ~23-54) with a routing `kimiServer` that handles `/files` (POST), `/files/{id}` (DELETE), and `/chat/completions` (POST), and returns pointers for the last chat body, the upload-call count, and the delete-call count:

```go
// kimiServer returns a routing httptest server mimicking the Moonshot endpoints
// used by Extract. It captures the last chat request body and counts upload and
// delete calls. The upload always returns id "file-test-1".
func kimiServer(t *testing.T, chatStatus int, chatContent string) (*httptest.Server, *string, *int, *int) {
	t.Helper()
	var (
		lastBody    string
		uploadCalls int
		deleteCalls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == http.MethodPost:
			uploadCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-test-1", "status": "ready"})
		case strings.HasPrefix(r.URL.Path, "/files/") && r.Method == http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/chat/completions" && r.Method == http.MethodPost:
			reqBody, _ := io.ReadAll(r.Body)
			lastBody = string(reqBody)
			if chatStatus != http.StatusOK {
				w.WriteHeader(chatStatus)
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
							"content": chatContent,
						},
						"finish_reason": "stop",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &lastBody, &uploadCalls, &deleteCalls
}
```

Now update every existing test that calls `chatServer(...)` to call `kimiServer(...)` and drop the second ignored return. They keep working because the upload/delete routes now answer. Example for `TestExtractor_HappyPath`:

```go
	srv, body, _, delCalls := kimiServer(t, http.StatusOK, content)
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	res, err := e.Extract(context.Background(), []byte("fake-image"), "image/png")
```

Concretely, replace across the file: `chatServer(t, X, Y)` → `kimiServer(t, X, Y)`, and adjust the `defer srv.Close()` + variable names as needed. For tests that previously did `srv, _ := chatServer(...)`, use `srv, _, _, _ := kimiServer(...)`.

Then **update `TestExtractor_HappyPath`'s body assertions** to the new contract:

```go
	if !strings.Contains(*body, "ms://file-test-1") {
		t.Errorf("request body missing ms:// reference:\n%s", *body)
	}
	if !strings.Contains(*body, `"thinking":{"type":"disabled"}`) {
		t.Errorf("request body missing thinking:disabled:\n%s", *body)
	}
	if !strings.Contains(*body, `"json_object"`) {
		t.Errorf("request body missing json_object response_format:\n%s", *body)
	}
	if !strings.Contains(*body, `"kimi-k2.7"`) {
		t.Errorf("request body missing model:\n%s", *body)
	}
	if *delCalls != 1 {
		t.Errorf("expected 1 DELETE after success, got %d", *delCalls)
	}
```

(The old `image_url` substring assertion is dropped — `ms://` is the new reference.)

Now **add** these new test functions at the end of the file:

```go
func TestExtractor_UploadFailureNoChatCall(t *testing.T) {
	srv, _, upCalls, chatSrv := uploadFailingFilesServer(t, http.StatusInternalServerError)
	defer srv.Close()
	_ = chatSrv

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	_, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected upload transport error, got nil")
	}
	_ = upCalls
}
```

(That helper isn't ideal — instead, drive the failure through the routing server. Replace the above with the concrete version below.)

**Use this concrete version instead** of the upload-failure test:

```go
func TestExtractor_UploadFailureNoChatCall(t *testing.T) {
	var chatCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"down"}`))
		case r.URL.Path == "/chat/completions" && r.Method == http.MethodPost:
			chatCalls++
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	_, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected upload transport error, got nil")
	}
	if chatCalls != 0 {
		t.Errorf("chat endpoint should not be hit on upload failure, got %d calls", chatCalls)
	}
}
```

**Chat-failure still records DELETE:**

```go
func TestExtractor_ChatFailureStillDeletes(t *testing.T) {
	srv, _, _, delCalls := kimiServer(t, http.StatusInternalServerError, "")
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	_, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err == nil {
		t.Fatal("expected chat transport error, got nil")
	}
	if *delCalls != 1 {
		t.Errorf("expected DELETE after chat failure, got %d", *delCalls)
	}
}
```

**Thinking-enabled path omits the `thinking` key:**

```go
func TestExtractor_ThinkingEnabledOmitsKey(t *testing.T) {
	content := `{"questions":[{"number":1,"question":"q","choices":["a"],"answers":[{"id":"A","value":"a"}],"confidence":0.8,"tags":["x"]}]}`
	srv, body, _, _ := kimiServer(t, http.StatusOK, content)
	defer srv.Close()

	cfg := testCfg(srv.URL, 30*time.Second)
	cfg.Thinking = true
	e := New(cfg, quietLogger())

	res, err := e.Extract(context.Background(), []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(res.Questions))
	}
	if strings.Contains(*body, "thinking") {
		t.Errorf("thinking-enabled request must NOT contain a thinking key:\n%s", *body)
	}
	if !strings.Contains(*body, "ms://file-test-1") {
		t.Errorf("thinking-enabled request still uses ms:// reference:\n%s", *body)
	}
}
```

Remove the now-unused `chatServer` function entirely (it is fully replaced by `kimiServer`). Remove any `// chatServer ...` doc comment too.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -short ./internal/ai/extractor/`
Expected: FAIL — `Extract` still uses base64, so the `ms://` / `thinking` assertions fail; `TestExtractor_UploadFailureNoChatCall` fails because the chat path is reached (no upload yet).

- [ ] **Step 4: Rewrite `Extract` and update imports**

In `internal/ai/extractor/extractor.go`:

1. Update the package doc comment (line 2-3) to:
```go
// Package extractor implements pipeline.AIExtractor using a vision LLM via the
// OpenAI-compatible Chat Completions API (Moonshot/Kimi by default). The image
// is uploaded to Moonshot Files and referenced as ms://<file_id> in a
// multimodal user message.
```

2. Replace the import block so it is exactly:
```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)
```
(`encoding/base64` is removed — no longer used; `net/http` and `time` are added.)

3. Replace the entire `Extract` method with:

```go
// Extract uploads the image, sends it to the vision model as an ms:// reference,
// and returns an ExtractResult. The uploaded file is deleted (best-effort) on
// return.
//
// Return-shape contract (consumed by pipeline.extractWithRetries):
//   - transport failure (upload error, HTTP/network/timeout, JSON parse)  → (zero, err)   [retried]
//   - content failure (Error != nil in model JSON)                       → (result, nil) [by code]
//   - success                                                            → (result, nil)
//
// "parse model JSON" failure is treated as transport-class so the pipeline
// retries — a prose-wrapped response may succeed on the next attempt.
func (e *Extractor) Extract(ctx context.Context, image []byte, mime string) (pipeline.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}

	fileID, err := e.uploadImage(ctx, image, mime)
	if err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: upload: %w", err)
	}
	defer func() {
		// Cleanup context decoupled from caller cancellation so a cancelled
		// request cannot leak the uploaded file, but bounded to 5s because the
		// DELETE travels through the same lossy proxy that may reset it.
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := e.deleteFile(delCtx, fileID); err != nil {
			e.log.Warn("delete uploaded file", "file_id", fileID, "error", err)
		}
	}()

	userText := "Extract all questions from this exam image. Respond as a JSON object matching this schema:\n\n" + extractionSchemaJSON

	rf := shared.NewResponseFormatJSONObjectParam()
	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(e.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("```schema\n" + userText + "\n```\n\nNow extract from this image:"),
				imageURLPart("ms://" + fileID),
			}),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &rf,
		},
	}
	// k2.6 enables "thinking" by default; disabling it cuts latency. The field
	// is Moonshot-specific and injected via SetExtraFields (a promoted method on
	// every generated param in openai-go v1.12.0).
	if !e.thinking {
		params.SetExtraFields(map[string]any{
			"thinking": map[string]any{"type": "disabled"},
		})
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -short ./internal/ai/extractor/`
Expected: PASS — all existing cases (UnreadableImage, NoQuestions, PartialExtraction, NumberLabeling, TransportError, MalformedJSON, FencedJSON, Timeout) now go through upload→chat→delete and still satisfy their assertions; the 4 new/updated cases pass.

Run: `go vet ./internal/ai/extractor/`
Expected: no issues.

> **Timeout test note:** `TestExtractor_Timeout` sets `cfg.Timeout=50ms` against a chat handler that sleeps 200ms. The upload is fast (httptest local), so the chat call still times out first — the test continues to assert an error. No change needed beyond switching it to `kimiServer`.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/extractor/extractor.go internal/ai/extractor/imagepart.go internal/ai/extractor/extractor_test.go
git commit -m "refactor(extractor): upload image + ms:// reference, disable thinking by default"
```

---

## Task 4: Pipeline backoff in `extractWithRetries`

**Files:**
- Modify: `internal/pipeline/pipeline.go` (struct field, constructor, `extractWithRetries`, new `defaultBackoff`, imports)
- Modify: `internal/pipeline/pipeline_test.go` (pure backoff test, `flakyExtractor`, retry-spacing, cancel-abort)

> The pure unit tests here run under `go test -short ./internal/pipeline/` (they use fakes, no Docker). The package's Docker-gated integration tests self-skip under `-short`.

- [ ] **Step 1: Write the failing tests**

In `internal/pipeline/pipeline_test.go`, add a `flakyExtractor` stub near the existing `fakeExtractor`:

```go
// flakyExtractor fails the first failN attempts with a transport error, then
// succeeds. Used to exercise retry spacing and backoff.
type flakyExtractor struct {
	failN int
	calls int
}

func (f *flakyExtractor) Extract(ctx context.Context, _ []byte, _ string) (ExtractResult, error) {
	f.calls++
	if ctx.Err() != nil {
		return ExtractResult{}, ctx.Err()
	}
	if f.calls <= f.failN {
		return ExtractResult{}, errors.New("transport error")
	}
	return ExtractResult{Questions: sampleQuestions()[:1]}, nil
}
```

Add these test functions at the end of the file:

```go
func TestDefaultBackoff_Bounds(t *testing.T) {
	// base=1s, factor=2, cap=8s: centers are 1s, 2s, 4s, 8s, 8s... with ±20% jitter.
	for attempt := 1; attempt <= 6; attempt++ {
		d := defaultBackoff(attempt)
		var center time.Duration
		if attempt >= 4 {
			center = 8 * time.Second
		} else {
			center = time.Duration(1 << (attempt - 1)) * time.Second // 1s, 2s, 4s
		}
		lo := time.Duration(float64(center) * 0.8)
		hi := time.Duration(float64(center) * 1.2)
		if d < lo || d > hi {
			t.Errorf("attempt %d: backoff = %v, want within [%v, %v]", attempt, d, lo, hi)
		}
	}
	// Monotonic non-decreasing across attempts even with jitter (adjacent
	// attempts' jitter bands do not overlap).
	var prev time.Duration
	for attempt := 1; attempt <= 6; attempt++ {
		d := defaultBackoff(attempt)
		if attempt > 1 && d < prev {
			t.Errorf("attempt %d: backoff %v < previous %v (not monotonic)", attempt, d, prev)
		}
		prev = d
	}
}

func TestExtractWithRetries_BackoffSpacing(t *testing.T) {
	ext := &flakyExtractor{failN: 2} // succeeds on attempt 3
	p, _, _, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})
	// Inject a tiny backoff so the test is fast; real default would sleep ~1s, ~2s.
	p.backoff = func(int) time.Duration { return 1 * time.Millisecond }

	start := time.Now()
	res, err := p.extractWithRetries(context.Background(), []byte("img"), "image/png")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success after 3 attempts, got err: %v", err)
	}
	if ext.calls != 3 {
		t.Errorf("calls = %d, want 3", ext.calls)
	}
	if len(res.Questions) != 1 {
		t.Errorf("questions = %d, want 1", len(res.Questions))
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, want < 100ms (injected 1ms backoff × 2 sleeps)", elapsed)
	}
}

func TestExtractWithRetries_BackoffAbortsOnCancel(t *testing.T) {
	ext := &flakyExtractor{failN: 100} // always fails
	p, _, _, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})
	// Long backoff so the first sleep is in flight when we cancel.
	p.backoff = func(int) time.Duration { return 1 * time.Second }

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.extractWithRetries(ctx, []byte("img"), "image/png")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if ext.calls > 2 {
		t.Errorf("calls = %d, want <= 2 (should abort during first backoff sleep)", ext.calls)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (abort during first 1s sleep)", elapsed)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/pipeline/`
Expected: FAIL — `p.backoff` undefined (field not on `Pipeline`); `defaultBackoff` undefined; `p.extractWithRetries` doesn't sleep.

- [ ] **Step 3: Implement `defaultBackoff` + the `backoff` field**

In `internal/pipeline/pipeline.go`:

1. Add `"time"` and `"math/rand/v2"` to the import block:
```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)
```

2. Add the field to the `Pipeline` struct (after `log *slog.Logger`):
```go
type Pipeline struct {
	images    storage.ImageRepo
	questions storage.QuestionRepo
	jobs      storage.JobQueue
	enhancer  ImageEnhancer
	extractor AIExtractor
	verifier  AIVerifier
	embedder  AIEmbedder
	cfg       config.PipelineConfig
	log       *slog.Logger
	backoff   func(attempt int) time.Duration
}
```

3. In `NewPipeline`, set `backoff: defaultBackoff` in the returned struct:
```go
	return &Pipeline{
		images: images, questions: questions, jobs: jobs,
		enhancer: enhancer, extractor: extractor, verifier: verifier, embedder: embedder,
		cfg: cfg, log: log,
		backoff: defaultBackoff,
	}
```

4. Add the pure helper near `extractWithRetries`:
```go
const (
	backoffBase = 1 * time.Second
	backoffCap  = 8 * time.Second
)

// defaultBackoff returns a jittered exponential backoff for the given attempt
// (1-based). Base 1s, factor 2, cap 8s → centers 1s, 2s, 4s, 8s, 8s..., each
// with ±20% uniform jitter. Pure (no I/O); injectable via Pipeline.backoff.
func defaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := backoffBase << (attempt - 1) // base * 2^(attempt-1)
	if d > backoffCap || d < 0 {
		d = backoffCap
	}
	factor := 0.8 + rand.Float64()*0.4 // [0.8, 1.2)
	return time.Duration(float64(d) * factor)
}
```

- [ ] **Step 4: Wire the sleep into `extractWithRetries`**

Replace the body of `extractWithRetries` with a version that sleeps (honoring ctx) before the next attempt whenever a retry is warranted and this isn't the last attempt:

```go
// extractWithRetries calls Extract up to ExtractMaxAttempts times.
// Retries on unreadable_image, no_questions_found, and transport errors.
// partial_extraction and unknown codes are terminal (no retry).
// Between retried attempts it sleeps per Pipeline.backoff (exponential +
// jitter by default); the sleep honors ctx cancellation.
func (p *Pipeline) extractWithRetries(ctx context.Context, image []byte, mime string) (ExtractResult, error) {
	var result ExtractResult
	var lastErr error
	for attempt := 1; attempt <= p.cfg.ExtractMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result, lastErr = p.extractor.Extract(ctx, image, mime)
		if lastErr == nil && result.Error == nil {
			return result, nil
		}

		retry := false
		if lastErr == nil && result.Error != nil {
			switch result.Error.Code {
			case ExtractionCodePartial:
				return result, nil // terminal
			case ExtractionCodeUnreadableImage, ExtractionCodeNoQuestions:
				p.log.Warn("extract retryable failure", "attempt", attempt, "code", result.Error.Code)
				retry = true
			default:
				return result, nil // terminal
			}
		} else {
			p.log.Warn("extract error", "attempt", attempt, "error", lastErr)
			retry = true
		}

		// No next attempt → stop without sleeping.
		if !retry || attempt == p.cfg.ExtractMaxAttempts {
			break
		}

		select {
		case <-time.After(p.backoff(attempt)):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	if lastErr != nil {
		return result, lastErr
	}
	return result, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -short ./internal/pipeline/`
Expected: PASS — the 3 new backoff tests pass, and all existing pipeline tests (HappyPath, ExactDedup, SemanticDedup, UnreadableThreeAttempts, Partial, VerifierFailure, ShutdownDuringExtract, EmbedderFailure, AnswersValueOnly, AIGeneratedTag) still pass. (The default backoff field is set in `NewPipeline`; existing tests construct via `testPipeline` → `NewPipeline`, so they get the real `defaultBackoff`. They exercise paths that either succeed first try, hit a terminal code, or retry on `unreadable_image` 3× — those now sleep ~1s and ~2s between attempts. If those existing tests slow down noticeably, that is expected and acceptable; if CI time matters, they can be sped up by injecting `p.backoff = func(int) time.Duration { return 0 }` in `testPipeline`.)

> **Optional polish (recommended):** to keep the existing `TestPipeline_UnreadableThreeAttemptsPlaceholder` fast, set a zero backoff in `testPipeline`:
> ```go
> 	p := NewPipeline(imgRepo, qRepo, jq, enh, ext, ver, emb,
> 		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())
> 	p.backoff = func(int) time.Duration { return 0 }
> 	return p, imgRepo, qRepo, jq
> ```
> Do this only if the existing retry test becomes slow; otherwise leave as-is. This does not weaken the dedicated backoff tests, which inject their own `p.backoff`.

Run: `go vet ./internal/pipeline/`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/pipeline/pipeline.go internal/pipeline/pipeline_test.go
git commit -m "feat(pipeline): exponential+jitter backoff between extraction retries"
```

---

## Task 5: Wiring verification + full `-short` suite

**Files:**
- Verify: `internal/app/wire.go` (no change expected — `extractor.New(cfg.AI.Vision, log)` still compiles with the unchanged signature)

- [ ] **Step 1: Confirm wiring compiles**

Run: `go vet ./internal/app/`
Expected: no issues. (`wire.go:72` calls `extractor.New(cfg.AI.Vision, log)`; the signature is unchanged, and `cfg.AI.Vision` now carries `Thinking` automatically.)

- [ ] **Step 2: Run the full `-short` unit suite**

Run: `go test -short ./...`
Expected: PASS across `internal/config`, `internal/ai/extractor`, `internal/pipeline`, and all other packages. (If a full `go build ./...` fails only due to missing libvips/CGO headers in this environment, that is expected — the `-short` Go tests still compile and run the touched packages.)

- [ ] **Step 3: Run full vet**

Run: `go vet ./...`
Expected: no issues (same CGO caveat as above for packages that import govips; the touched packages vet cleanly).

- [ ] **Step 4: Commit (if any formatting/whitespace fixups were made; otherwise skip)**

Only commit if a file changed in this task:
```bash
git add -A
git commit -m "chore: verify wiring + full short test suite" || echo "nothing to commit"
```

---

## Self-Review (run after writing the plan — results recorded here)

**1. Spec coverage:**
- §4.1 component table → Task 1 (config), Task 2 (files.go), Task 3 (extractor.go + imagepart.go), Task 4 (pipeline.go). ✓
- §4.2 `Extract` data flow (upload → chat(`ms://`) → parse, deferred delete with `WithoutCancel`+5s) → Task 3 Step 4. ✓
- §4.3 error-handling contract (transport→retried, content→nil, delete logged) → Task 3 header block, verbatim. ✓ Upload non-2xx as transport error → Task 2 Step 4 + Task 3. ✓
- §4.4 backoff (base 1s, cap 8s, factor 2, ±20% jitter, injectable field, ctx-honoring sleep, partial/unknown terminal) → Task 4 Step 3-4. ✓
- §4.5 config (`Thinking bool`, default false, env `COEUS_AI_VISION_THINKING`) → Task 1. ✓
- §5 testing (httptest routing, ms://+thinking asserts, upload-failure no-chat, DELETE on chat-fail, thinking-enabled omits key, multipart shape, MIME→ext, isolated DELETE, cleanup-on-cancel, pure backoff, retry-spacing, cancel-abort) → Tasks 1-4 tests. ✓
  - **Gap check:** "cleanup-on-cancel" is listed in spec §5 under `files_test.go`. It is covered by the deferred-delete design and the `TestExtractor_ChatFailureStillDeletes` case, but a dedicated cancel→DELETE test was not explicitly written. **Resolution:** added below.
- §6 risks → captured as notes (DELETE support open question flagged in Task 3 header).

**Gap fix — add cleanup-on-cancel test to `files_test.go` (Task 2 Step 2):**

Append to `internal/ai/extractor/files_test.go`:

```go
func TestExtractor_CleanupOnCancelDeleteStillRuns(t *testing.T) {
	var deleteCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-x", "status": "ready"})
		case strings.HasPrefix(r.URL.Path, "/files/") && r.Method == http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/chat/completions" && r.Method == http.MethodPost:
			<-r.Context().Done() // block until the client tears the request down
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 30*time.Second), quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, _ = e.Extract(ctx, []byte("img"), "image/png")

	// The deferred delete uses context.WithoutCancel(ctx), so it must reach the
	// server despite the caller ctx being cancelled. Poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && deleteCalls == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if deleteCalls == 0 {
		t.Fatal("DELETE /v1/files/{id} should run even after caller ctx cancel (via WithoutCancel)")
	}
}
```

**2. Placeholder scan:** No "TBD/TODO/later". Every code step shows full code. No "similar to Task N" — each task repeats the code. The one spot that drafted-then-replaced a helper (`TestExtractor_UploadFailureNoChatCall`) is resolved inline to a concrete version.

**3. Type/method consistency check:**
- `uploadImage(ctx, image, mime) (string, error)` — defined Task 2 Step 4, used Task 3 Step 4. ✓
- `deleteFile(ctx, fileID) error` — defined Task 2 Step 4, used Task 3 Step 4 (defer). ✓
- `filenameForMime(mime) string` — defined Task 2 Step 4, tested Task 2 Step 2. ✓
- `imageURLPart(url string)` — defined Task 3 Step 1, used Task 3 Step 4 as `imageURLPart("ms://" + fileID)`. ✓
- `Extractor` fields `baseURL`, `apiKey`, `httpClient`, `thinking` — added Task 2 Step 1 (struct), used Task 2 Step 4 (upload/delete), Task 3 Step 4 (`e.thinking`). ✓
- `defaultBackoff(attempt int) time.Duration` — defined Task 4 Step 3, tested Task 4 Step 1, wired Task 4 Step 3 + Step 4. ✓
- `Pipeline.backoff func(int) time.Duration` — field added Task 4 Step 3, set in `NewPipeline`, overridden in tests via `p.backoff = ...`. ✓
- `kimiServer(...) (*httptest.Server, *string, *int, *int)` — defined Task 3 Step 2, used consistently (4 returns). ✓
- `New(cfg, log)` signature unchanged → wire.go compiles (Task 5). ✓

No inconsistencies found.

---

## Execution Notes

- **Task ordering matters:** Tasks 1→5 are sequential. Task 2 adds the `Extractor` fields so `files.go` compiles standalone; Task 3 then only changes `Extract`. Each task's `-short` tests are green before the next begins.
- **CGO caveat:** If `go build ./...` fails locally on libvips, verify touched packages with `go vet ./internal/config/ ./internal/ai/extractor/ ./internal/pipeline/ ./internal/app/` and `go test -short ./internal/config/ ./internal/ai/extractor/ ./internal/pipeline/`. Note the caveat in the task output.
- **Single implementer at a time:** Tasks touch overlapping files (`extractor.go` in Tasks 2 & 3; `extractor_test.go` in Task 3) — do not run them in parallel.
