package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type mockUserRepo struct {
	users map[string]*storage.User
}

func (m *mockUserRepo) Create(_ context.Context, email, hash, role string) (*storage.User, error) {
	if _, ok := m.users[email]; ok {
		return nil, fmt.Errorf("create: %w", domain.ErrDuplicate)
	}
	u := &storage.User{ID: uuid.NewString(), Email: email, PasswordHash: hash, Role: role, Active: true}
	m.users[email] = u
	return u, nil
}
func (m *mockUserRepo) FindByEmail(_ context.Context, email string) (*storage.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
	}
	return u, nil
}
func (m *mockUserRepo) FindByID(_ context.Context, id string) (*storage.User, error) {
	for _, u := range m.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
}

func newTestAuthHandler() (*AuthHandler, *mockUserRepo) {
	repo := &mockUserRepo{users: make(map[string]*storage.User)}
	mgr := auth.NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	return NewAuthHandler(repo, mgr), repo
}

func TestRegisterHandler_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)

	body, _ := json.Marshal(map[string]string{"email": "new@test.com", "password": "pass1234"})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["email"] != "new@test.com" {
		t.Errorf("email = %v", resp["email"])
	}
	if resp["role"] != "user" {
		t.Errorf("role = %v", resp["role"])
	}
}

func TestRegisterHandler_Duplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)

	body, _ := json.Marshal(map[string]string{"email": "dup@test.com", "password": "pass1234"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body)))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestLoginHandler_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)

	regBody, _ := json.Marshal(map[string]string{"email": "login@test.com", "password": "pass1234"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody)))

	loginBody, _ := json.Marshal(map[string]string{"email": "login@test.com", "password": "pass1234"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(loginBody)))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected non-empty token")
	}
}

func TestLoginHandler_WrongPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTestAuthHandler()
	r := gin.New()
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)

	regBody, _ := json.Marshal(map[string]string{"email": "wp@test.com", "password": "correct"})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody)))

	loginBody, _ := json.Marshal(map[string]string{"email": "wp@test.com", "password": "wrong"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(loginBody)))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
