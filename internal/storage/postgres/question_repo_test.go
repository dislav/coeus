package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestQuestionRepo_CreateAndFindExact(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Text: "What is the capital of France?",
		TextNorm: "what is the capital of france", TextHash: "abc123hash",
		Choices: []string{"Paris", "London", "Berlin"}, Answers: []string{"Paris"},
		ChoiceLabeling: "letter", Confidence: 0.95,
		Explanation: "Paris is the capital.", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated", "geography"},
	}
	id, err := repo.Create(ctx, q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty question ID")
	}

	found, err := repo.FindExact(ctx, "abc123hash")
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}
	if found.ID != id {
		t.Errorf("found ID = %q, want %q", found.ID, id)
	}
	if len(found.Answers) != 1 || found.Answers[0] != "Paris" {
		t.Errorf("answers = %v, want [Paris]", found.Answers)
	}
}

func TestQuestionRepo_FindExactNoMatch(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	found, err := repo.FindExact(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("FindExact err: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil, got %v", found)
	}
}

func TestQuestionRepo_FindSemanticAboveThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h1",
		Choices: []string{}, Answers: []string{}, Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	repo.Create(ctx, q)

	search := make([]float32, 1536)
	search[0] = 1.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found == nil {
		t.Fatal("expected semantic match, got nil")
	}
}

func TestQuestionRepo_FindSemanticBelowThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h1",
		Choices: []string{}, Answers: []string{}, Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	repo.Create(ctx, q)

	search := make([]float32, 1536)
	search[1] = 1.0 // orthogonal — cosine similarity = 0.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found != nil {
		t.Error("expected no match, got result")
	}
}

func TestQuestionRepo_UpdateFromVerification(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h",
		Choices: []string{"a"}, Answers: []string{"a"}, Confidence: 0.90,
		Explanation: "original", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	id, _ := repo.Create(ctx, q)

	repo.UpdateFromVerification(ctx, id, 0.75, "original [VERIFICATION FLAG]")

	found, _ := repo.FindByID(ctx, id)
	if found.Confidence != 0.75 {
		t.Errorf("confidence = %v, want 0.75", found.Confidence)
	}
}

func TestQuestionRepo_CountUnresolvedForImage(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	qRepo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "count@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	statuses := []string{domain.QuestionStatusModeration, domain.QuestionStatusModeration, domain.QuestionStatusVerified}
	for i, status := range statuses {
		q := &domain.Question{
			Number: i + 1, Text: "q", TextNorm: "q", TextHash: "hash" + string(rune('a'+i)),
			Choices: []string{}, Answers: []string{}, Confidence: 0.9,
			Embedding: make([]float32, 1536), Status: status, Tags: []string{"ai-generated"},
		}
		qID, _ := qRepo.Create(ctx, q)
		qRepo.LinkToSession(ctx, sess.ID, imgID, qID, i+1, 0.9)
	}

	count, err := qRepo.CountUnresolvedForImage(ctx, imgID)
	if err != nil {
		t.Fatalf("CountUnresolvedForImage: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}
