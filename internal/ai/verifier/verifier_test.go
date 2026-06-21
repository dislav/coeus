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
		{Number: 1, Text: "What is 2+2?",
			Choices: []pipeline.Answer{{ID: "A", Text: "3"}, {ID: "B", Text: "4"}},
			Answers: []pipeline.Answer{{ID: "B", Text: "4"}}, Confidence: 0.95, Tags: []string{"math"}},
		{Number: 2, Text: "Capital of France?",
			Choices: []pipeline.Answer{{ID: "A", Text: "London"}, {ID: "B", Text: "Paris"}},
			Answers: []pipeline.Answer{{ID: "B", Text: "Paris"}}, Confidence: 0.90, Tags: []string{"geography"}},
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
