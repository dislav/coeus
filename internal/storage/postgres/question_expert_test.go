package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestUpdateByExpert_CleansImageBytesWhenLastResolved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)
	users := NewUserRepo(pool)

	// One user + one session + one image.
	user, err := users.Create(ctx, "clean-last@example.com", "hash", "user")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := sessions.Create(ctx, user.ID, 3600, 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	imgID, err := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}

	// Two questions linked to the same image, both moderation. Unique, non-empty
	// TextHash/TextNorm are required to satisfy the question_hash UNIQUE constraint.
	q1, err := questions.Create(ctx, &domain.Question{Text: "Q1", TextHash: "q1-hash", TextNorm: "q1", Status: domain.QuestionStatusModeration, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	if err != nil {
		t.Fatalf("create q1: %v", err)
	}
	q2, err := questions.Create(ctx, &domain.Question{Text: "Q2", TextHash: "q2-hash", TextNorm: "q2", Status: domain.QuestionStatusModeration, Choices: []string{"b"}, ChoiceLabeling: "letter"})
	if err != nil {
		t.Fatalf("create q2: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, q1, 1, 0.9); err != nil {
		t.Fatalf("link q1: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, q2, 2, 0.9); err != nil {
		t.Fatalf("link q2: %v", err)
	}

	// Resolve q1 -> bytes MUST remain (q2 still moderation).
	if err := questions.UpdateByExpert(ctx, q1, []string{"a"}, []string{"a"}, "", 1.0, nil, user.ID); err != nil {
		t.Fatalf("update q1: %v", err)
	}
	if img, _ := imgs.FindByID(ctx, imgID); img.Original == nil {
		t.Fatal("bytes cleaned too early: q2 still moderation")
	}

	// Resolve q2 -> bytes MUST now be NULL (no unresolved siblings).
	if err := questions.UpdateByExpert(ctx, q2, []string{"b"}, []string{"b"}, "", 1.0, nil, user.ID); err != nil {
		t.Fatalf("update q2: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original != nil || img.Enhanced != nil {
		t.Fatalf("expected cleaned bytes, got original=%v enhanced=%v", img.Original, img.Enhanced)
	}
	// Metadata retained.
	if img.Mime != "image/png" {
		t.Errorf("mime should be retained, got %q", img.Mime)
	}
}

func TestUpdateByExpert_CleansImageBytesWhenErrorSiblingResolved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)
	users := NewUserRepo(pool)

	user, err := users.Create(ctx, "clean-error@example.com", "hash", "user")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := sessions.Create(ctx, user.ID, 3600, 0)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	imgID, err := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}

	// Two questions on the same image: one 'moderation', one 'error'. Both count
	// as "unresolved" for the cleanup decision (spec §3.5: status IN moderation|error).
	qMod, err := questions.Create(ctx, &domain.Question{Text: "M", TextHash: "mod-hash", TextNorm: "mod", Status: domain.QuestionStatusModeration, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	if err != nil {
		t.Fatalf("create moderation question: %v", err)
	}
	qErr, err := questions.Create(ctx, &domain.Question{Text: "E", TextHash: "err-hash", TextNorm: "err", Status: domain.QuestionStatusError, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	if err != nil {
		t.Fatalf("create error question: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, qMod, 1, 0.9); err != nil {
		t.Fatalf("link moderation question: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, qErr, 2, 0.9); err != nil {
		t.Fatalf("link error question: %v", err)
	}

	// Step 1: resolve the 'moderation' question -> bytes MUST remain
	//         (the 'error' sibling is still unresolved).
	if err := questions.UpdateByExpert(ctx, qMod, []string{"a"}, []string{"a"}, "", 1.0, nil, user.ID); err != nil {
		t.Fatalf("update moderation question: %v", err)
	}
	if img, _ := imgs.FindByID(ctx, imgID); img.Original == nil {
		t.Fatal("bytes cleaned too early: error sibling still unresolved")
	}

	// Step 2: resolve the 'error' question -> bytes MUST now be NULL.
	if err := questions.UpdateByExpert(ctx, qErr, []string{"a"}, []string{"a"}, "", 1.0, nil, user.ID); err != nil {
		t.Fatalf("update error question: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original != nil || img.Enhanced != nil {
		t.Fatalf("expected cleaned bytes, got original=%v enhanced=%v", img.Original, img.Enhanced)
	}
}

func TestFindExpertByID_ReturnsImageLinkAndReportFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (needs Docker)")
	}
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)
	users := NewUserRepo(pool)

	user, _ := users.Create(ctx, "expert-view@example.com", "hash", "user")
	sess, _ := sessions.Create(ctx, user.ID, 3600, 0)
	imgID, _ := imgs.Create(ctx, sess.ID, []byte("orig"), "image/png", 10, 10)
	_ = imgs.UpdateVerificationReport(ctx, imgID, []byte(`{"flag":true}`))

	qID, err := questions.Create(ctx, &domain.Question{Text: "Q", TextHash: "qe-hash", TextNorm: "qe", Status: domain.QuestionStatusModeration, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	if err := questions.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9); err != nil {
		t.Fatalf("link question: %v", err)
	}

	ev, err := questions.FindExpertByID(ctx, qID)
	if err != nil {
		t.Fatalf("find expert: %v", err)
	}
	if ev.ImageID != imgID {
		t.Errorf("image_id: got %q want %q", ev.ImageID, imgID)
	}
	if !ev.HasVerificationReport {
		t.Errorf("has_verification_report: want true")
	}
}
