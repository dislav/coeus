package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// fakeSessionRepo implements just enough for middleware tests.
type fakeSessionRepo struct {
	session *domain.Session
	err     error
}

func (f *fakeSessionRepo) Create(context.Context, string, int, int) (*domain.Session, error) {
	return nil, nil
}
func (f *fakeSessionRepo) FindByID(_ context.Context, _ string) (*domain.Session, error) {
	return f.session, f.err
}
func (f *fakeSessionRepo) ListByUser(context.Context, string, int, int) ([]*domain.Session, error) {
	return nil, nil
}
func (f *fakeSessionRepo) Close(context.Context, string) error { return nil }

func setupRouter(repo *fakeSessionRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	return r
}

func TestSessionWindow_OpenPasses(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	called := false
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		called = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestSessionWindow_Expired(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", w.Code)
	}
	var body map[string]map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"]["code"] != "session_expired" {
		t.Errorf("error code = %v, want session_expired", body["error"]["code"])
	}
}

func TestSessionWindow_Closed(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusClosed,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", w.Code)
	}
}

func TestSessionWindow_NotFound(t *testing.T) {
	repo := &fakeSessionRepo{err: domain.ErrNotFound}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSessionWindow_WrongOwnership(t *testing.T) {
	repo := &fakeSessionRepo{session: &domain.Session{
		ID: "sess-1", UserID: "other-user", Status: domain.SessionStatusOpen,
		ExpiresAt: time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	r := setupRouter(repo)
	r.POST("/sessions/:id/images", SessionWindow(repo), func(c *gin.Context) {
		t.Error("should not reach handler")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/sessions/sess-1/images", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (don't leak existence)", w.Code)
	}
}
