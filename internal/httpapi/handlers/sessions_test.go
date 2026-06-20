package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
)

// --- Fakes ---

type fakeSessionRepo struct {
	created   *domain.Session
	list      []*domain.Session
	session   *domain.Session // returned by FindByID
	err       error
	closed    bool
}

func (f *fakeSessionRepo) Create(_ context.Context, userID string, dur, buf int) (*domain.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = &domain.Session{
		ID: "sess-new", UserID: userID, DurationSeconds: dur, BufferSeconds: buf,
		Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(time.Duration(dur+buf) * time.Second).Format(time.RFC3339),
	}
	return f.created, nil
}
func (f *fakeSessionRepo) FindByID(_ context.Context, _ string) (*domain.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}
func (f *fakeSessionRepo) ListByUser(_ context.Context, _ string, _, _ int) ([]*domain.Session, error) {
	return f.list, nil
}
func (f *fakeSessionRepo) Close(_ context.Context, _ string) error {
	f.closed = true
	return nil
}

type fakeImageRepoForSessions struct {
	count int
}

func (f *fakeImageRepoForSessions) Create(context.Context, string, []byte, string, int, int) (string, error) {
	return "", nil
}
func (f *fakeImageRepoForSessions) FindByID(context.Context, string) (*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoForSessions) ListBySession(context.Context, string) ([]*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoForSessions) UpdateEnhanced(context.Context, string, []byte) error { return nil }
func (f *fakeImageRepoForSessions) UpdateVerificationReport(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoForSessions) UpdateExtractionError(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoForSessions) CleanBytes(context.Context, string) error { return nil }
func (f *fakeImageRepoForSessions) CountBySession(_ context.Context, _ string) (int, error) {
	return f.count, nil
}

// --- Tests ---

func newSessionRouter(h *SessionHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/sessions", h.Create)
	r.GET("/sessions", h.List)
	r.GET("/sessions/:id", h.Get)
	r.POST("/sessions/:id/close", h.Close)
	return r
}

func TestSessionHandler_Create(t *testing.T) {
	repo := &fakeSessionRepo{}
	imgRepo := &fakeImageRepoForSessions{}
	h := NewSessionHandler(repo, imgRepo)
	r := newSessionRouter(h)

	body, _ := json.Marshal(dto.CreateSessionRequest{DurationSeconds: 3600, BufferSeconds: 300})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions", bytes.NewReader(body)))

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	var resp dto.SessionResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != domain.SessionStatusOpen {
		t.Errorf("status = %q, want open", resp.Status)
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at should not be empty")
	}
}

func TestSessionHandler_CreateValidation(t *testing.T) {
	repo := &fakeSessionRepo{}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	// Missing duration_seconds
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions", bytes.NewReader([]byte(`{}`))))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSessionHandler_List(t *testing.T) {
	repo := &fakeSessionRepo{list: []*domain.Session{
		{ID: "s1", Status: domain.SessionStatusOpen, ExpiresAt: "2026-12-01T00:00:00Z"},
		{ID: "s2", Status: domain.SessionStatusClosed, ExpiresAt: "2026-12-02T00:00:00Z"},
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions?page=1&per_page=10", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp dto.SessionListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(resp.Data))
	}
	if resp.Page != 1 || resp.PerPage != 10 {
		t.Errorf("pagination wrong: page=%d per_page=%d", resp.Page, resp.PerPage)
	}
}

func TestSessionHandler_Get(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		DurationSeconds: 3600, BufferSeconds: 300,
		StartedAt: "2026-06-20T12:00:00Z", ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	imgRepo := &fakeImageRepoForSessions{count: 3}
	h := NewSessionHandler(repo, imgRepo)
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-1", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp dto.SessionDetailResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ImageCount != 3 {
		t.Errorf("image_count = %d, want 3", resp.ImageCount)
	}
}

func TestSessionHandler_GetNotFound(t *testing.T) {
	repo := &fakeSessionRepo{err: domain.ErrNotFound}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-404", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSessionHandler_GetWrongOwnership(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "other-user", Status: domain.SessionStatusOpen,
		ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sessions/sess-1", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (don't leak)", w.Code)
	}
}

func TestSessionHandler_Close(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: "2026-06-20T13:05:00Z",
	}}
	h := NewSessionHandler(repo, &fakeImageRepoForSessions{})
	r := newSessionRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/close", nil))

	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if !repo.closed {
		t.Error("Close was not called")
	}
}
