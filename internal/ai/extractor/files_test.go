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
		{"IMAGE/PNG", "image.png"},
		{" image/jpeg ", "image.jpg"},
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

func TestUploadImage_EmptyIDIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "", "status": "ready"})
	}))
	defer srv.Close()

	e := New(testCfg(srv.URL, 10*time.Second), quietLogger())
	_, err := e.uploadImage(context.Background(), []byte("x"), "image/png")
	if err == nil {
		t.Fatal("expected error for empty file id, got nil")
	}
}

func TestExtractor_CleanupOnCancelDeleteStillRuns(t *testing.T) {
	var deleteCalls int
	stopCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-x", "status": "ready"})
		case strings.HasPrefix(r.URL.Path, "/files/") && r.Method == http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/chat/completions" && r.Method == http.MethodPost:
			// Block until either the client disconnects or test cleanup signals.
			select {
			case <-r.Context().Done():
			case <-stopCh:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer close(stopCh)

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
		t.Fatal("DELETE /files/{id} should run even after caller ctx cancel (via WithoutCancel)")
	}
}
