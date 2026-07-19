package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// ctrlUserRepo is a controllable UserRepo for handler unit tests.
type ctrlUserRepo struct {
	list   func(ctx context.Context, f storage.UserFilter, limit, off int) ([]*storage.User, error)
	update func(ctx context.Context, id string, upd storage.UserUpdate, caller string) (*storage.User, error)
	del    func(ctx context.Context, id, caller string) error
	reset  func(ctx context.Context, id string) (string, error)
}

func (c *ctrlUserRepo) Create(context.Context, string, string, string) (*storage.User, error) { return nil, nil }
func (c *ctrlUserRepo) FindByEmail(context.Context, string) (*storage.User, error)             { return nil, nil }
func (c *ctrlUserRepo) FindByID(context.Context, string) (*storage.User, error)                { return nil, nil }
func (c *ctrlUserRepo) List(ctx context.Context, f storage.UserFilter, l, o int) ([]*storage.User, error) {
	if c.list != nil {
		return c.list(ctx, f, l, o)
	}
	return nil, nil
}
func (c *ctrlUserRepo) Update(ctx context.Context, id string, upd storage.UserUpdate, caller string) (*storage.User, error) {
	return c.update(ctx, id, upd, caller)
}
func (c *ctrlUserRepo) Delete(ctx context.Context, id, caller string) error { return c.del(ctx, id, caller) }
func (c *ctrlUserRepo) ResetPassword(ctx context.Context, id string) (string, error) {
	return c.reset(ctx, id)
}

func setUserRouter(role, userID string, h *UserHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	return r
}

func TestUserHandler_List_MapsToResponse(t *testing.T) {
	repo := &ctrlUserRepo{list: func(context.Context, storage.UserFilter, int, int) ([]*storage.User, error) {
		return []*storage.User{{ID: "u1", Email: "a@x.com", Role: "user", Active: true, CreatedAt: "2026-07-15T00:00:00Z"}}, nil
	}}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.GET("/users", h.List)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/users?role=admin&active=true&q=foo&page=2&per_page=5", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp dto.UserListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].ID != "u1" || !resp.Data[0].Active {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
	if resp.Page != 2 || resp.PerPage != 5 {
		t.Errorf("paging = page %d per %d", resp.Page, resp.PerPage)
	}
}

func TestUserHandler_Update_Validation400(t *testing.T) {
	h := NewUserHandler(&ctrlUserRepo{update: func(context.Context, string, storage.UserUpdate, string) (*storage.User, error) {
		t.Error("update must not run on bad body")
		return nil, nil
	}})
	r := setUserRouter("admin", "caller", h)
	r.PUT("/users/:id", h.Update)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/users/u1", bytes.NewReader([]byte(`{"email":"bad","role":"x"}`))))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUserHandler_Update_SelfForbidden409(t *testing.T) {
	repo := &ctrlUserRepo{update: func(_ context.Context, _ string, _ storage.UserUpdate, _ string) (*storage.User, error) {
		return nil, domain.ErrSelfForbidden
	}}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.PUT("/users/:id", h.Update)

	body, _ := json.Marshal(map[string]any{"email": "a@x.com", "role": "user", "active": true})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/users/u1", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	var env map[string]map[string]string
	json.Unmarshal(w.Body.Bytes(), &env)
	if env["error"]["code"] != "self_forbidden" {
		t.Errorf("code = %v, want self_forbidden", env["error"]["code"])
	}
}

func TestUserHandler_Delete_204(t *testing.T) {
	repo := &ctrlUserRepo{del: func(context.Context, string, string) error { return nil }}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.DELETE("/users/:id", h.Delete)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/users/u1", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

func TestUserHandler_ResetPassword_ReturnsPlaintext(t *testing.T) {
	repo := &ctrlUserRepo{reset: func(context.Context, string) (string, error) { return "abc1234567890qrstUV", nil }}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.POST("/users/:id/reset-password", h.ResetPassword)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/users/u1/reset-password", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["password"] != "abc1234567890qrstUV" {
		t.Errorf("password = %q", resp["password"])
	}
}
