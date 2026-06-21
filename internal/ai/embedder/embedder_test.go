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
