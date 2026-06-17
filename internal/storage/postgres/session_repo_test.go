package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestSessionRepo_Create(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "sess@example.com", "hash", "user")
	sess, err := sessRepo.Create(ctx, user.ID, 3600, 300)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("empty session ID")
	}
	if sess.DurationSeconds != 3600 {
		t.Errorf("duration = %d", sess.DurationSeconds)
	}
	if sess.Status != "open" {
		t.Errorf("status = %q", sess.Status)
	}
}

func TestSessionRepo_FindByIDNotFound(t *testing.T) {
	pool := setupTestDB(t)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	_, err := sessRepo.FindByID(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestSessionRepo_ListByUser(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "list@example.com", "hash", "user")
	sessRepo.Create(ctx, user.ID, 3600, 300)
	sessRepo.Create(ctx, user.ID, 1800, 120)

	list, err := sessRepo.ListByUser(ctx, user.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2", len(list))
	}
}

func TestSessionRepo_Close(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "close@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	sessRepo.Close(ctx, sess.ID)

	found, _ := sessRepo.FindByID(ctx, sess.ID)
	if found.Status != "closed" {
		t.Errorf("status = %q, want 'closed'", found.Status)
	}
}
