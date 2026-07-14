package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
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

// fakeUserRepo implements just storage.UserRepo for middleware tests.
type fakeUserRepo struct {
	user *storage.User
	err  error
}

func (f *fakeUserRepo) Create(context.Context, string, string, string) (*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindByEmail(context.Context, string) (*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindByID(_ context.Context, _ string) (*storage.User, error) {
	return f.user, f.err
}

func TestTokenValid(t *testing.T) {
	cases := []struct {
		name          string
		claimsActive  bool
		claimsVersion int64
		userActive    bool
		userVersion   int64
		want          bool
	}{
		{"match", true, 3, true, 3, true},
		{"active mismatch", true, 3, false, 3, false},
		{"version mismatch", true, 3, true, 4, false},
		{"both mismatch", false, 0, true, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &auth.Claims{Active: tc.claimsActive, TokenVersion: tc.claimsVersion}
			user := &storage.User{Active: tc.userActive, TokenVersion: tc.userVersion}
			if got := tokenValid(claims, user); got != tc.want {
				t.Errorf("tokenValid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoleGuard_Variadic(t *testing.T) {
	mk := func(role string, guard ...string) int {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Next() })
		r.GET("/x", RoleGuard(guard...), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		return w.Code
	}
	if c := mk("admin", "expert", "admin"); c != 200 {
		t.Errorf("admin via (expert,admin): got %d want 200", c)
	}
	if c := mk("expert", "expert"); c != 200 {
		t.Errorf("expert via (expert): got %d want 200", c)
	}
	if c := mk("user", "expert", "admin"); c != 403 {
		t.Errorf("user via (expert,admin): got %d want 403", c)
	}
	if c := mk("user"); c != 403 { // empty allowed list
		t.Errorf("no allowed roles: got %d want 403", c)
	}
}

func TestRoleGuard_MissingRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// No "role" set in context.
	r.GET("/x", RoleGuard("admin"), func(c *gin.Context) { t.Error("must not run") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != 403 {
		t.Errorf("missing role: got %d want 403", w.Code)
	}
}
