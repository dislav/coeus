# Question Import via CSV/Excel Upload — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /api/v1/questions/upload` that synchronously imports exam questions from a headerless 5-column CSV or `.xlsx` file into the canonical `questions` table with exact-hash upsert (file wins), best-effort batched embeddings, and a per-row import report.

**Architecture:** New `internal/importer` package (domain logic between HTTP and storage, mirroring `internal/pipeline`'s role) orchestrates sniff → parse → validate → embed → per-row upsert. Shared structural validation is refactored out of the `Create`/`Update` handlers into `domain.ValidateDraft` (behavior unchanged). Storage gets a single-transaction `UpsertFromImport` (`ON CONFLICT (question_hash) DO UPDATE` + tag replace). The embedder port gains `EmbedBatch`; HTTP layer gets one new handler + route. Fully synchronous — no job queue.

**Tech Stack:** Go 1.26.3, Gin, pgx/v5 + pgvector, `github.com/xuri/excelize/v2` v2.11.0 (new, pure Go), stdlib `encoding/csv`, openai-go v1.12.0 (embedder), testcontainers for storage integration tests.

**Spec:** `docs/superpowers/specs/2026-07-19-question-import-design.md` (approved). Section references (§) below point into it.

## Global Constraints

- Go 1.26.3; every build needs `CGO_ENABLED=1` + libvips (brew-installed on this machine). excelize is pure Go and must not disturb that.
- excelize pinned at **v2.11.0** (CVE-2026-54063 fix); opened with `excelize.Options{UnzipSizeLimit: 100 << 20, UnzipXMLSizeLimit: 64 << 20}`.
- `import.max_rows` default **20000**, env override `COEUS_IMPORT_MAX_ROWS`.
- `maxImportRowErrors = 100` (report caps `errors`, never `failed`); `embedChunkSize = 100`.
- Dedup during import is **exact hash only** (`domain.NormalizeQuestion` → `domain.HashQuestion`). No semantic dedup. On conflict the **file wins**; question ID never changes; `UpsertFromImport` must **never** call `cleanupImageBytesTx`.
- File format: fixed headerless 5 columns (`question;choices;answers;explanation;tags` as columns, multi-value cells `;`-separated), first sheet only for XLSX, sniff by content (`http.DetectContentType`), never by extension. Legacy `.xls` rejected explicitly.
- Row shape rule (identical for CSV and XLSX, in order): trim trailing empty cells → right-pad to 5 → >5 is a row-level error.
- The `Create`/`Update` handler refactor to `domain.ValidateDraft` must be **behavior-identical**: binding tags stay, HTTP responses stay `domain.ErrValidation` on structural failure; `Update`'s tag/confidence checks stay in the handler; `Create` gains no tag validation.
- Typed-nil embedder: `wire.go` keeps `var emb pipeline.AIEmbedder` assigned only when `COEUS_AI_EMBEDDER_API_KEY` is set; never wrap `(*embedder.Embedder)(nil)` in an interface.
- Verification commands: `go build ./...`, `go vet ./...`, unit `go test -short ./...`, integration `go test ./internal/storage/postgres/ -timeout 180s` (Docker required).
- `docs/` and `*.md` are gitignored — never `git add` this plan or the spec.

---

### Task 1: `domain.ValidateDraft` (new domain function)

**Files:**
- Create: `internal/domain/validate.go`
- Test: `internal/domain/validate_test.go`

**Interfaces:**
- Consumes: `domain.QuestionTypeMultipleChoice`, `domain.QuestionTypeFreeResponse` (`internal/domain/question.go:24-28`).
- Produces: `func ValidateDraft(text string, choices, answers []string, typ string) error` — used by Task 2 (handlers) and Task 6 (importer). Returned errors are plain `error` whose `.Error()` is the exact row-level message; handlers map any non-nil return to `domain.ErrValidation`.

- [ ] **Step 1: Write the failing test**

Create `internal/domain/validate_test.go`:

```go
package domain

import "testing"

func TestValidateDraft(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		choices []string
		answers []string
		typ     string
		wantErr string // "" means nil error
	}{
		{"valid multiple_choice", "q?", []string{"a", "b"}, []string{"a"}, QuestionTypeMultipleChoice, ""},
		{"valid multiple_choice multi-answer", "q?", []string{"a", "b", "c"}, []string{"a", "c"}, QuestionTypeMultipleChoice, ""},
		{"valid free_response", "q?", nil, []string{"42"}, QuestionTypeFreeResponse, ""},
		{"empty text", "", []string{"a", "b"}, []string{"a"}, QuestionTypeMultipleChoice, "question text is required"},
		{"empty text free_response", "", nil, []string{"42"}, QuestionTypeFreeResponse, "question text is required"},
		{"no answers multiple_choice", "q?", []string{"a", "b"}, nil, QuestionTypeMultipleChoice, "at least one answer is required"},
		{"no answers free_response", "q?", nil, nil, QuestionTypeFreeResponse, "at least one answer is required"},
		{"multiple_choice one choice", "q?", []string{"a"}, []string{"a"}, QuestionTypeMultipleChoice, "multiple_choice requires at least 2 choices"},
		{"multiple_choice zero choices", "q?", nil, []string{"a"}, QuestionTypeMultipleChoice, "multiple_choice requires at least 2 choices"},
		{"answer not in choices", "q?", []string{"a", "b"}, []string{"c"}, QuestionTypeMultipleChoice, "answers must be a subset of choices"},
		{"answer subset is case-sensitive", "q?", []string{"Paris", "London"}, []string{"paris"}, QuestionTypeMultipleChoice, "answers must be a subset of choices"},
		{"free_response with choices", "q?", []string{"a", "b"}, []string{"a"}, QuestionTypeFreeResponse, "free_response must not have choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDraft(tt.text, tt.choices, tt.answers, tt.typ)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDraft() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateDraft() = nil, want error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("ValidateDraft() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -short ./internal/domain/ -run TestValidateDraft -v`
Expected: FAIL — `undefined: ValidateDraft`

- [ ] **Step 3: Write minimal implementation**

Create `internal/domain/validate.go`:

```go
package domain

import "errors"

// ValidateDraft checks the shared structural rules for a question draft
// (spec §5.5): non-empty text, at least one answer for both types, and the
// type-conditional checks. It is called by the Create/Update question
// handlers (which map any error to ErrValidation) and by the bulk importer
// (which surfaces err.Error() as the row-level report message).
//
// The answers >= 1 rule applies to both types deliberately: without it,
// empty answers would vacuously pass the answers-subset-of-choices check
// for multiple_choice.
func ValidateDraft(text string, choices, answers []string, typ string) error {
	if text == "" {
		return errors.New("question text is required")
	}
	if len(answers) < 1 {
		return errors.New("at least one answer is required")
	}
	switch typ {
	case QuestionTypeMultipleChoice:
		if len(choices) < 2 {
			return errors.New("multiple_choice requires at least 2 choices")
		}
		if !answersSubsetOfChoices(answers, choices) {
			return errors.New("answers must be a subset of choices")
		}
	case QuestionTypeFreeResponse:
		if len(choices) != 0 {
			return errors.New("free_response must not have choices")
		}
	}
	return nil
}

// answersSubsetOfChoices reports whether every answer equals some choice using
// exact, case-sensitive Go string equality (no normalization). Duplicates in
// answers are fine as long as each is present in choices (spec §3.2.3).
// Moved here from internal/httpapi/handlers/questions.go; semantics unchanged.
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

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -short ./internal/domain/ -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/domain/`
Expected: no output

```bash
git add internal/domain/validate.go internal/domain/validate_test.go
git commit -m "feat(domain): ValidateDraft with shared type-conditional structural checks"
```

---

### Task 2: Refactor `Create`/`Update` handlers onto `domain.ValidateDraft`

Behavior-preserving refactor. The existing `internal/httpapi/handlers/questions_test.go` suite is the regression guard — it must pass before and after with **zero test changes**.

**Files:**
- Modify: `internal/httpapi/handlers/questions.go` (Update switch at lines 177–196; `answersSubsetOfChoices` at lines 254–271; Create switch at lines 303–319)

**Interfaces:**
- Consumes: `domain.ValidateDraft` (Task 1).
- Produces: nothing new. `answersSubsetOfChoices` is deleted from this package (its behavior now lives in `internal/domain/validate.go`).

- [ ] **Step 1: Baseline — run the existing handler suite**

Run: `go test -short ./internal/httpapi/handlers/ -run 'TestCreate|TestUpdate' -v`
Expected: PASS (this is the baseline the refactor must preserve)

- [ ] **Step 2: Replace the `Update` switch block**

In `internal/httpapi/handlers/questions.go`, replace lines 177–196 (the `switch req.Type { ... }` block inside `Update`) with:

```go
	// Structural rules are type-conditional (spec §3.5.4), shared with Create
	// and the importer via domain.ValidateDraft. Binding guarantees:
	//   - req.Type is one of {multiple_choice, free_response}
	//   - every present choice is non-empty
	//   - len(req.Answers) >= 1
	// Update's DTO carries no question text, so the placeholder keeps
	// ValidateDraft's non-empty-text check trivially satisfied; only the
	// type-conditional checks are load-bearing here.
	if err := domain.ValidateDraft(" ", req.Choices, req.Answers, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
```

- [ ] **Step 3: Replace the `Create` switch block**

Replace lines 303–319 (`// Type-conditional structural validation (spec §3.5.4).` comment plus the `switch req.Type { ... }` block inside `Create`) with:

```go
	// Type-conditional structural validation (spec §3.5.4), shared with Update
	// and the importer via domain.ValidateDraft.
	if err := domain.ValidateDraft(req.Question, req.Choices, req.Answers, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
```

- [ ] **Step 4: Delete the handlers-local `answersSubsetOfChoices`**

Delete the entire function at lines 254–271 (including its doc comment):

```go
// answersSubsetOfChoices reports whether every answer equals some choice using
// exact, case-sensitive Go string equality (no normalization). Duplicates in
// answers are fine as long as each is present in choices (spec §3.2.3).
func answersSubsetOfChoices(answers, choices []string) bool { ... }
```

- [ ] **Step 5: Run the full handler suite (regression gate)**

Run: `go test -short ./internal/httpapi/handlers/ -v`
Expected: PASS — every pre-existing test, unchanged

- [ ] **Step 6: Vet and commit**

Run: `go vet ./internal/httpapi/...`
Expected: no output

```bash
git add internal/httpapi/handlers/questions.go
git commit -m "refactor(handlers): Create/Update use domain.ValidateDraft, drop local answersSubsetOfChoices"
```

---

### Task 3: `ImportConfig` (config section + env override)

**Files:**
- Modify: `internal/config/config.go` (`Config` struct at lines 17–25; `applyEnvOverrides` at lines 121–197)
- Modify: `internal/config/config.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: existing `applyEnvOverrides` parse-int style (see `COEUS_WORKERS_COUNT`, config.go:167-173).
- Produces: `config.ImportConfig` struct; `Config.Import` field; `COEUS_IMPORT_MAX_ROWS` env override — consumed by Task 11 (`wire.go` passes `cfg.Import.MaxRows` to `importer.New`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestLoadDefaults_ImportMaxRows(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_VISION_API_KEY", "kimi-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "ds-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Import.MaxRows != 20000 {
		t.Errorf("import.max_rows = %d, want 20000", cfg.Import.MaxRows)
	}
}

func TestApplyEnvOverrides_ImportMaxRows(t *testing.T) {
	t.Setenv("COEUS_IMPORT_MAX_ROWS", "500")
	var cfg Config
	if err := applyEnvOverrides(&cfg); err != nil {
		t.Fatalf("applyEnvOverrides: %v", err)
	}
	if cfg.Import.MaxRows != 500 {
		t.Errorf("MaxRows = %d, want 500", cfg.Import.MaxRows)
	}
}

func TestApplyEnvOverrides_ImportMaxRowsInvalid(t *testing.T) {
	t.Setenv("COEUS_IMPORT_MAX_ROWS", "many")
	var cfg Config
	if err := applyEnvOverrides(&cfg); err == nil {
		t.Fatal("expected error for invalid COEUS_IMPORT_MAX_ROWS, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/config/ -run ImportMaxRows -v`
Expected: FAIL — `cfg.Import undefined`

- [ ] **Step 3: Implement config changes**

In `internal/config/config.go`, add `Import` to the `Config` struct (after `Upload`):

```go
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Postgres PostgresConfig `yaml:"postgres"`
	JWT      JWTConfig      `yaml:"jwt"`
	AI       AIConfig       `yaml:"ai"`
	Pipeline PipelineConfig `yaml:"pipeline"`
	Workers  WorkersConfig  `yaml:"workers"`
	Upload   UploadConfig   `yaml:"upload"`
	Import   ImportConfig   `yaml:"import"`
}
```

Add the new type next to `UploadConfig` (after line 106):

```go
// ImportConfig bounds the synchronous question-import endpoint
// (spec §10). 20000 keeps the worst-case runtime within server.write_timeout.
type ImportConfig struct {
	MaxRows int `yaml:"max_rows"`
}
```

Add the env override at the end of `applyEnvOverrides`, before `return nil` (mirrors the `COEUS_WORKERS_COUNT` style):

```go
	if v := os.Getenv("COEUS_IMPORT_MAX_ROWS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid COEUS_IMPORT_MAX_ROWS %q: %w", v, err)
		}
		cfg.Import.MaxRows = n
	}
```

In `internal/config/config.yaml`, append at the end of the file:

```yaml
import:
  max_rows: 20000
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -short ./internal/config/ -v`
Expected: PASS (new + all pre-existing tests)

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/config/`
Expected: no output

```bash
git add internal/config/config.go internal/config/config.yaml internal/config/config_test.go
git commit -m "feat(config): import.max_rows (default 20000) with COEUS_IMPORT_MAX_ROWS override"
```

---

### Task 4: Importer package core — report, format sniffing, CSV parsing, row shape

Creates the `internal/importer` package skeleton: report type, file-level sentinel errors, `SniffKind`, CSV parser, `normalizeRow`, `splitMulti`.

**Files:**
- Create: `internal/importer/report.go`
- Create: `internal/importer/parse.go`
- Test: `internal/importer/parse_test.go`

**Interfaces:**
- Consumes: `domain.NewError` (`internal/domain/errors.go:29`).
- Produces (used by Tasks 5, 6, 9, 10):
  - `type FileKind int` with `KindCSV`, `KindXLSX`
  - `func SniffKind(data []byte) (FileKind, error)`
  - `var ErrEmptyFile, ErrUnsupportedFormat, ErrLegacyXLS, ErrTooManyRows` — all `*domain.Error` with code `"validation"` (⇒ 400 via `domain.HTTPStatus`)
  - `func parseCSV(r io.Reader) ([][]string, error)`
  - `func normalizeRow(cells []string) ([5]string, error)`
  - `func splitMulti(cell string) []string`
  - `type Report struct { TotalRows, Created, Updated, Failed int; Errors []RowError }`, `type RowError struct { Row int; Message string }`, `const maxImportRowErrors = 100`, `func (r *Report) addRowError(row int, msg string)`

- [ ] **Step 1: Write the failing tests**

Create `internal/importer/parse_test.go`:

```go
package importer

import (
	"strings"
	"testing"
)

func sniffable(prefix []byte) []byte {
	// http.DetectContentType inspects up to the first 512 bytes.
	buf := make([]byte, 512)
	copy(buf, prefix)
	return buf
}

func TestSniffKind(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    FileKind
		wantErr error
	}{
		{"csv text", []byte("What is 2+2?,3;4,4,math,arith\n"), KindCSV, nil},
		{"xlsx zip", sniffable([]byte("PK\x03\x04")), KindXLSX, nil},
		{"legacy xls", sniffable([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}), 0, ErrLegacyXLS},
		{"png unsupported", sniffable([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}), 0, ErrUnsupportedFormat},
		{"empty", nil, 0, ErrEmptyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, err := SniffKind(tt.data)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("SniffKind() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SniffKind() unexpected err = %v", err)
			}
			if kind != tt.want {
				t.Errorf("SniffKind() = %v, want %v", kind, tt.want)
			}
		})
	}
}

