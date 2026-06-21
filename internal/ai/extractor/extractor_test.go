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
		reqBody, _ := io.ReadAll(r.Body)
		lastBody = string(reqBody)
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
