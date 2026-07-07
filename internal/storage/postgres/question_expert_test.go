package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestUpdateByExpert_CleansImageBytesWhenLastResolved(t *testing.T) {
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
	if err := questions.UpdateByExpert(ctx, q1, domain.QuestionUpdate{
		Status: domain.QuestionStatusVerified, Answers: []string{"a"}, Choices: []string{"a"}, Confidence: 1.0,
	}, user.ID); err != nil {
		t.Fatalf("update q1: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original == nil {
		t.Fatal("bytes cleaned too early: q2 still moderation")
	}

	// Resolve q2 -> bytes MUST now be NULL (no unresolved siblings).
	if err := questions.UpdateByExpert(ctx, q2, domain.QuestionUpdate{
		Status: domain.QuestionStatusVerified, Answers: []string{"b"}, Choices: []string{"b"}, Confidence: 1.0,
	}, user.ID); err != nil {
		t.Fatalf("update q2: %v", err)
	}
	img, err = imgs.FindByID(ctx, imgID)
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
	if err := questions.UpdateByExpert(ctx, qMod, domain.QuestionUpdate{
		Status: domain.QuestionStatusVerified, Answers: []string{"a"}, Choices: []string{"a"}, Confidence: 1.0,
	}, user.ID); err != nil {
		t.Fatalf("update moderation question: %v", err)
	}
	img, err := imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original == nil {
		t.Fatal("bytes cleaned too early: error sibling still unresolved")
	}

	// Step 2: resolve the 'error' question -> bytes MUST now be NULL.
	if err := questions.UpdateByExpert(ctx, qErr, domain.QuestionUpdate{
		Status: domain.QuestionStatusVerified, Answers: []string{"a"}, Choices: []string{"a"}, Confidence: 1.0,
	}, user.ID); err != nil {
		t.Fatalf("update error question: %v", err)
	}
	img, err = imgs.FindByID(ctx, imgID)
	if err != nil {
		t.Fatalf("find image: %v", err)
	}
	if img.Original != nil || img.Enhanced != nil {
		t.Fatalf("expected cleaned bytes, got original=%v enhanced=%v", img.Original, img.Enhanced)
	}
}

func TestFindExpertByID_ReturnsImageLinkAndReportFlag(t *testing.T) {
	ctx := context.Background()
	pool := setupTestDB(t)

	imgs := NewImageRepo(pool)
	questions := NewQuestionRepo(pool)
	sessions := NewSessionRepo(pool)
	users := NewUserRepo(pool)

	user, err := users.Create(ctx, "expert-view@example.com", "hash", "user")
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
	if err := imgs.UpdateVerificationReport(ctx, imgID, []byte(`{"flag":true}`)); err != nil {
		t.Fatalf("update verification report: %v", err)
	}

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

// TestListForModerationExpert_SearchMatchesAllFields verifies the universal
// `search` parameter is a case-insensitive substring matched against the
// question text, any choice, any answer, and any tag name — and that a term
// matching nothing returns an empty result set.
func TestListForModerationExpert_SearchMatchesAllFields(t *testing.T) {
	ctx := context.Background()
	pool := setupTestDB(t)
	questions := NewQuestionRepo(pool)

	// Each question is discoverable through exactly one field.
	mk := func(text, hash, norm string, choices, answers, tags []string) string {
		id, err := questions.Create(ctx, &domain.Question{
			Text:           text,
			TextHash:       hash,
			TextNorm:       norm,
			Status:         domain.QuestionStatusModeration,
			Choices:        choices,
			Answers:        answers,
			Tags:           tags,
			ChoiceLabeling: "letter",
		})
		if err != nil {
			t.Fatalf("create question %q: %v", text, err)
		}
		return id
	}
	qByQuestion := mk("What is the capital of France", "h-q", "nq", []string{"x"}, nil, nil)
	qByChoice := mk("General knowledge", "h-c", "nc", []string{"Photosynthesis"}, nil, nil)
	qByAnswer := mk("Cell biology", "h-a", "na", []string{"y"}, []string{"Mitochondria"}, nil)
	qByTag := mk("European history", "h-t", "nt", []string{"z"}, nil, []string{"renaissance"})
	mk("Pure math", "h-m", "nm", []string{"1"}, nil, nil) // control: matches nothing

	cases := []struct {
		name   string
		search string
		wantID string
	}{
		{"question text substring", "CAPITAL of France", qByQuestion},
		{"choice substring", "photosyn", qByChoice},
		{"answer substring", "MITOCHOND", qByAnswer},
		{"tag substring", "NAISSANCE", qByTag},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := questions.ListForModerationExpert(ctx, "", tc.search, 50, 0)
			if err != nil {
				t.Fatalf("search %q: %v", tc.search, err)
			}
			var found bool
			for _, ev := range got {
				if ev.ID == tc.wantID {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("search %q: want question %s in results, got %d rows",
					tc.search, tc.wantID, len(got))
			}
		})
	}

	// A term absent from every field returns nothing.
	t.Run("no match", func(t *testing.T) {
		got, err := questions.ListForModerationExpert(ctx, "", "zzz-no-such-term", 50, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("want 0 results for unmatched term, got %d", len(got))
		}
	})
}