func TestParseCSV(t *testing.T) {
	in := "q1,a;b,a,e1,t1\nq2,,42,e2,t1;t2\n"
	rows, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0][1] != "a;b" || rows[1][2] != "42" {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseCSV_VariableFields(t *testing.T) {
	// FieldsPerRecord = -1: ragged rows must not error (handled per-row later).
	in := "q1,a;b,a\nq2,a;b,a,e,t,EXTRA\n"
	rows, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(rows) != 2 || len(rows[0]) != 3 || len(rows[1]) != 6 {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseCSV_TrimLeadingSpace(t *testing.T) {
	rows, err := parseCSV(strings.NewReader("q1, a;b ,a,e,t\n"))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if rows[0][1] != "a;b " {
		t.Errorf("rows[0][1] = %q, want %q (leading trimmed, trailing kept)", rows[0][1], "a;b ")
	}
}

func TestParseCSV_Malformed(t *testing.T) {
	_, err := parseCSV(strings.NewReader("q1,\"unterminated,a\n"))
	if err == nil {
		t.Fatal("expected error for malformed csv, got nil")
	}
	if !strings.Contains(err.Error(), "malformed csv") {
		t.Errorf("err = %q, want it to mention malformed csv", err.Error())
	}
}

func TestNormalizeRow(t *testing.T) {
	tests := []struct {
		name    string
		cells   []string
		want    [5]string
		wantErr bool
	}{
		{"exactly 5", []string{"q", "c", "a", "e", "t"}, [5]string{"q", "c", "a", "e", "t"}, false},
		{"pad short row", []string{"q", "c", "a"}, [5]string{"q", "c", "a", "", ""}, false},
		{"trim trailing empties", []string{"q", "c", "a", "", ""}, [5]string{"q", "c", "a", "", ""}, false},
		{"trim then pad", []string{"q", "c", "a", "", " ", ""}, [5]string{"q", "c", "a", "", ""}, false},
		{"over 5 after trim errors", []string{"q", "c", "a", "e", "t", "EXTRA"}, [5]string{}, true},
		{"over 5 only via empties is fine", []string{"q", "c", "a", "e", "t", ""}, [5]string{"q", "c", "a", "e", "t"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRow(tt.cells)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRow: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeRow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitMulti(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a;b;c", []string{"a", "b", "c"}},
		{"a; b ;c", []string{"a", "b", "c"}},
		{"a;;b", []string{"a", "b"}},
		{"", []string{}},
		{" ; ", []string{}},
		{"single", []string{"single"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := splitMulti(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitMulti(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitMulti(%q) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/importer/ -v`
Expected: FAIL — package does not exist / undefined symbols

- [ ] **Step 3: Implement `report.go` and `parse.go`**

Create `internal/importer/report.go`:

```go
package importer

// maxImportRowErrors caps the errors carried in a Report; Failed always
// reports the true count (spec §4.2).
const maxImportRowErrors = 100

// RowError is one failed row: 1-based file row number plus a human message.
type RowError struct {
	Row     int
	Message string
}

// Report is the per-file import outcome (spec §4.2). Invariant:
// TotalRows = Created + Updated + Failed (each in-file duplicate occurrence
// counts individually).
type Report struct {
	TotalRows int
	Created   int
	Updated   int
	Failed    int
	Errors    []RowError
}

// addRowError records a failed row, honoring the maxImportRowErrors cap.
func (r *Report) addRowError(row int, msg string) {
	r.Failed++
	if len(r.Errors) < maxImportRowErrors {
		r.Errors = append(r.Errors, RowError{Row: row, Message: msg})
	}
}
```

Create `internal/importer/parse.go`:

```go
package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// File-level sentinel errors (spec §11). All carry the "validation" code so
// domain.HTTPStatus maps them to 400 and errorResponse renders the envelope.
var (
	ErrEmptyFile         = domain.NewError("validation", "empty file")
	ErrUnsupportedFormat = domain.NewError("validation", "unsupported file format")
	ErrLegacyXLS         = domain.NewError("validation", "legacy .xls not supported — save as .xlsx")
	ErrTooManyRows       = domain.NewError("validation", "too many rows")
)

// FileKind is the sniffed upload format (spec §5.1).
type FileKind int

const (
	KindCSV FileKind = iota
	KindXLSX
)

// SniffKind detects the upload format from content bytes — never the file
// extension — via http.DetectContentType (spec §5.1). Non-UTF-8 CSV sniffs as
// application/octet-stream and is therefore rejected as unsupported.
func SniffKind(data []byte) (FileKind, error) {
	if len(data) == 0 {
		return 0, ErrEmptyFile
	}
	ct := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(ct, "text/"):
		return KindCSV, nil
	case ct == "application/zip":
		return KindXLSX, nil
	case ct == "application/x-ole-storage":
		return 0, ErrLegacyXLS
	default:
		return 0, ErrUnsupportedFormat
	}
}

// parseCSV streams all rows from a UTF-8 CSV reader (spec §5.2). Variable
// column counts are allowed here and handled per-row by normalizeRow.
func parseCSV(r io.Reader) ([][]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	var rows [][]string
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, domain.NewError("validation", fmt.Sprintf("malformed csv: %v", err))
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

// normalizeRow applies the deterministic row-shape rule (spec §5.4), identical
// for CSV and XLSX: trim trailing empty cells, right-pad short rows to 5
// columns, reject rows with more than 5 remaining columns.
func normalizeRow(cells []string) ([5]string, error) {
	var out [5]string
	trimmed := cells
	for len(trimmed) > 0 && strings.TrimSpace(trimmed[len(trimmed)-1]) == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) > 5 {
		return out, errors.New("too many columns (max 5)")
	}
	copy(out[:], trimmed)
	return out, nil
}

// splitMulti splits a multi-value cell on ';', trims each item, and drops
// empty items (spec §5.4).
func splitMulti(cell string) []string {
	parts := strings.Split(cell, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -short ./internal/importer/ -v`
Expected: PASS (all tests)

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/importer/`
Expected: no output

```bash
git add internal/importer/report.go internal/importer/parse.go internal/importer/parse_test.go
git commit -m "feat(importer): report type, content sniffing, CSV parsing, row-shape rule"
```

---

### Task 5: XLSX parsing with excelize (new dependency)

**Files:**
- Modify: `go.mod`, `go.sum` (add `github.com/xuri/excelize/v2 v2.11.0`)
- Create: `internal/importer/parse_xlsx.go`
- Test: `internal/importer/parse_xlsx_test.go`

**Interfaces:**
- Consumes: `github.com/xuri/excelize/v2` v2.11.0 (`OpenReader`, `Options{UnzipSizeLimit, UnzipXMLSizeLimit}`, `GetSheetList`, `Rows` iterator); `domain.NewError`; sentinels from Task 4.
- Produces: `func parseXLSX(r io.Reader) ([][]string, error)` — consumed by Task 9 (`Service.Import`).

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/xuri/excelize/v2@v2.11.0 && go mod tidy`
Expected: `go: added github.com/xuri/excelize/v2 v2.11.0` (pure Go — no CGO impact)

- [ ] **Step 2: Write the failing test**

Create `internal/importer/parse_xlsx_test.go` (fixtures generated in-test — no binary fixtures committed, spec §12.1):

```go
package importer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// xlsxFixture builds a one-sheet workbook in memory with the given rows.
func xlsxFixture(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	for i, row := range rows {
		for j, v := range row {
			cell, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}
			if err := f.SetCellValue("Sheet1", cell, v); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func TestParseXLSX_FirstSheetRows(t *testing.T) {
	data := xlsxFixture(t, [][]string{
		{"q1", "a;b", "a", "expl", "t1;t2"},
		{"q2", "", "42", "", ""},
	})
	rows, err := parseXLSX(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseXLSX: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0][0] != "q1" || rows[0][1] != "a;b" || rows[0][4] != "t1;t2" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1][0] != "q2" {
		t.Errorf("row 1 = %v", rows[1])
	}
}

func TestParseXLSX_FirstSheetOnly(t *testing.T) {
	f := excelize.NewFile()
	if err := f.SetCellValue("Sheet1", "A1", "from-sheet-1"); err != nil {
		t.Fatalf("set cell: %v", err)
	}
	f.NewSheet("Sheet2")
	if err := f.SetCellValue("Sheet2", "A1", "from-sheet-2"); err != nil {
		t.Fatalf("set cell: %v", err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	_ = f.Close()

	rows, err := parseXLSX(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("parseXLSX: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "from-sheet-1" {
		t.Errorf("rows = %v, want first sheet only", rows)
	}
}

func TestParseXLSX_Malformed(t *testing.T) {
	// Truncated zip — OpenReader must fail and surface a file-level error.
	data := xlsxFixture(t, [][]string{{"q", "a;b", "a", "", ""}})
	_, err := parseXLSX(bytes.NewReader(data[:len(data)/2]))
	if err == nil {
		t.Fatal("expected error for truncated xlsx, got nil")
	}
	if !strings.Contains(err.Error(), "malformed xlsx") {
		t.Errorf("err = %q, want it to mention malformed xlsx", err.Error())
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -short ./internal/importer/ -run TestParseXLSX -v`
Expected: FAIL — `undefined: parseXLSX`

- [ ] **Step 4: Implement `parse_xlsx.go`**

Create `internal/importer/parse_xlsx.go`:

```go
package importer

import (
	"fmt"
	"io"

	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/xuri/excelize/v2"
)

// parseXLSX streams all rows of the FIRST sheet (spec §5.3). Decompression
// limits are explicit: the library defaults (16 GB / 16 MB) are unsafe
// against the 10 MB upload cap.
func parseXLSX(r io.Reader) ([][]string, error) {
	f, err := excelize.OpenReader(r, excelize.Options{
		UnzipSizeLimit:    100 << 20,
		UnzipXMLSizeLimit: 64 << 20,
	})
	if err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, domain.NewError("validation", "malformed xlsx: no sheets")
	}

	rows, err := f.Rows(sheets[0])
	if err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	defer func() { _ = rows.Close() }()

	var out [][]string
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
		}
		out = append(out, cols)
	}
	if err := rows.Error(); err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -short ./internal/importer/ -v`
Expected: PASS (all tests incl. Task 4's)

- [ ] **Step 6: Build, vet, commit**

Run: `go build ./... && go vet ./...`
Expected: no output (verifies the new dep didn't disturb the CGO/libvips build)

```bash
git add go.mod go.sum internal/importer/parse_xlsx.go internal/importer/parse_xlsx_test.go
git commit -m "feat(importer): XLSX parsing via excelize v2.11.0, first sheet, explicit unzip limits"
```

---

### Task 6: Importer row validation — `buildQuestion`

**Files:**
- Create: `internal/importer/validate.go`
- Test: `internal/importer/validate_test.go`

**Interfaces:**
- Consumes: `domain.ValidateDraft` (Task 1), `domain.InferQuestionType` / `NormalizeQuestion` / `HashQuestion` (`internal/domain/question.go:87,98,104`), `domain.ChoiceLabelingLetter`, `domain.QuestionStatusVerified`, `domain.QuestionTypeMultipleChoice` / `QuestionTypeFreeResponse`, `normalizeRow` + `splitMulti` (Task 4).
- Produces: `func buildQuestion(cols [5]string, userID string, now time.Time) (*domain.Question, error)` — consumed by Task 9. Constants: `maxFileTags = 20`, `importTag = "import"`, `importConfidence = 0.99`.

- [ ] **Step 1: Write the failing tests**

Create `internal/importer/validate_test.go`:

```go
package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

var importNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func TestBuildQuestion_ValidMultipleChoice(t *testing.T) {
	cols := [5]string{"What is 2+2?", "3;4", "4", "basic math", "arith;easy"}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}

	if q.Type != domain.QuestionTypeMultipleChoice {
		t.Errorf("Type = %q, want multiple_choice", q.Type)
	}
	if q.Number != 0 {
		t.Errorf("Number = %d, want 0", q.Number)
	}
	if q.ChoiceLabeling != domain.ChoiceLabelingLetter {
		t.Errorf("ChoiceLabeling = %q, want letter", q.ChoiceLabeling)
	}
	if q.Confidence != 0.99 {
		t.Errorf("Confidence = %v, want 0.99", q.Confidence)
	}
	if q.Status != domain.QuestionStatusVerified {
		t.Errorf("Status = %q, want verified", q.Status)
	}
	if q.VerifiedAt == nil || *q.VerifiedAt != "2026-07-19T12:00:00Z" {
		t.Errorf("VerifiedAt = %v, want 2026-07-19T12:00:00Z", q.VerifiedAt)
	}
	if q.VerifiedBy == nil || *q.VerifiedBy != "user-1" {
		t.Errorf("VerifiedBy = %v, want user-1", q.VerifiedBy)
	}
	wantNorm := domain.NormalizeQuestion("What is 2+2?")
	if q.TextNorm != wantNorm || q.TextHash != domain.HashQuestion(wantNorm) {
		t.Errorf("norm/hash mismatch: %q / %q", q.TextNorm, q.TextHash)
	}
	if q.Explanation != "basic math" {
		t.Errorf("Explanation = %q", q.Explanation)
	}
	// tags = file tags + "import"
	if len(q.Tags) != 3 || q.Tags[0] != "arith" || q.Tags[1] != "easy" || q.Tags[2] != "import" {
		t.Errorf("Tags = %v, want [arith easy import]", q.Tags)
	}
	if q.Embedding != nil {
		t.Errorf("Embedding = %v, want nil (assigned later by the embed step)", q.Embedding)
	}
}

func TestBuildQuestion_ValidFreeResponse(t *testing.T) {
	cols := [5]string{"Explain entropy.", "", "disorder increases", "", ""}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}
	if q.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response", q.Type)
	}
	if len(q.Choices) != 0 {
		t.Errorf("Choices = %v, want empty", q.Choices)
	}
}

func TestBuildQuestion_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		cols    [5]string
		wantErr string
	}{
		{"empty question", [5]string{"", "a;b", "a", "", ""}, "question text is required"},
		{"no answers", [5]string{"q", "a;b", "", "", ""}, "at least one answer is required"},
		{"one choice", [5]string{"q", "a", "a", "", ""}, "multiple_choice requires at least 2 choices"},
		{"answer not a choice", [5]string{"q", "a;b", "c", "", ""}, "answers must be a subset of choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildQuestion(tt.cols, "user-1", importNow)
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("buildQuestion() err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildQuestion_TooManyTags(t *testing.T) {
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = strings.Repeat("t", i+1)
	}
	cols := [5]string{"q", "a;b", "a", "", strings.Join(tags, ";")}
	_, err := buildQuestion(cols, "user-1", importNow)
	if err == nil || err.Error() != "too many tags (max 20)" {
		t.Errorf("err = %v, want too many tags (max 20)", err)
	}
}

func TestBuildQuestion_Exactly20TagsAllowed(t *testing.T) {
	tags := make([]string, 20)
	for i := range tags {
		tags[i] = strings.Repeat("t", i+1)
	}
	cols := [5]string{"q", "a;b", "a", "", strings.Join(tags, ";")}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}
	// 20 file tags + "import" marker = 21 stored (spec §5.5).
	if len(q.Tags) != 21 || q.Tags[20] != "import" {
		t.Errorf("Tags len = %d, want 21 with trailing import marker", len(q.Tags))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/importer/ -run TestBuildQuestion -v`
Expected: FAIL — `undefined: buildQuestion`

- [ ] **Step 3: Implement `validate.go`**

Create `internal/importer/validate.go`:

```go
package importer

import (
	"errors"
	"fmt"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// maxFileTags caps tags supplied by the file. The "import" marker is appended
// AFTER this check, so a stored row may carry 21 tags total — mirroring how
// Create appends "manual-entry" after binding (spec §5.5).
const maxFileTags = 20

// importTag marks every imported question (spec §5.6).
const importTag = "import"

// importConfidence is constant: the file has no confidence column, and 0.99
// fits the numeric(3,2) column (spec §5.6).
const importConfidence = 0.99

// buildQuestion validates one shape-normalized row and, on success, builds
// the canonical verified question with the §5.6 constant field values.
// Embedding stays nil — the Service's embed step assigns it later.
func buildQuestion(cols [5]string, userID string, now time.Time) (*domain.Question, error) {
	text := cols[0]
	choices := splitMulti(cols[1])
	answers := splitMulti(cols[2])
	explanation := cols[3]
	tags := splitMulti(cols[4])

	// No type column in the file: empty choices ⇒ free_response.
	typ := domain.InferQuestionType(choices)
	if err := domain.ValidateDraft(text, choices, answers, typ); err != nil {
		return nil, err
	}

	if len(tags) > maxFileTags {
		return nil, fmt.Errorf("too many tags (max %d)", maxFileTags)
	}
	for _, tg := range tags {
		if tg == "" { // defensive: splitMulti already drops empty items
			return nil, errors.New("empty tag")
		}
	}

	norm := domain.NormalizeQuestion(text)
	verifiedAt := now.UTC().Format(time.RFC3339)

	// tags = file tags + ["import"]; copy to avoid aliasing (Create precedent).
	fullTags := make([]string, 0, len(tags)+1)
	fullTags = append(fullTags, tags...)
	fullTags = append(fullTags, importTag)

	return &domain.Question{
		Number:         0,
		Text:           text,
		TextNorm:       norm,
		TextHash:       domain.HashQuestion(norm),
		Choices:        choices,
		Answers:        answers,
		ChoiceLabeling: domain.ChoiceLabelingLetter,
		Type:           typ,
		Confidence:     importConfidence,
		Explanation:    explanation,
		Status:         domain.QuestionStatusVerified,
		VerifiedAt:     &verifiedAt,
		VerifiedBy:     &userID,
		Tags:           fullTags,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -short ./internal/importer/ -v`
Expected: PASS

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/importer/`
Expected: no output

```bash
git add internal/importer/validate.go internal/importer/validate_test.go
git commit -m "feat(importer): buildQuestion row validation with import constants and tag rules"
```

---

### Task 7: `EmbedBatch` on `pipeline.AIEmbedder` + native implementation

Extends the embedder port and implements batch embedding. Both test fakes of `AIEmbedder` gain stubs in the same task (interface change would otherwise break the build).

**Files:**
- Modify: `internal/pipeline/ports.go` (`AIEmbedder` at lines 30–33)
- Modify: `internal/ai/embedder/input.go`
- Modify: `internal/ai/embedder/embedder.go`
- Test: `internal/ai/embedder/embedder_test.go`
- Modify: `internal/pipeline/pipeline_test.go` (fakeEmbedder at lines 85–94)
- Modify: `internal/httpapi/handlers/questions_test.go` (fakeEmbedder at lines 111–121)

**Interfaces:**
- Consumes: openai-go v1.12.0 `EmbeddingNewParamsInputUnion.OfArrayOfStrings` (verified present in the pinned SDK); existing `Embedder` fields `client/dim/model` (`internal/ai/embedder/embedder.go:24-29`).
- Produces:
  - `AIEmbedder.EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)` — consumed by Task 11 (`wire.go`: a non-nil `emb` then satisfies the importer's `BatchEmbedder` port directly).
  - `func (e *Embedder) EmbedBatch(...)` — results aligned to inputs by the response `index` field, dim-checked (1536 via `e.dim`) exactly like `Embed`.
  - `type StringsInput []string` with `FromStrings()`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ai/embedder/embedder_test.go`:

```go
const batchEmbeddingsBody = `{"object":"list","data":[` +
	`{"object":"embedding","index":1,"embedding":[0.3,0.4]},` +
	`{"object":"embedding","index":0,"embedding":[0.1,0.2]}],` +
	`"model":"text-embedding-3-small","usage":{"prompt_tokens":4,"total_tokens":4}}`

func TestEmbedder_EmbedBatchHappyPath(t *testing.T) {
	// Server returns data out of order; alignment must follow the index field.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(req.Input) != 2 {
			t.Errorf("input len = %d, want 2 (array input)", len(req.Input))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(batchEmbeddingsBody))
	}))
	defer srv.Close()

	cfg := testCfg(srv.URL)
	cfg.Dim = 2
	e := New(cfg, quietLogger())
	out, err := e.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0][0] != float32(0.1) || out[0][1] != float32(0.2) {
		t.Errorf("out[0] = %v, want [0.1 0.2] (index-aligned)", out[0])
	}
	if out[1][0] != float32(0.3) || out[1][1] != float32(0.4) {
		t.Errorf("out[1] = %v, want [0.3 0.4] (index-aligned)", out[1])
	}
}

func TestEmbedder_EmbedBatchTransportError(t *testing.T) {
	srv := embeddingsServer(t, 0, http.StatusInternalServerError, `{"error":"down"}`)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	_, err := e.EmbedBatch(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}

func TestEmbedder_EmbedBatchDimensionMismatch(t *testing.T) {
	// Server returns 10-dim vectors but cfg.Dim is 1536.
	srv := embeddingsServer(t, 10, http.StatusOK, happyEmbeddingsBody)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	_, err := e.EmbedBatch(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected error on dim mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error = %q, expected to mention dimension", err.Error())
	}
}

func TestEmbedder_EmbedBatchEmptyInput(t *testing.T) {
	srv := embeddingsServer(t, 1536, http.StatusOK, happyEmbeddingsBody)
	defer srv.Close()

	e := New(testCfg(srv.URL), quietLogger())
	// The guard rejects empty input before any network call.
	_, err := e.EmbedBatch(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
}
```

Note: `TestEmbedder_EmbedBatchDimensionMismatch` reuses `happyEmbeddingsBody` (a single `index:0` entry) — with one input text, `len(resp.Data) == 1 == len(texts)`, so the dim check is what fires.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/ai/embedder/ -run EmbedBatch -v`
Expected: FAIL — `e.EmbedBatch undefined`

- [ ] **Step 3: Extend the port, the input union, and the embedder**

In `internal/pipeline/ports.go`, replace the `AIEmbedder` interface (lines 30–33):

```go
// AIEmbedder produces a vector embedding of question text for semantic dedup.
type AIEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch embeds many texts in one API call. Results are aligned to
	// inputs by index and dim-checked exactly like Embed.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}
```

In `internal/ai/embedder/input.go`, append (the same union-reconciliation comment as `StringInput` applies; `OfArrayOfStrings` matches the pinned openai-go v1.12.0):

```go
// StringsInput adapts a []string into the openai-go embedding input union.
// Same union-reconciliation caveat as StringInput: the concrete field
// (OfArrayOfStrings) matches the pinned SDK version; update only here if
// the generated union changes.
type StringsInput []string

// FromStrings sets the union to the given string array. The wire requirement
// is {"model":"<model>","input":["<text>","<text>",...]}.
func (s StringsInput) FromStrings() openai.EmbeddingNewParamsInputUnion {
	return openai.EmbeddingNewParamsInputUnion{
		OfArrayOfStrings: s,
	}
}
```

In `internal/ai/embedder/embedder.go`, append after `Embed`:

```go
// EmbedBatch calls the embeddings endpoint once for many texts and returns one
// vector per input, aligned by the response index field. On any failure
// (transport, count mismatch, dimension mismatch, empty input) it returns
// (nil, err) — the caller treats batch embedding as best-effort.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("embed batch: empty input")
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: StringsInput(texts).FromStrings(),
	}

	resp, err := e.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("embed batch: %w", err)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embed batch: got %d embeddings for %d inputs", len(resp.Data), len(texts))
	}

	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= int64(len(texts)) {
			return nil, fmt.Errorf("embed batch: response index %d out of range", d.Index)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		if e.dim > 0 && len(vec) != e.dim {
			return nil, fmt.Errorf("embed batch: dimension mismatch: got %d, want %d", len(vec), e.dim)
		}
		out[d.Index] = vec
	}
	return out, nil
}
```

- [ ] **Step 4: Add `EmbedBatch` stubs to both test fakes**

In `internal/pipeline/pipeline_test.go`, after the fakeEmbedder `Embed` method (line 94):

```go
func (f *fakeEmbedder) EmbedBatch(_ context.Context, _ []string) ([][]float32, error) {
	return nil, f.err
}
```

In `internal/httpapi/handlers/questions_test.go`, after the fakeEmbedder `Embed` method (line 121):

```go
func (f *fakeEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
```

- [ ] **Step 5: Run tests to verify they pass (and nothing broke)**

Run: `go test -short ./... `
Expected: PASS (embedder's new tests + the whole suite; the two fake stubs keep pipeline/handlers green)

- [ ] **Step 6: Build, vet, commit**

Run: `go build ./... && go vet ./...`
Expected: no output

```bash
git add internal/pipeline/ports.go internal/ai/embedder/input.go internal/ai/embedder/embedder.go internal/ai/embedder/embedder_test.go internal/pipeline/pipeline_test.go internal/httpapi/handlers/questions_test.go
git commit -m "feat(ai): EmbedBatch on AIEmbedder with index-aligned, dim-checked results"
```

---

### Task 8: `UpsertFromImport` (storage port + Postgres implementation)

Single-transaction upsert with tag replace; **no** `cleanupImageBytesTx`. Both `storage.QuestionRepo` test fakes gain stubs in the same task.

**Files:**
- Modify: `internal/storage/ports.go` (`QuestionRepo` interface, lines 85–103)
- Create: `internal/storage/postgres/question_import.go`
- Test: `internal/storage/postgres/question_import_test.go` (integration, testcontainers)
- Modify: `internal/pipeline/pipeline_test.go` (fakeQuestionRepo, after `Delete` at line 231)
- Modify: `internal/httpapi/handlers/questions_test.go` (fakeQuestionRepo, after `Delete` at line 93)

**Interfaces:**
- Consumes: `linkTagTx` (`question_repo.go:416`), `pgvector.NewVector`, the `question_hash` UNIQUE constraint (`migrations/0002_core.sql:31`), the 14-column INSERT list mirrored from `Create` (`question_repo.go:40-50`).
- Produces: `UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)` on `storage.QuestionRepo` — consumed by Task 9 (via the importer's narrow `QuestionUpserter` port, which has the identical signature).

- [ ] **Step 1: Write the failing integration tests**

Create `internal/storage/postgres/question_import_test.go`:

```go
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// importQuestion builds a verified question as the importer would (spec §5.6).
func importQuestion(text string, choices, answers []string, tags []string, embedding []float32, verifiedBy string) *domain.Question {
	norm := domain.NormalizeQuestion(text)
	now := time.Now().UTC().Format(time.RFC3339)
	typ := domain.InferQuestionType(choices)
	return &domain.Question{
		Number: 0, Text: text, TextNorm: norm, TextHash: domain.HashQuestion(norm),
		Choices: choices, Answers: answers,
		ChoiceLabeling: domain.ChoiceLabelingLetter, Type: typ,
		Confidence: 0.99, Explanation: "imported", Embedding: embedding,
		Status: domain.QuestionStatusVerified, VerifiedAt: &now, VerifiedBy: &verifiedBy,
		Tags: tags,
	}
}

func TestQuestionRepo_UpsertFromImport_InsertThenUpdate(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	uploader, err := userRepo.Create(ctx, "importer@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	q1 := importQuestion("What is 2+2?", []string{"3", "4"}, []string{"4"}, []string{"arith", "import"}, nil, uploader.ID)
	created, err := repo.UpsertFromImport(ctx, q1)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !created {
		t.Error("first upsert created = false, want true")
	}

	stored, err := repo.FindExact(ctx, q1.TextHash)
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}

	// Same hash, new content — the file wins (spec §7).
	q2 := importQuestion("What is 2+2?", []string{"3", "4", "5"}, []string{"4"}, []string{"math", "import"}, nil, uploader.ID)
	q2.Explanation = "updated explanation"
	created, err = repo.UpsertFromImport(ctx, q2)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Error("second upsert created = true, want false (hash conflict)")
	}

	after, err := repo.FindExact(ctx, q1.TextHash)
	if err != nil {
		t.Fatalf("FindExact after update: %v", err)
	}
	if after.ID != stored.ID {
		t.Errorf("ID changed across upsert: %q -> %q (must be update-in-place)", stored.ID, after.ID)
	}
	if len(after.Choices) != 3 || after.Choices[2] != "5" {
		t.Errorf("choices not replaced: %v", after.Choices)
	}
	if after.Explanation != "updated explanation" {
		t.Errorf("explanation = %q, want replaced", after.Explanation)
	}
	if after.Status != domain.QuestionStatusVerified {
		t.Errorf("status = %q, want verified", after.Status)
	}
	if after.VerifiedAt == nil || *after.VerifiedAt == "" {
		t.Error("verified_at not set on upsert")
	}
	if after.VerifiedBy == nil || *after.VerifiedBy != uploader.ID {
		t.Errorf("verified_by = %v, want %q", after.VerifiedBy, uploader.ID)
	}

	// Tags fully replaced by the file's set.
	ev, err := repo.FindExpertByID(ctx, stored.ID)
	if err != nil {
		t.Fatalf("FindExpertByID: %v", err)
	}
	if len(ev.Tags) != 2 {
		t.Errorf("tags = %v, want exactly [math import] (replaced, not merged)", ev.Tags)
	}
}

func TestQuestionRepo_UpsertFromImport_EmbeddingCoalesce(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	uploader, err := userRepo.Create(ctx, "embed@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	emb := make([]float32, 1536)
	emb[0] = 1.0

	var hasEmbedding bool
	row := func(id string) {
		if err := pool.QueryRow(ctx, `SELECT embedding IS NOT NULL FROM questions WHERE id = $1`, id).Scan(&hasEmbedding); err != nil {
			t.Fatalf("query embedding: %v", err)
		}
	}

	// Insert with an embedding; upsert with nil ⇒ COALESCE keeps the old vector.
	q := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, emb, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, q); err != nil {
		t.Fatalf("insert: %v", err)
	}
	stored, err := repo.FindExact(ctx, q.TextHash)
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}

	qNil := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, nil, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, qNil); err != nil {
		t.Fatalf("nil-embedding upsert: %v", err)
	}
	row(stored.ID)
	if !hasEmbedding {
		t.Error("embedding lost on nil upsert — COALESCE must preserve it")
	}

	// Upsert with a fresh embedding ⇒ stored.
	fresh := make([]float32, 1536)
	fresh[1] = 1.0
	qFresh := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, fresh, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, qFresh); err != nil {
		t.Fatalf("fresh-embedding upsert: %v", err)
	}
	row(stored.ID)
	if !hasEmbedding {
		t.Error("fresh embedding not stored")
	}
}

func TestQuestionRepo_UpsertFromImport_SessionLinkedSucceeds(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "linked@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	// Pre-existing session-linked question (as the pipeline would have made it).
	existing := importQuestion("Linked question?", []string{"a", "b"}, []string{"a"}, []string{"ai-generated"}, nil, user.ID)
	existing.Status = domain.QuestionStatusModeration
	existing.VerifiedAt = nil
	existing.VerifiedBy = nil
	qID, err := repo.Create(ctx, existing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9); err != nil {
		t.Fatalf("LinkToSession: %v", err)
	}

	// Import upsert on the same hash must succeed (the Delete guard is never in play).
	upd := importQuestion("Linked question?", []string{"a", "b", "c"}, []string{"b"}, []string{"import"}, nil, user.ID)
	created, err := repo.UpsertFromImport(ctx, upd)
	if err != nil {
		t.Fatalf("upsert on session-linked question: %v", err)
	}
	if created {
		t.Error("created = true, want false (existing hash)")
	}
	after, err := repo.FindByID(ctx, qID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if len(after.Choices) != 3 || after.Status != domain.QuestionStatusVerified {
		t.Errorf("after = %+v, want 3 choices + verified", after)
	}
}

// Regression guard (spec §7.2, §12.2): an import-update of an image-linked,
// fully-resolved question must NOT trigger cleanupImageBytesTx semantics.
func TestQuestionRepo_UpsertFromImport_PreservesImageBytes(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "bytes@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("original-bytes"), "image/jpeg", 800, 600)

	existing := importQuestion("Bytes question?", []string{"a", "b"}, []string{"a"}, []string{"ai-generated"}, nil, user.ID)
	qID, err := repo.Create(ctx, existing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9); err != nil {
		t.Fatalf("LinkToSession: %v", err)
	}

	// The question is already verified, so after the upsert the image has zero
	// unresolved questions — exactly the condition under which UpdateByExpert's
	// cleanupImageBytesTx would null the bytes. UpsertFromImport must not.
	upd := importQuestion("Bytes question?", []string{"a", "b"}, []string{"b"}, []string{"import"}, nil, user.ID)
	if _, err := repo.UpsertFromImport(ctx, upd); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var original []byte
	if err := pool.QueryRow(ctx, `SELECT original FROM images WHERE id = $1`, imgID).Scan(&original); err != nil {
		t.Fatalf("query image: %v", err)
	}
	if original == nil {
		t.Error("image bytes were nulled by import upsert — cleanupImageBytesTx semantics leaked in")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/storage/postgres/ -run TestQuestionRepo_UpsertFromImport -timeout 180s -v`
(Docker required — testcontainers pulls `pgvector/pgvector:pg16`)
Expected: FAIL to compile — `repo.UpsertFromImport undefined`

- [ ] **Step 3: Extend the port and implement**

In `internal/storage/ports.go`, add to the `QuestionRepo` interface after `Create` (line 86):

```go
	// UpsertFromImport inserts q or, on question_hash conflict, replaces all
	// content fields in place (the file wins). The question ID never changes;
	// session/image linkage and image bytes are untouched. created is true on
	// insert, false on update. Tags are fully replaced by q.Tags.
	UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)
```

Create `internal/storage/postgres/question_import.go`:

```go
package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgvector/pgvector-go"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// UpsertFromImport implements storage.QuestionRepo.UpsertFromImport (spec §7.1).
// One transaction: ON CONFLICT (question_hash) upsert + tag delete-and-reinsert.
// It deliberately does NOT call cleanupImageBytesTx — image bytes are a
// cross-aggregate concern of expert edits, not of import.
//
// question_normalized is absent from the UPDATE SET on purpose: a hash
// conflict implies identical normalized text under the current
// NormalizeQuestion, so the stored value is already correct. embedding uses
// COALESCE: a hash match implies identical text, so the old vector stays
// valid when the new one is nil.
func (r *QuestionRepo) UpsertFromImport(ctx context.Context, q *domain.Question) (bool, error) {
	choicesJSON, _ := json.Marshal(q.Choices)
	answersJSON, _ := json.Marshal(q.Answers)

	var embedding interface{}
	if q.Embedding != nil {
		embedding = pgvector.NewVector(q.Embedding)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin import upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	var id string
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    choices, answers, choice_labeling, question_type, confidence,
		    explanation, embedding, status, verified_at, verified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
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
		RETURNING id, (xmax = 0)
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		choicesJSON, answersJSON, q.ChoiceLabeling, q.Type,
		q.Confidence, q.Explanation, embedding, q.Status,
		q.VerifiedAt, q.VerifiedBy,
	).Scan(&id, &created)
	if err != nil {
		return false, fmt.Errorf("import upsert: %w", err)
	}

	// Tag delete-and-reinsert, same transaction as the upsert.
	if _, err := tx.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id); err != nil {
		return false, fmt.Errorf("clear tags: %w", err)
	}
	for _, tagName := range q.Tags {
		if err := r.linkTagTx(ctx, tx, id, tagName); err != nil {
			return false, fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit import upsert: %w", err)
	}
	return created, nil
}
```

- [ ] **Step 4: Add `UpsertFromImport` stubs to both QuestionRepo test fakes**

In `internal/pipeline/pipeline_test.go`, after the fakeQuestionRepo `Delete` method (line 231):

```go
func (r *fakeQuestionRepo) UpsertFromImport(context.Context, *domain.Question) (bool, error) {
	return false, nil
}
```

In `internal/httpapi/handlers/questions_test.go`, after the fakeQuestionRepo `Delete` method (line 93):

```go
func (f *fakeQuestionRepo) UpsertFromImport(context.Context, *domain.Question) (bool, error) {
	return false, nil
}
```

- [ ] **Step 5: Run integration tests to verify they pass**

Run: `go test ./internal/storage/postgres/ -run TestQuestionRepo_UpsertFromImport -timeout 180s -v`
Expected: PASS (all 4 tests)

Then the full integration gate (existing repo tests must stay green):

Run: `go test ./internal/storage/postgres/ -timeout 180s`
Expected: PASS

- [ ] **Step 6: Unit suite, vet, commit**

Run: `go test -short ./... && go vet ./...`
Expected: PASS, no vet output

```bash
git add internal/storage/ports.go internal/storage/postgres/question_import.go internal/storage/postgres/question_import_test.go internal/pipeline/pipeline_test.go internal/httpapi/handlers/questions_test.go
git commit -m "feat(storage): UpsertFromImport with hash-conflict update-in-place and tag replace"
```

---

### Task 9: `importer.Service` orchestration

**Files:**
- Create: `internal/importer/importer.go`
- Test: `internal/importer/importer_test.go`

**Interfaces:**
- Consumes: `parseCSV`/`parseXLSX` (Tasks 4–5), `buildQuestion` (Task 6), `Report`/`RowError`/sentinels (Task 4). The narrow ports match signatures produced by Tasks 7–8.
- Produces:
  - `type QuestionUpserter interface { UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error) }`
  - `type BatchEmbedder interface { EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) }` (nil-able)
  - `type Service`, `func New(questions QuestionUpserter, embedder BatchEmbedder, maxRows int, log *slog.Logger) *Service`
  - `func (s *Service) Import(ctx context.Context, r io.Reader, kind FileKind, userID string) (Report, error)`
  - `const embedChunkSize = 100`

- [ ] **Step 1: Write the failing tests**

Create `internal/importer/importer_test.go`:

```go
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// --- fakes ---

type fakeUpserter struct {
	createdByHash map[string]bool
	questions     []*domain.Question
	err           error
}

func newFakeUpserter() *fakeUpserter {
	return &fakeUpserter{createdByHash: map[string]bool{}}
}

func (f *fakeUpserter) UpsertFromImport(_ context.Context, q *domain.Question) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.questions = append(f.questions, q)
	if f.createdByHash[q.TextHash] {
		return false, nil
	}
	f.createdByHash[q.TextHash] = true
	return true, nil
}

type fakeBatchEmbedder struct {
	calls int
	err   error
}

func (f *fakeBatchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func csvReader(rows ...string) io.Reader {
	return strings.NewReader(strings.Join(rows, "\n") + "\n")
}

// --- tests ---

func TestService_ImportHappyPath(t *testing.T) {
	up := newFakeUpserter()
	emb := &fakeBatchEmbedder{}
	svc := New(up, emb, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		"What is 2+2?,3;4,4,math,arith",
		"Explain entropy.,,disorder increases,physics,",
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 2 || rep.Created != 2 || rep.Updated != 0 || rep.Failed != 0 {
		t.Errorf("report = %+v, want total=2 created=2", rep)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %v, want none", rep.Errors)
	}
	if emb.calls != 1 {
		t.Errorf("embed calls = %d, want 1 (one chunk)", emb.calls)
	}
	for i, q := range up.questions {
		if q.Embedding == nil {
			t.Errorf("question %d has nil embedding, want assigned", i)
		}
	}
}

func TestService_InFileDuplicatesLastWins(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		"What is 2+2?,3;4,4,first,",
		"What is 2+2?,3;4,4,second,",
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 2 || rep.Created != 1 || rep.Updated != 1 || rep.Failed != 0 {
		t.Errorf("report = %+v, want total=2 created=1 updated=1", rep)
	}
	// Last wins: the second occurrence's content is what was upserted last.
	last := up.questions[len(up.questions)-1]
	if last.Explanation != "second" {
		t.Errorf("last upserted explanation = %q, want %q (last-wins)", last.Explanation, "second")
	}
}

func TestService_RowFailureIsolation(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		"Good one?,a;b,a,,",
		"Bad one?,only,a,,", // 1 choice ⇒ row error
		"Good two?,x;y,y,,",
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 3 || rep.Created != 2 || rep.Failed != 1 {
		t.Errorf("report = %+v, want total=3 created=2 failed=1", rep)
	}
	if len(rep.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly 1", rep.Errors)
	}
	if rep.Errors[0].Row != 2 {
		t.Errorf("error row = %d, want 2 (1-based)", rep.Errors[0].Row)
	}
	if rep.Errors[0].Message != "multiple_choice requires at least 2 choices" {
		t.Errorf("error message = %q", rep.Errors[0].Message)
	}
}

func TestService_TooManyRows(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 2, quietLog())

	_, err := svc.Import(context.Background(), csvReader(
		"q1?,a;b,a,,",
		"q2?,a;b,a,,",
		"q3?,a;b,a,,",
	), KindCSV, "user-1")
	if err != ErrTooManyRows {
		t.Errorf("err = %v, want ErrTooManyRows", err)
	}
	if len(up.questions) != 0 {
		t.Errorf("upserted %d questions, want 0 (rejected before processing)", len(up.questions))
	}
}

func TestService_NilEmbedderSkipsEmbedding(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog()) // nil embedder

	rep, err := svc.Import(context.Background(), csvReader("q?,a;b,a,,"), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.Created != 1 {
		t.Errorf("created = %d, want 1", rep.Created)
	}
	if up.questions[0].Embedding != nil {
		t.Error("embedding assigned despite nil embedder")
	}
}

func TestService_EmbedChunkFailureSkipsRemaining(t *testing.T) {
	up := newFakeUpserter()
	emb := &fakeBatchEmbedder{err: errors.New("embedder down")}
	svc := New(up, emb, 1000, quietLog())

	// 150 rows ⇒ 2 chunks of (100, 50). First chunk fails ⇒ only 1 call.
	rows := make([]string, 150)
	for i := range rows {
		rows[i] = fmt.Sprintf("Question number %d?,a;b,a,,", i)
	}
	rep, err := svc.Import(context.Background(), csvReader(rows...), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if emb.calls != 1 {
		t.Errorf("embed calls = %d, want 1 (fail-fast skips remaining chunks)", emb.calls)
	}
	if rep.Created != 150 || rep.Failed != 0 {
		t.Errorf("report = %+v, want 150 created, 0 failed (embedding is best-effort)", rep)
	}
	for i, q := range up.questions {
		if q.Embedding != nil {
			t.Errorf("question %d got embedding despite chunk failure", i)
		}
	}
}

func TestService_RowErrorCap(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 1000, quietLog())

	// 101 invalid rows (1 choice each) ⇒ Failed=101 but Errors capped at 100.
	rows := make([]string, 101)
	for i := range rows {
		rows[i] = fmt.Sprintf("Bad %d?,only,a,,", i)
	}
	rep, err := svc.Import(context.Background(), csvReader(rows...), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 101 || rep.Failed != 101 {
		t.Errorf("report = %+v, want total=101 failed=101", rep)
	}
	if len(rep.Errors) != maxImportRowErrors {
		t.Errorf("len(Errors) = %d, want capped at %d", len(rep.Errors), maxImportRowErrors)
	}
}

func TestService_UpsertFailureRecorded(t *testing.T) {
	up := newFakeUpserter()
	up.err = errors.New("db exploded")
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader("q?,a;b,a,,"), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.Failed != 1 || rep.Created != 0 {
		t.Errorf("report = %+v, want failed=1 created=0", rep)
	}
	if !strings.Contains(rep.Errors[0].Message, "db exploded") {
		t.Errorf("error message = %q, want upsert error surfaced", rep.Errors[0].Message)
	}
}

func TestService_ReportArithmeticAndRowNumbers(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		"New question?,a;b,a,,",          // created (row 1)
		"Bad row?,solo,a,,",              // failed  (row 2)
		"New question?,a;b,a,,updated,",  // updated (row 3, in-file dup)
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != rep.Created+rep.Updated+rep.Failed {
		t.Errorf("arithmetic broken: %+v", rep)
	}
	if rep.Created != 1 || rep.Updated != 1 || rep.Failed != 1 || rep.TotalRows != 3 {
		t.Errorf("report = %+v", rep)
	}
	if rep.Errors[0].Row != 2 {
		t.Errorf("failed row reported as %d, want 2 (1-based file row)", rep.Errors[0].Row)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/importer/ -run TestService -v`
Expected: FAIL — `undefined: New` / `Service`

- [ ] **Step 3: Implement `importer.go`**

Create `internal/importer/importer.go`:

```go
// Package importer synchronously imports exam questions from CSV/XLSX
// uploads into the canonical questions table: parse → validate → embed
// (best effort) → per-row upsert (spec §6).
package importer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// embedChunkSize bounds each EmbedBatch call (spec §8).
const embedChunkSize = 100

// QuestionUpserter is the consumer-side narrow port for storage.
type QuestionUpserter interface {
	UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)
}

// BatchEmbedder is the consumer-side narrow port for embeddings. It is
// nil-able: a nil embedder skips the embedding step silently (spec §8).
type BatchEmbedder interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

type Service struct {
	questions QuestionUpserter
	embedder  BatchEmbedder
	maxRows   int
	log       *slog.Logger
}

func New(questions QuestionUpserter, embedder BatchEmbedder, maxRows int, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{questions: questions, embedder: embedder, maxRows: maxRows, log: log}
}

// validRow pairs a validated question with its 1-based file row number.
type validRow struct {
	row int
	q   *domain.Question
}

// Import runs the full synchronous pipeline (spec §6). File-level failures
// (parse errors, row cap) return an error; row-level failures are recorded
// in the Report and never abort the import.
func (s *Service) Import(ctx context.Context, r io.Reader, kind FileKind, userID string) (Report, error) {
	var rows [][]string
	var err error
	switch kind {
	case KindCSV:
		rows, err = parseCSV(r)
	case KindXLSX:
		rows, err = parseXLSX(r)
	default:
		return Report{}, ErrUnsupportedFormat
	}
	if err != nil {
		return Report{}, err
	}
	if len(rows) > s.maxRows {
		return Report{}, ErrTooManyRows
	}

	rep := Report{TotalRows: len(rows), Errors: []RowError{}}

	now := time.Now().UTC()
	valid := make([]validRow, 0, len(rows))
	for i, raw := range rows {
		rowNum := i + 1 // no header row: file row = data row (spec §4.2)
		cols, shapeErr := normalizeRow(raw)
		if shapeErr != nil {
			rep.addRowError(rowNum, shapeErr.Error())
			continue
		}
		q, valErr := buildQuestion(cols, userID, now)
		if valErr != nil {
			rep.addRowError(rowNum, valErr.Error())
			continue
		}
		valid = append(valid, validRow{row: rowNum, q: q})
	}

	s.embedAll(ctx, valid)

	// Per-row upsert, each in its own transaction (atomic per row). A failed
	// row is recorded; later rows proceed — bad rows never block good rows.
	for _, vr := range valid {
		created, err := s.questions.UpsertFromImport(ctx, vr.q)
		if err != nil {
			s.log.Warn("import upsert failed", "row", vr.row, "err", err)
			rep.addRowError(vr.row, fmt.Sprintf("upsert failed: %v", err))
			continue
		}
		if created {
			rep.Created++
		} else {
			rep.Updated++
		}
	}
	return rep, nil
}

// embedAll fetches embeddings in sequential chunks of embedChunkSize. On the
// FIRST failed chunk it logs a warning and skips embedding for all remaining
// chunks (fail-fast, spec §8) — affected rows import with nil embedding and
// the report is unaffected. A nil embedder skips the step entirely.
func (s *Service) embedAll(ctx context.Context, valid []validRow) {
	if s.embedder == nil || len(valid) == 0 {
		return
	}
	for start := 0; start < len(valid); start += embedChunkSize {
		end := start + embedChunkSize
		if end > len(valid) {
			end = len(valid)
		}
		chunk := valid[start:end]
		texts := make([]string, len(chunk))
		for i, vr := range chunk {
			texts[i] = vr.q.Text
		}
		vecs, err := s.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			s.log.Warn("import embedding chunk failed — skipping remaining chunks",
				"chunk_start", start, "err", err)
			return
		}
		if len(vecs) != len(chunk) {
			s.log.Warn("import embedding chunk size mismatch — skipping remaining chunks",
				"chunk_start", start, "got", len(vecs), "want", len(chunk))
			return
		}
		for i, vec := range vecs {
			chunk[i].q.Embedding = vec
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -short ./internal/importer/ -v`
Expected: PASS (Service tests + Tasks 4–6 tests)

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/importer/`
Expected: no output

```bash
git add internal/importer/importer.go internal/importer/importer_test.go
git commit -m "feat(importer): Service orchestration — validate, chunked best-effort embed, per-row upsert"
```

---

### Task 10: HTTP layer — report DTOs + `QuestionImportHandler`

**Files:**
- Modify: `internal/httpapi/dto/responses.go`
- Create: `internal/httpapi/handlers/question_import.go`
- Test: `internal/httpapi/handlers/question_import_test.go`

**Interfaces:**
- Consumes: `importer.Service` (Task 9), `importer.SniffKind` + sentinels (Task 4), `config.UploadConfig.MaxBytes`, `errorResponse` (`handlers/common.go`), `domain.HTTPStatus` (`domain/errors.go:33`).
- Produces:
  - `dto.ImportReportResponse{TotalRows, Created, Updated, Failed int; Errors []ImportRowError}` with JSON keys `total_rows, created, updated, failed, errors`
  - `dto.ImportRowError{Row int; Message string}` with JSON keys `row, message`
  - `func NewQuestionImportHandler(svc *importer.Service, uploadCfg config.UploadConfig) *QuestionImportHandler`
  - `func (h *QuestionImportHandler) Upload(c *gin.Context)` — registered in Task 11.

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/handlers/question_import_test.go`:

```go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/importer"
)

// --- fakes ---

type fakeImportUpserter struct {
	createdByHash map[string]bool
	userIDs       []string
}

func (f *fakeImportUpserter) UpsertFromImport(_ context.Context, q *domain.Question) (bool, error) {
	if f.createdByHash == nil {
		f.createdByHash = map[string]bool{}
	}
	if q.VerifiedBy != nil {
		f.userIDs = append(f.userIDs, *q.VerifiedBy)
	}
	if f.createdByHash[q.TextHash] {
		return false, nil
	}
	f.createdByHash[q.TextHash] = true
	return true, nil
}

// --- helpers ---

func newImportHandler(maxBytes int64) (*QuestionImportHandler, *fakeImportUpserter) {
	up := &fakeImportUpserter{}
	svc := importer.New(up, nil, 100, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return NewQuestionImportHandler(svc, config.UploadConfig{MaxBytes: maxBytes}), up
}

func newImportRouter(h *QuestionImportHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/api/v1/questions/upload", h.Upload)
	return r
}

// newFileMultipartForm creates a multipart form with a "file" field.
func newFileMultipartForm(buf *bytes.Buffer, data []byte) *multipart.Writer {
	w := multipart.NewWriter(buf)
	fw, _ := w.CreateFormFile("file", "questions.csv")
	fw.Write(data)
	w.Close()
	return w
}

func postUpload(t *testing.T, r *gin.Engine, body *bytes.Buffer, w *multipart.Writer) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/questions/upload", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func sniffable512(prefix []byte) []byte {
	buf := make([]byte, 512)
	copy(buf, prefix)
	return buf
}

// --- tests ---

func TestImportHandler_UploadCSVReport(t *testing.T) {
	h, up := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	csvData := []byte("What is 2+2?,3;4,4,math,arith\nBad row?,only,a,,\n")
	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, csvData)
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp dto.ImportReportResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalRows != 2 || resp.Created != 1 || resp.Updated != 0 || resp.Failed != 1 {
		t.Errorf("report = %+v", resp)
	}
	if len(resp.Errors) != 1 || resp.Errors[0].Row != 2 {
		t.Errorf("errors = %+v, want one error at row 2", resp.Errors)
	}
	if len(up.userIDs) == 0 || up.userIDs[0] != "user-1" {
		t.Errorf("verified_by = %v, want user-1 from JWT context", up.userIDs)
	}
}

func TestImportHandler_UnsupportedFormat(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, sniffable512([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}))
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "validation" || env.Error.Message != "unsupported file format" {
		t.Errorf("body = %s, want validation/unsupported file format", rr.Body.String())
	}
}

func TestImportHandler_LegacyXLS(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, sniffable512([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}))
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("legacy .xls not supported")) {
		t.Errorf("body = %s, want legacy .xls message", rr.Body.String())
	}
}

func TestImportHandler_EmptyFile(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, []byte{})
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("empty file")) {
		t.Errorf("body = %s, want empty file message", rr.Body.String())
	}
}

func TestImportHandler_OversizeBody(t *testing.T) {
	h, _ := newImportHandler(16) // 16-byte cap
	r := newImportRouter(h)

	csvData := []byte("What is 2+2?,3;4,4,math,arith\n")
	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, csvData)
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize body", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -short ./internal/httpapi/handlers/ -run TestImportHandler -v`
Expected: FAIL to compile — `undefined: NewQuestionImportHandler`, `dto.ImportReportResponse`

- [ ] **Step 3: Implement DTOs and the handler**

In `internal/httpapi/dto/responses.go`, append:

```go
// ImportRowError is one failed row in an import report: 1-based file row
// number plus the validation/upsert message.
type ImportRowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportReportResponse is returned on POST /api/v1/questions/upload (200 OK)
// whenever the file itself parses — even if every row failed (spec §4.2).
type ImportReportResponse struct {
	TotalRows int             `json:"total_rows"`
	Created   int             `json:"created"`
	Updated   int             `json:"updated"`
	Failed    int             `json:"failed"`
	Errors    []ImportRowError `json:"errors"`
}
```

Create `internal/httpapi/handlers/question_import.go`:

```go
package handlers

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/importer"
)

// QuestionImportHandler serves POST /api/v1/questions/upload (spec §4).
type QuestionImportHandler struct {
	svc       *importer.Service
	uploadCfg config.UploadConfig
}

func NewQuestionImportHandler(svc *importer.Service, uploadCfg config.UploadConfig) *QuestionImportHandler {
	return &QuestionImportHandler{svc: svc, uploadCfg: uploadCfg}
}

// Upload reads a multipart CSV/XLSX file, imports it synchronously, and
// returns the per-row report (200) or a file-level error envelope (400).
// Read pattern mirrors ImageHandler.Upload: MaxBytesReader → FormFile → ReadAll.
func (h *QuestionImportHandler) Upload(c *gin.Context) {
	// Enforce size cap (spec §4.3).
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.uploadCfg.MaxBytes)

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	kind, err := importer.SniffKind(data)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}

	userID := c.GetString("user_id")
	// The multipart reader is fully drained by io.ReadAll + SniffKind, so a
	// fresh bytes.Reader over the byte slice — never the multipart stream —
	// is passed to the importer (spec §9.5).
	rep, err := h.svc.Import(c.Request.Context(), bytes.NewReader(data), kind, userID)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}

	resp := dto.ImportReportResponse{
		TotalRows: rep.TotalRows,
		Created:   rep.Created,
		Updated:   rep.Updated,
		Failed:    rep.Failed,
		Errors:    make([]dto.ImportRowError, 0, len(rep.Errors)),
	}
	for _, re := range rep.Errors {
		resp.Errors = append(resp.Errors, dto.ImportRowError{Row: re.Row, Message: re.Message})
	}
	c.JSON(http.StatusOK, resp)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -short ./internal/httpapi/handlers/ -v`
Expected: PASS (new import tests + the whole pre-existing suite)

- [ ] **Step 5: Vet and commit**

Run: `go vet ./internal/httpapi/...`
Expected: no output

```bash
git add internal/httpapi/dto/responses.go internal/httpapi/handlers/question_import.go internal/httpapi/handlers/question_import_test.go
git commit -m "feat(httpapi): QuestionImportHandler with report DTOs and file-level error mapping"
```

---

### Task 11: Route registration + wiring (endpoint goes live)

**Files:**
- Modify: `internal/httpapi/server.go` (`Server` struct lines 16–27; `NewServer` lines 29–60; questions group lines 113–120)
- Modify: `internal/app/wire.go` (NewServer call at lines 64–67)
- Modify: `internal/storage/postgres/server_auth_test.go` (`bootTestServer` NewServer call, lines 27–37)
- Test: `internal/httpapi/server_test.go`

**Interfaces:**
- Consumes: `importer.New` (Task 9), `NewQuestionImportHandler` (Task 10), `cfg.Import.MaxRows` (Task 3), the nil-able `emb pipeline.AIEmbedder` (satisfies `importer.BatchEmbedder` when non-nil — typed-nil caveat, spec §8).
- Produces: live route `POST /api/v1/questions/upload` behind `AuthMiddleware` (group-wide) + `RoleGuard("expert", "admin")`; `httpapi.NewServer` grows to **11 parameters**.

- [ ] **Step 1: Write the failing test (route-level RoleGuard, mirrors existing pattern)**

Append to `internal/httpapi/server_test.go`:

```go
// TestQuestionUpload_RoleGuardRejectsUser verifies the upload route is gated by
// RoleGuard("expert", "admin"): a user-role caller gets 403 at the middleware
// layer before the handler runs. Mirrors the route wiring in registerRoutes.
func TestQuestionUpload_RoleGuardRejectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("user_id", "u1"); c.Next() })
	r.POST("/api/v1/questions/upload", RoleGuard("expert", "admin"), func(c *gin.Context) {
		t.Error("handler must not run for user role")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/questions/upload", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("user upload: got %d want 403", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it passes as-is**

This test mirrors wiring and passes without production changes (same as the existing PUT/POST guards). Its value is pinning the `RoleGuard("expert", "admin")` wiring for the new route. Step 3 makes the real wiring change.

Run: `go test -short ./internal/httpapi/ -run TestQuestionUpload -v`
Expected: PASS

- [ ] **Step 3: Wire the route into `server.go`**

In `internal/httpapi/server.go`:

1. Add the import to the import block:

```go
	"github.com/vlgrigoriev/coeus/internal/importer"
```

2. Add the field to the `Server` struct (after `embedder pipeline.AIEmbedder`):

```go
	importSvc    *importer.Service
```

3. Add the 11th parameter to `NewServer` (after `corsCfg config.CORSConfig`) and store it:

```go
func NewServer(
	userRepo storage.UserRepo,
	sessionRepo storage.SessionRepo,
	imageRepo storage.ImageRepo,
	questionRepo storage.QuestionRepo,
	jobQueue storage.JobQueue,
	jwtMgr *auth.JWTManager,
	pool *pgxpool.Pool,
	uploadCfg config.UploadConfig,
	embedder pipeline.AIEmbedder,
	corsCfg config.CORSConfig,
	importSvc *importer.Service,
) *Server {
```

```go
	s := &Server{
		router: r, userRepo: userRepo, sessionRepo: sessionRepo,
		imageRepo: imageRepo, questionRepo: questionRepo, jobQueue: jobQueue,
		jwtMgr: jwtMgr, pool: pool, uploadCfg: uploadCfg, embedder: embedder,
		importSvc: importSvc,
	}
```

4. Register the route inside the questions group (after `questions.POST("", ...)`):

```go
		questionImportHandler := handlers.NewQuestionImportHandler(s.importSvc, s.uploadCfg)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.POST("", RoleGuard("expert", "admin"), questionHandler.Create)
			questions.POST("/upload", RoleGuard("expert", "admin"), questionImportHandler.Upload)
			questions.PUT("/:id", RoleGuard("expert", "admin"), questionHandler.Update)
			questions.DELETE("/:id", RoleGuard("expert", "admin"), questionHandler.Delete)
		}
```

(Gin's per-method radix trees keep `POST /questions/upload` from conflicting with `POST /questions` or `GET /questions/:id` — spec §4.1.)

- [ ] **Step 4: Update `wire.go`**

In `internal/app/wire.go`, add `"github.com/vlgrigoriev/coeus/internal/importer"` to the import block, then change the `NewServer` call site (lines 64–67) to construct and pass the importer:

```go
	// emb must remain a nil-able interface variable: it is declared as
	// `var emb pipeline.AIEmbedder` and assigned only when
	// COEUS_AI_EMBEDDER_API_KEY is set (above). Interface-to-interface
	// assignment preserves true nil, so the importer Service's nil-checks
	// work. Never pass a concrete (*embedder.Embedder)(nil) (spec §8).
	imp := importer.New(questionRepo, emb, cfg.Import.MaxRows, log)

	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload, emb, cfg.Server.CORS, imp,
	)
```

Since `pipeline.AIEmbedder` now includes `EmbedBatch` (Task 7), a non-nil `emb` satisfies `importer.BatchEmbedder` directly; an unconfigured `emb` arrives as a true nil interface and the embedding step is skipped.

- [ ] **Step 5: Update the integration test boot helper**

In `internal/storage/postgres/server_auth_test.go`, `bootTestServer` (lines 27–37): add the 11th argument to the `httpapi.NewServer` call:

```go
	srv := httpapi.NewServer(
		userRepo,
		NewSessionRepo(pool),
		NewImageRepo(pool),
		NewQuestionRepo(pool),
		NewJobQueue(pool),
		jwt, pool,
		config.UploadConfig{},
		nil, // embedder unused
		config.CORSConfig{AllowedOrigins: []string{"*"}},
		nil, // import service unused by these routes
	)
```

- [ ] **Step 6: Full verification gate**

Run each, in order; all must pass:

```
go build ./...
go vet ./...
go test -short ./...
```

Expected: build/vet clean; entire unit suite PASS (compile check proves the two updated `NewServer` call sites and both fake stubs are consistent).

Then the integration gate (Docker required):

```
go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s
```

Expected: PASS (includes the 4 `UpsertFromImport` integration tests and the auth-route suite that exercises `bootTestServer`).

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/server_test.go internal/app/wire.go internal/storage/postgres/server_auth_test.go
git commit -m "feat(httpapi): register POST /api/v1/questions/upload behind expert/admin RoleGuard, wire importer"
```

---

## Self-Review Notes (already applied)

- **Spec coverage:** §4 API contract → Tasks 10–11; §5 format/parsing/validation → Tasks 4–6; §5.5 ValidateDraft + handler refactor → Tasks 1–2; §5.6 constants → Task 6; §6 pipeline order → Task 9; §7 upsert → Task 8; §8 EmbedBatch + typed-nil → Tasks 7, 11; §9.5 handler data flow (`bytes.NewReader`) → Task 10; §9.6 wiring → Task 11; §10 config → Task 3; §11 error taxonomy → Tasks 4, 10; §12.1 unit tests → Tasks 1, 3–7, 9, 10, 11; §12.2 integration tests (all four bullets incl. image-bytes regression guard) → Task 8.
- **Type consistency:** `UpsertFromImport(ctx, *domain.Question) (bool, error)` identical across `storage.QuestionRepo`, postgres impl, importer's `QuestionUpserter`, and all four fake stubs; `EmbedBatch(ctx, []string) ([][]float32, error)` identical across `pipeline.AIEmbedder`, `*embedder.Embedder`, importer's `BatchEmbedder`, and both fake embedders; `buildQuestion([5]string, string, time.Time)` matches Task 9's call; `normalizeRow` returns `[5]string` which is `buildQuestion`'s input type.
- **Deliberate deviations from placeholder-free ideals:** none — every step carries complete code and exact commands.
