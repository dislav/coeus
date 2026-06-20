package postgres

import (
	"context"
	"testing"
)

func TestImageRepo_CreateAndFind(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "img@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	imgID, err := imgRepo.Create(ctx, sess.ID, []byte("fake-jpeg"), "image/jpeg", 800, 600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if imgID == "" {
		t.Fatal("empty image ID")
	}

	img, err := imgRepo.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if img.Mime != "image/jpeg" {
		t.Errorf("mime = %q", img.Mime)
	}
	if string(img.Original) != "fake-jpeg" {
		t.Error("original bytes mismatch")
	}
}

func TestImageRepo_UpdateEnhanced(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "enh@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	imgRepo.UpdateEnhanced(ctx, imgID, []byte("enhanced"))

	img, _ := imgRepo.FindByID(ctx, imgID)
	if string(img.Enhanced) != "enhanced" {
		t.Errorf("enhanced = %q", string(img.Enhanced))
	}
}

func TestImageRepo_CleanBytes(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "clean@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)
	imgRepo.UpdateEnhanced(ctx, imgID, []byte("enhanced"))

	imgRepo.CleanBytes(ctx, imgID)

	img, _ := imgRepo.FindByID(ctx, imgID)
	if img.Original != nil {
		t.Error("original should be nil after cleanup")
	}
	if img.Enhanced != nil {
		t.Error("enhanced should be nil after cleanup")
	}
	if img.Mime != "image/jpeg" {
		t.Error("metadata should remain after cleanup")
	}
}

func TestImageRepo_CountBySession(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "count@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	imgRepo.Create(ctx, sess.ID, []byte("a"), "image/jpeg", 1, 1)
	imgRepo.Create(ctx, sess.ID, []byte("b"), "image/jpeg", 1, 1)
	imgRepo.Create(ctx, sess.ID, []byte("c"), "image/jpeg", 1, 1)

	count, err := imgRepo.CountBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("CountBySession: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}
