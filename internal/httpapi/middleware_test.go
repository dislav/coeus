package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
)

func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.GET("/p", func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("u1", "user")
	req := httptest.NewRequest("GET", "/p", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAuthMiddleware_NoToken(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.GET("/p", func(c *gin.Context) { c.Status(200) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/p", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRoleGuard_AllowsExpert(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.PATCH("/q/:id", RoleGuard("expert"), func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("e1", "expert")
	req := httptest.NewRequest("PATCH", "/q/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRoleGuard_BlocksUser(t *testing.T) {
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	r := setupTestRouter()
	r.Use(AuthMiddleware(mgr))
	r.PATCH("/q/:id", RoleGuard("expert"), func(c *gin.Context) { c.Status(200) })

	token, _ := mgr.Issue("u1", "user")
	req := httptest.NewRequest("PATCH", "/q/1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}
