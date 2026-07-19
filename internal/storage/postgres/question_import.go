package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgvector/pgvector-go"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// UpsertFromImport implements storage.QuestionRepo.UpsertFromImport (spec §7.1).
// One transaction: ON CONFLICT (question_hash) upsert + tag delete-and-reinsert.
// It deliberately does NOT call cleanupImageBytesTx — image bytes are a
// cross-aggregate concern of expert edits, not of import.
//
// question_normalized is absent from the UPDATE SET on purpose: a hash
// conflict implies identical normalized text under the current
// NormalizeQuestion, so the stored value is already correct. embedding uses
// COALESCE: a hash match implies identical text, so the old vector stays
// valid when the new one is nil.
func (r *QuestionRepo) UpsertFromImport(ctx context.Context, q *domain.Question) (bool, error) {
	choicesJSON, _ := json.Marshal(q.Choices)
	answersJSON, _ := json.Marshal(q.Answers)

	var embedding interface{}
	if q.Embedding != nil {
		embedding = pgvector.NewVector(q.Embedding)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin import upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	var id string
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    choices, answers, choice_labeling, question_type, confidence,
		    explanation, embedding, status, verified_at, verified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (question_hash) DO UPDATE SET
		    question       = EXCLUDED.question,
		    choices        = EXCLUDED.choices,
		    answers        = EXCLUDED.answers,
		    explanation    = EXCLUDED.explanation,
		    confidence     = EXCLUDED.confidence,
		    question_type  = EXCLUDED.question_type,
		    status         = 'verified',
		    verified_at    = now(),
		    verified_by    = EXCLUDED.verified_by,
		    embedding      = COALESCE(EXCLUDED.embedding, questions.embedding),
		    updated_at     = now()
		-- (xmax = 0) is true on INSERT, false on UPDATE — the canonical
		-- Postgres trick to distinguish the two arms of an upsert in a
		-- single round trip.
		RETURNING id, (xmax = 0)
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		choicesJSON, answersJSON, q.ChoiceLabeling, q.Type,
		q.Confidence, q.Explanation, embedding, q.Status,
		q.VerifiedAt, q.VerifiedBy,
	).Scan(&id, &created)
	if err != nil {
		return false, fmt.Errorf("import upsert: %w", err)
	}

	// Tag delete-and-reinsert, same transaction as the upsert.
	if _, err := tx.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id); err != nil {
		return false, fmt.Errorf("clear tags: %w", err)
	}
	for _, tagName := range q.Tags {
		if err := r.linkTagTx(ctx, tx, id, tagName); err != nil {
			return false, fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit import upsert: %w", err)
	}
	return created, nil
}
