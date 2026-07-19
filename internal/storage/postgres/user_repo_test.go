package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
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

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestUserRepo_List(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "a@example.com", "h", "user")
	repo.Create(ctx, "b@example.com", "h", "expert")
	repo.Create(ctx, "c@example.com", "h", "admin")
	repo.Create(ctx, "admin2@example.com", "h", "admin")

	// All, ordered by created_at DESC.
	all, err := repo.List(ctx, storage.UserFilter{}, 100, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4", len(all))
	}

	// Filter by role.
	admins, _ := repo.List(ctx, storage.UserFilter{Role: strPtr("admin")}, 100, 0)
	if len(admins) != 2 {
		t.Errorf("admins = %d, want 2", len(admins))
	}

	// Filter by active.
	inactive, _ := repo.List(ctx, storage.UserFilter{Active: boolPtr(false)}, 100, 0)
	if len(inactive) != 0 {
		t.Errorf("inactive = %d, want 0", len(inactive))
	}

	// Query substring (ILIKE).
	got, _ := repo.List(ctx, storage.UserFilter{Query: strPtr("ADMIN2")}, 100, 0)
	if len(got) != 1 || got[0].Email != "admin2@example.com" {
		t.Errorf("query ADMIN2: got %+v", got)
	}

	// Pagination.
	page1, _ := repo.List(ctx, storage.UserFilter{}, 2, 0)
	page2, _ := repo.List(ctx, storage.UserFilter{}, 2, 2)
	if len(page1) != 2 || len(page2) != 2 {
		t.Errorf("pagination: page1=%d page2=%d", len(page1), len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Error("pagination returned same row on both pages")
	}
}

func TestUserRepo_NewColumnsPopulated(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, "cols@example.com", "hash", "user")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Active {
		t.Errorf("created.Active = false, want true")
	}
	if created.TokenVersion != 0 {
		t.Errorf("created.TokenVersion = %d, want 0", created.TokenVersion)
	}

	byEmail, err := repo.FindByEmail(ctx, "cols@example.com")
	if err != nil {
		t.Fatalf("find by email: %v", err)
	}
	if !byEmail.Active || byEmail.TokenVersion != 0 {
		t.Errorf("by email: Active=%v TokenVersion=%d", byEmail.Active, byEmail.TokenVersion)
	}

	byID, err := repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if !byID.Active || byID.TokenVersion != 0 {
		t.Errorf("by id: Active=%v TokenVersion=%d", byID.Active, byID.TokenVersion)
	}
}

func TestUserRepo_Update_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "up@example.com", "h", "user")
	// Another admin performs the update.
	caller, _ := repo.Create(ctx, "caller@example.com", "h", "admin")

	updated, err := repo.Update(ctx, target.ID, storage.UserUpdate{
		Email: "changed@example.com", Role: "expert", Active: true,
	}, caller.ID)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Email != "changed@example.com" || updated.Role != "expert" {
		t.Errorf("updated = %+v", updated)
	}
	// role changed user->expert => token_version bumped from 0 to 1.
	if updated.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1 (role changed)", updated.TokenVersion)
	}
}

func TestUserRepo_Update_NoBumpOnSameRoleActive(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "nb@example.com", "h", "expert")
	// First bump it to 1 via a role change.
	repo.Update(ctx, target.ID, storage.UserUpdate{Email: "nb@example.com", Role: "user", Active: true}, "someone-else")
	// Now send the SAME role/active back (only email differs) => no bump.
	updated, err := repo.Update(ctx, target.ID, storage.UserUpdate{Email: "nb2@example.com", Role: "user", Active: true}, "someone-else")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1 (no bump on pure email change)", updated.TokenVersion)
	}
	if updated.Email != "nb2@example.com" {
		t.Errorf("email not updated: %q", updated.Email)
	}
}

func TestUserRepo_Update_SelfForbidden(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "self@example.com", "h", "admin")
	_, err := repo.Update(ctx, self.ID, storage.UserUpdate{
		Email: "self@example.com", Role: "user", Active: true,
	}, self.ID)
	if !errors.Is(err, domain.ErrSelfForbidden) {
		t.Errorf("err = %v, want ErrSelfForbidden", err)
	}
}

