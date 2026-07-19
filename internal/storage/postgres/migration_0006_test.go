package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// TestMigration0006_AdminRoleAccepted asserts the widened CHECK allows 'admin'.
func TestMigration0006_AdminRoleAccepted(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	if _, err := repo.Create(ctx, "admin@example.com", "hash", "admin"); err != nil {
		t.Fatalf("create admin user: %v", err)
	}
}

// TestMigration0006_VerifiedByOnDeleteSetNull asserts that deleting a user who
// verified a question leaves the question intact with verified_by IS NULL.
func TestMigration0006_VerifiedByOnDeleteSetNull(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	users := NewUserRepo(pool)
	questions := NewQuestionRepo(pool)

	verifier, err := users.Create(ctx, "verifier@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	qID, err := questions.Create(ctx, &domain.Question{
		Text: "Q", TextHash: "vby-hash", TextNorm: "vby",
		Status: domain.QuestionStatusVerified, Choices: []string{"a"},
		ChoiceLabeling: "letter", VerifiedAt: &now, VerifiedBy: &verifier.ID,
	})
	if err != nil {
		t.Fatalf("create verified question: %v", err)
	}

	// Delete the verifier directly (UserRepo.Delete lands in Task 8).
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, verifier.ID); err != nil {
		t.Fatalf("delete verifier: %v", err)
	}

	// Question must survive, with verified_by now NULL.
	q, err := questions.FindByID(ctx, qID)
	if err != nil {
		t.Fatalf("question should survive user deletion, got: %v", err)
	}
	if q.VerifiedBy != nil {
		t.Errorf("VerifiedBy = %v, want nil (ON DELETE SET NULL)", *q.VerifiedBy)
	}
}
