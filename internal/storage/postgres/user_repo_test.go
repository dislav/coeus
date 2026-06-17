package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestUserRepo_CreateAndFindByEmail(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	user, err := repo.Create(ctx, "test@example.com", "hashed-pwd", "user")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID == "" {
		t.Fatal("expected non-empty user ID")
	}
	if user.Email != "test@example.com" {
		t.Errorf("email = %q", user.Email)
	}

	found, err := repo.FindByEmail(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("found ID = %q, want %q", found.ID, user.ID)
	}
	if found.PasswordHash != "hashed-pwd" {
		t.Errorf("password_hash = %q", found.PasswordHash)
	}
}

func TestUserRepo_CreateDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "dup@example.com", "hash", "user")
	_, err := repo.Create(ctx, "dup@example.com", "hash2", "user")
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got: %v", err)
	}
}

func TestUserRepo_FindByEmailNotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.FindByEmail(ctx, "nonexistent@example.com")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUserRepo_FindByID(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	created, _ := repo.Create(ctx, "byid@example.com", "hash", "expert")
	found, err := repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Role != "expert" {
		t.Errorf("role = %q", found.Role)
	}
}