func TestUserRepo_Update_SelfEmailAllowed(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "selfmail@example.com", "h", "admin")
	// Editing own email only (role/active unchanged) is allowed.
	updated, err := repo.Update(ctx, self.ID, storage.UserUpdate{
		Email: "selfmail2@example.com", Role: "admin", Active: true,
	}, self.ID)
	if err != nil {
		t.Fatalf("self email edit should be allowed: %v", err)
	}
	if updated.TokenVersion != 0 {
		t.Errorf("TokenVersion = %d, want 0 (no bump)", updated.TokenVersion)
	}
}

func TestUserRepo_Update_LastAdmin(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	only, _ := repo.Create(ctx, "only-admin@example.com", "h", "admin")
	caller, _ := repo.Create(ctx, "caller2@example.com", "h", "user") // non-admin caller: exactly one admin exists
	_, err := repo.Update(ctx, only.ID, storage.UserUpdate{
		Email: "only-admin@example.com", Role: "user", Active: true,
	}, caller.ID)
	if !errors.Is(err, domain.ErrLastAdmin) {
		t.Errorf("err = %v, want ErrLastAdmin", err)
	}
}

func TestUserRepo_Update_DuplicateEmail(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "taken@example.com", "h", "user")
	target, _ := repo.Create(ctx, "orig@example.com", "h", "user")
	caller, _ := repo.Create(ctx, "caller3@example.com", "h", "admin")
	_, err := repo.Update(ctx, target.ID, storage.UserUpdate{
		Email: "taken@example.com", Role: "user", Active: true,
	}, caller.ID)
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
}

func TestUserRepo_Update_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.Update(ctx, "00000000-0000-0000-0000-000000000000", storage.UserUpdate{
		Email: "x@example.com", Role: "user", Active: true,
	}, "caller")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUserRepo_Delete_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "del@example.com", "h", "user")
	caller, _ := repo.Create(ctx, "del-caller@example.com", "h", "admin")

	if err := repo.Delete(ctx, target.ID, caller.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.FindByID(ctx, target.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("after delete, FindByID err = %v, want ErrNotFound", err)
	}
}

func TestUserRepo_Delete_SelfForbidden(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "del-self@example.com", "h", "admin")
	err := repo.Delete(ctx, self.ID, self.ID)
	if !errors.Is(err, domain.ErrSelfForbidden) {
		t.Errorf("err = %v, want ErrSelfForbidden", err)
	}
}

func TestUserRepo_Delete_LastAdmin(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	only, _ := repo.Create(ctx, "del-only@example.com", "h", "admin")
	caller, _ := repo.Create(ctx, "del-caller2@example.com", "h", "user") // non-admin caller: exactly one admin exists
	err := repo.Delete(ctx, only.ID, caller.ID)
	if !errors.Is(err, domain.ErrLastAdmin) {
		t.Errorf("err = %v, want ErrLastAdmin", err)
	}
}

func TestUserRepo_Delete_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	err := repo.Delete(ctx, "00000000-0000-0000-0000-000000000000", "caller")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUserRepo_ResetPassword(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "rp@example.com", "old-hash", "user")

	plaintext, err := repo.ResetPassword(ctx, target.ID)
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if len(plaintext) != 20 {
		t.Errorf("len(plaintext) = %d, want 20", len(plaintext))
	}

	// token_version bumped from 0 to 1.
	after, _ := repo.FindByID(ctx, target.ID)
	if after.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1", after.TokenVersion)
	}
	// active untouched.
	if !after.Active {
		t.Errorf("Active = false, want true (reset must not deactivate)")
	}
	// A NEW bcrypt hash replaced the old one and verifies the plaintext.
	if after.PasswordHash == "old-hash" {
		t.Error("password_hash was not replaced")
	}
	if !auth.VerifyPassword(after.PasswordHash, plaintext) {
		t.Error("new hash does not verify the generated plaintext")
	}
}

func TestUserRepo_ResetPassword_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.ResetPassword(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
