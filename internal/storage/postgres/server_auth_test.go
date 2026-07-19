package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// bootTestServer builds a real httpapi.Server against the test pool with a
// known JWT secret. embedder is nil (not exercised by these routes).
func bootTestServer(t *testing.T, pool *pgxpool.Pool) (*httpapi.Server, *auth.JWTManager, *UserRepo) {
	t.Helper()
	jwt := auth.NewJWTManager(config.JWTConfig{Secret: "route-auth-secret", AccessTTL: time.Hour})
	userRepo := NewUserRepo(pool)
	srv := httpapi.NewServer(
		userRepo,
		NewSessionRepo(pool),
		NewImageRepo(pool),
		NewQuestionRepo(pool),
		NewJobQueue(pool),
		jwt, pool,
		config.UploadConfig{},
		nil, // embedder unused
		config.CORSConfig{AllowedOrigins: []string{"*"}},
	)
	return srv, jwt, userRepo
}

// tokenFor creates a user of `role` and returns a valid Bearer token for it.
func tokenFor(t *testing.T, repo *UserRepo, jwt *auth.JWTManager, email, role string) string {
	t.Helper()
	u, err := repo.Create(context.Background(), email, "hash", role)
	if err != nil {
		t.Fatalf("create %s %s: %v", role, email, err)
	}
	tok, err := jwt.Issue(u.ID, u.Role, u.Active, u.TokenVersion)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return "Bearer " + tok
}

func do(t *testing.T, h http.Handler, method, target, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRouteAuth_ProfileWorksForAllRoles(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	ctx := context.Background()

	for _, role := range []string{"user", "expert", "admin"} {
		email := role + "-profile@example.com"
		u, _ := users.Create(ctx, email, "hash", role)
		tok, _ := jwt.Issue(u.ID, u.Role, u.Active, u.TokenVersion)
		w := do(t, h, "GET", "/api/v1/profile", "Bearer "+tok)
		if w.Code != http.StatusOK {
			t.Errorf("%s /profile: got %d want 200 (body=%s)", role, w.Code, w.Body.String())
		}
	}
}

func TestRouteAuth_AdminRoutesRejectNonAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()

	userTok := tokenFor(t, users, jwt, "ra-user@example.com", "user")
	expertTok := tokenFor(t, users, jwt, "ra-expert@example.com", "expert")

	cases := []struct {
		name, method, target, tok string
	}{
		{"user GET /users", "GET", "/api/v1/users", userTok},
		{"expert GET /users", "GET", "/api/v1/users", expertTok},
		{"user PUT /users/:id", "PUT", "/api/v1/users/00000000-0000-0000-0000-000000000000", userTok},
		{"user DELETE /users/:id", "DELETE", "/api/v1/users/00000000-0000-0000-0000-000000000000", userTok},
		{"user POST reset-password", "POST", "/api/v1/users/00000000-0000-0000-0000-000000000000/reset-password", userTok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.target, tc.tok)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: got %d want 403", tc.method, tc.target, w.Code)
			}
		})
	}
}

func TestRouteAuth_AdminRoutesAcceptAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	adminTok := tokenFor(t, users, jwt, "ra-admin@example.com", "admin")

	// GET /users must return 200 (not 403) for admin.
	w := do(t, h, "GET", "/api/v1/users", adminTok)
	if w.Code != http.StatusOK {
		t.Errorf("admin GET /users: got %d want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestRouteAuth_QuestionDeleteRejectsUser(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	userTok := tokenFor(t, users, jwt, "qd-user@example.com", "user")

	w := do(t, h, "DELETE", "/api/v1/questions/00000000-0000-0000-0000-000000000000", userTok)
	if w.Code != http.StatusForbidden {
		t.Errorf("user DELETE /questions/:id: got %d want 403", w.Code)
	}
}

func TestRouteAuth_WidenedExpertRouteAcceptsAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	adminTok := tokenFor(t, users, jwt, "we-admin@example.com", "admin")

	// POST /questions is gated by RoleGuard("expert","admin"). Admin must NOT
	// get 403 (a 400/422 from a missing body is fine — it proves the guard let
	// the request through).
	w := do(t, h, "POST", "/api/v1/questions", adminTok)
	if w.Code == http.StatusForbidden {
		t.Fatalf("admin POST /questions: got 403, guard should have admitted admin (body=%s)", w.Body.String())
	}
}

func TestRouteAuth_ExpiredOrStaleTokenRejected(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()

	u, _ := users.Create(context.Background(), "stale@example.com", "hash", "user")
	// Issue with active=true, then deactivate via Update (bumps token_version).
	admin, _ := users.Create(context.Background(), "stale-admin@example.com", "hash", "admin")
	users.Update(context.Background(), u.ID, storage.UserUpdate{Email: "stale@example.com", Role: "user", Active: false}, admin.ID)
	// Token still claims active=true, version=0; live row is active=false, version=1.
	tok, _ := jwt.Issue(u.ID, u.Role, true, 0)

	w := do(t, h, "GET", "/api/v1/profile", "Bearer "+tok)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("stale token: got %d want 401 (stateless invalidation)", w.Code)
	}
}
