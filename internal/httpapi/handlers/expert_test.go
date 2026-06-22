package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type fakeImageRepo struct {
	byID func(id string) (*domain.Image, error)
}

func (f *fakeImageRepo) Create(context.Context, string, []byte, string, int, int) (string, error) {
	return "", nil
}
func (f *fakeImageRepo) ListBySession(context.Context, string) ([]*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepo) UpdateEnhanced(context.Context, string, []byte) error          { return nil }
func (f *fakeImageRepo) UpdateVerificationReport(context.Context, string, []byte) error { return nil }
func (f *fakeImageRepo) UpdateExtractionError(context.Context, string, []byte) error    { return nil }
func (f *fakeImageRepo) CleanBytes(context.Context, string) error                       { return nil }
func (f *fakeImageRepo) CountBySession(context.Context, string) (int, error)            { return 0, nil }
func (f *fakeImageRepo) FindByID(ctx context.Context, id string) (*domain.Image, error) {
	return f.byID(id)
}

func newExpertRouter(imgs storage.ImageRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewExpertHandler(imgs)
	r.GET("/api/v1/images/:id", h.GetImage)
	r.GET("/api/v1/images/:id/verification-report", h.GetVerificationReport)
	return r
}

func TestGetImage_ServesOriginalBytes(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", Original: []byte("PNGDATA"), Mime: "image/png"}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	if w.Body.String() != "PNGDATA" {
		t.Errorf("body: got %q want PNGDATA", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("content-type: got %q want image/png", w.Header().Get("Content-Type"))
	}
}

func TestGetImage_BytesCleaned404(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", Original: nil, Mime: "image/png"}, nil // cleaned
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cleaned bytes: got %d want 404", w.Code)
	}
}

func TestGetImage_NotFound(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return nil, domain.ErrNotFound
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestGetImage_RepoError500(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return nil, errors.New("boom")
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500", w.Code)
	}
}

func TestGetVerificationReport_Present(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", VerificationReport: []byte(`{"flag":true}`)}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1/verification-report", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	if w.Body.String() != `{"flag":true}` {
		t.Errorf("body: got %q", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q want application/json", ct)
	}
}

func TestGetVerificationReport_NullWhenAbsent(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return &domain.Image{ID: "i1", VerificationReport: nil}, nil
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/i1/verification-report", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	var v interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body not valid JSON %q: %v", w.Body.String(), err)
	}
	if v != nil {
		t.Errorf("body: got %v want null", v)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q want application/json", ct)
	}
}

func TestGetVerificationReport_ImageNotFound404(t *testing.T) {
	imgs := &fakeImageRepo{byID: func(string) (*domain.Image, error) {
		return nil, domain.ErrNotFound
	}}
	r := newExpertRouter(imgs)
	w := doReq(t, r, "GET", "/api/v1/images/missing/verification-report", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}
