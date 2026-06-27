package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestQuestionPatch_RoleGuardRejectsUser verifies the PATCH route is gated by
// RoleGuard("expert"): a user-role caller gets 403 at the middleware layer
// before the handler runs. Mirrors the route wiring in registerRoutes.
func TestQuestionPatch_RoleGuardRejectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Simulate AuthMiddleware having authenticated a "user"-role caller.
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("user_id", "u1"); c.Next() })
	r.PATCH("/api/v1/questions/:id", RoleGuard("expert"), func(c *gin.Context) {
		t.Error("handler must not run for user role")
	})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/questions/q1", strings.NewReader(`{"status":"verified"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("user patch: got %d want 403", w.Code)
	}
}

// TestQuestionPost_RoleGuardRejectsUser verifies the POST route is gated by
// RoleGuard("expert"): a user-role caller gets 403 at the middleware layer
// before the handler runs. Mirrors the route wiring in registerRoutes.
func TestQuestionPost_RoleGuardRejectsUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "user"); c.Set("user_id", "u1"); c.Next() })
	r.POST("/api/v1/questions", RoleGuard("expert"), func(c *gin.Context) {
		t.Error("handler must not run for user role")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/questions", strings.NewReader(`{"question":"q","choices":["a","b"],"answers":["a"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("user post: got %d want 403", w.Code)
	}
}
