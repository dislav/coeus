package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// importQuestion builds a verified question as the importer would (spec §5.6).
func importQuestion(text string, choices, answers []string, tags []string, embedding []float32, verifiedBy string) *domain.Question {
	norm := domain.NormalizeQuestion(text)
	now := time.Now().UTC().Format(time.RFC3339)
	typ := domain.InferQuestionType(choices)
	return &domain.Question{
		Number: 0, Text: text, TextNorm: norm, TextHash: domain.HashQuestion(norm),
		Choices: choices, Answers: answers,
		ChoiceLabeling: domain.ChoiceLabelingLetter, Type: typ,
		Confidence: 0.99, Explanation: "imported", Embedding: embedding,
		Status: domain.QuestionStatusVerified, VerifiedAt: &now, VerifiedBy: &verifiedBy,
		Tags: tags,
	}
}

func TestQuestionRepo_UpsertFromImport_InsertThenUpdate(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	uploader, err := userRepo.Create(ctx, "importer@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	q1 := importQuestion("What is 2+2?", []string{"3", "4"}, []string{"4"}, []string{"arith", "import"}, nil, uploader.ID)
	created, err := repo.UpsertFromImport(ctx, q1)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !created {
		t.Error("first upsert created = false, want true")
	}

	stored, err := repo.FindExact(ctx, q1.TextHash)
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}

	// Same hash, new content — the file wins (spec §7).
	q2 := importQuestion("What is 2+2?", []string{"3", "4", "5"}, []string{"4"}, []string{"math", "import"}, nil, uploader.ID)
	q2.Explanation = "updated explanation"
	created, err = repo.UpsertFromImport(ctx, q2)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Error("second upsert created = true, want false (hash conflict)")
	}

	after, err := repo.FindExact(ctx, q1.TextHash)
	if err != nil {
		t.Fatalf("FindExact after update: %v", err)
	}
	if after.ID != stored.ID {
		t.Errorf("ID changed across upsert: %q -> %q (must be update-in-place)", stored.ID, after.ID)
	}
	if len(after.Choices) != 3 || after.Choices[2] != "5" {
		t.Errorf("choices not replaced: %v", after.Choices)
	}
	if after.Explanation != "updated explanation" {
		t.Errorf("explanation = %q, want replaced", after.Explanation)
	}
	if after.Status != domain.QuestionStatusVerified {
		t.Errorf("status = %q, want verified", after.Status)
	}
	if after.VerifiedAt == nil || *after.VerifiedAt == "" {
		t.Error("verified_at not set on upsert")
	}
	if after.VerifiedBy == nil || *after.VerifiedBy != uploader.ID {
		t.Errorf("verified_by = %v, want %q", after.VerifiedBy, uploader.ID)
	}

	// Tags fully replaced by the file's set.
	ev, err := repo.FindExpertByID(ctx, stored.ID)
	if err != nil {
		t.Fatalf("FindExpertByID: %v", err)
	}
	if len(ev.Tags) != 2 {
		t.Errorf("tags = %v, want exactly [math import] (replaced, not merged)", ev.Tags)
	}
}

func TestQuestionRepo_UpsertFromImport_EmbeddingCoalesce(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	uploader, err := userRepo.Create(ctx, "embed@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	emb := make([]float32, 1536)
	emb[0] = 1.0

	var hasEmbedding bool
	row := func(id string) {
		if err := pool.QueryRow(ctx, `SELECT embedding IS NOT NULL FROM questions WHERE id = $1`, id).Scan(&hasEmbedding); err != nil {
			t.Fatalf("query embedding: %v", err)
		}
	}

	// Insert with an embedding; upsert with nil ⇒ COALESCE keeps the old vector.
	q := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, emb, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, q); err != nil {
		t.Fatalf("insert: %v", err)
	}
	stored, err := repo.FindExact(ctx, q.TextHash)
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}

	qNil := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, nil, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, qNil); err != nil {
		t.Fatalf("nil-embedding upsert: %v", err)
	}
	row(stored.ID)
	if !hasEmbedding {
		t.Error("embedding lost on nil upsert — COALESCE must preserve it")
	}

	// Upsert with a fresh embedding ⇒ stored.
	fresh := make([]float32, 1536)
	fresh[1] = 1.0
	qFresh := importQuestion("Embedding question?", []string{"a", "b"}, []string{"a"}, []string{"import"}, fresh, uploader.ID)
	if _, err := repo.UpsertFromImport(ctx, qFresh); err != nil {
		t.Fatalf("fresh-embedding upsert: %v", err)
	}
	row(stored.ID)
	if !hasEmbedding {
		t.Error("fresh embedding not stored")
	}
}

func TestQuestionRepo_UpsertFromImport_SessionLinkedSucceeds(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "linked@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	// Pre-existing session-linked question (as the pipeline would have made it).
	existing := importQuestion("Linked question?", []string{"a", "b"}, []string{"a"}, []string{"ai-generated"}, nil, user.ID)
	existing.Status = domain.QuestionStatusModeration
	existing.VerifiedAt = nil
	existing.VerifiedBy = nil
	qID, err := repo.Create(ctx, existing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9); err != nil {
		t.Fatalf("LinkToSession: %v", err)
	}

	// Import upsert on the same hash must succeed (the Delete guard is never in play).
	upd := importQuestion("Linked question?", []string{"a", "b", "c"}, []string{"b"}, []string{"import"}, nil, user.ID)
	created, err := repo.UpsertFromImport(ctx, upd)
	if err != nil {
		t.Fatalf("upsert on session-linked question: %v", err)
	}
	if created {
		t.Error("created = true, want false (existing hash)")
	}
	after, err := repo.FindByID(ctx, qID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if len(after.Choices) != 3 || after.Status != domain.QuestionStatusVerified {
		t.Errorf("after = %+v, want 3 choices + verified", after)
	}
}

// Regression guard (spec §7.2, §12.2): an import-update of an image-linked,
// fully-resolved question must NOT trigger cleanupImageBytesTx semantics.
func TestQuestionRepo_UpsertFromImport_PreservesImageBytes(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "bytes@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("original-bytes"), "image/jpeg", 800, 600)

	existing := importQuestion("Bytes question?", []string{"a", "b"}, []string{"a"}, []string{"ai-generated"}, nil, user.ID)
	qID, err := repo.Create(ctx, existing)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.LinkToSession(ctx, sess.ID, imgID, qID, 1, 0.9); err != nil {
		t.Fatalf("LinkToSession: %v", err)
	}

	// The question is already verified, so after the upsert the image has zero
	// unresolved questions — exactly the condition under which UpdateByExpert's
	// cleanupImageBytesTx would null the bytes. UpsertFromImport must not.
	upd := importQuestion("Bytes question?", []string{"a", "b"}, []string{"b"}, []string{"import"}, nil, user.ID)
	if _, err := repo.UpsertFromImport(ctx, upd); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var original []byte
	if err := pool.QueryRow(ctx, `SELECT original FROM images WHERE id = $1`, imgID).Scan(&original); err != nil {
		t.Fatalf("query image: %v", err)
	}
	if original == nil {
		t.Error("image bytes were nulled by import upsert — cleanupImageBytesTx semantics leaked in")
	}
}
