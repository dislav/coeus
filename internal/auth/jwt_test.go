package auth

import (
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
)

func TestIssueAndVerifyToken(t *testing.T) {
	cfg := config.JWTConfig{Secret: "test-secret", AccessTTL: time.Hour}
	mgr := NewJWTManager(cfg)

	token, err := mgr.Issue("user-123", "user", true, 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID = %q", claims.UserID)
	}
	if claims.Role != "user" {
		t.Errorf("Role = %q", claims.Role)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	cfg := config.JWTConfig{Secret: "test-secret", AccessTTL: -time.Hour}
	mgr := NewJWTManager(cfg)
	token, _ := mgr.Issue("user-123", "user", true, 0)
	_, err := mgr.Verify(token)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerifyWrongSecret(t *testing.T) {
	mgr1 := NewJWTManager(config.JWTConfig{Secret: "s1", AccessTTL: time.Hour})
	mgr2 := NewJWTManager(config.JWTConfig{Secret: "s2", AccessTTL: time.Hour})
	token, _ := mgr1.Issue("user-123", "user", true, 0)
	_, err := mgr2.Verify(token)
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestVerifyExpertRole(t *testing.T) {
	mgr := NewJWTManager(config.JWTConfig{Secret: "test-secret", AccessTTL: time.Hour})
	token, _ := mgr.Issue("expert-1", "expert", true, 0)
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Role != "expert" {
		t.Errorf("Role = %q, want 'expert'", claims.Role)
	}
}

func TestIssueAndVerifyCarriesActiveAndVersion(t *testing.T) {
	mgr := NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	token, err := mgr.Issue("u1", "user", true, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !claims.Active {
		t.Error("claims.Active = false, want true")
	}
	if claims.TokenVersion != 7 {
		t.Errorf("claims.TokenVersion = %d, want 7", claims.TokenVersion)
	}
}
