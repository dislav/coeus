package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type QuestionRepo struct {
	pool *pgxpool.Pool
}

func NewQuestionRepo(pool *pgxpool.Pool) *QuestionRepo {
	return &QuestionRepo{pool: pool}
}

var _ storage.QuestionRepo = (*QuestionRepo)(nil)

func (r *QuestionRepo) Create(ctx context.Context, q *domain.Question) (string, error) {
	choicesJSON, _ := json.Marshal(q.Choices)
	answersJSON, _ := json.Marshal(q.Answers)

	var embedding interface{}
	if q.Embedding != nil {
		embedding = pgvector.NewVector(q.Embedding)
	}

	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO questions (number, question, question_normalized, question_hash,
		    choices, answers, choice_labeling, confidence,
		    explanation, embedding, status, verified_at, verified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id
	`, q.Number, q.Text, q.TextNorm, q.TextHash,
		choicesJSON, answersJSON, q.ChoiceLabeling,
		q.Confidence, q.Explanation, embedding, q.Status,
		q.VerifiedAt, q.VerifiedBy,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create question: %w", err)
	}

	for _, tagName := range q.Tags {
		if err := r.linkTag(ctx, id, tagName); err != nil {
			return id, fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}
	return id, nil
}

func (r *QuestionRepo) LinkToSession(ctx context.Context, sessionID, imageID, questionID string, number int, confidence float64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO session_questions (session_id, image_id, question_id, extracted_number, extracted_confidence)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id, image_id, question_id) DO NOTHING
	`, sessionID, imageID, questionID, number, confidence)
	if err != nil {
		return fmt.Errorf("link question to session: %w", err)
	}
	return nil
}

func (r *QuestionRepo) FindByID(ctx context.Context, id string) (*domain.Question, error) {
	row := r.pool.QueryRow(ctx, questionSelectBase+` WHERE q.id = $1`, id)
	return scanQuestion(row)
}

func (r *QuestionRepo) FindExact(ctx context.Context, hash string) (*domain.Question, error) {
	row := r.pool.QueryRow(ctx, questionSelectBase+` WHERE q.question_hash = $1`, hash)
	q, err := scanQuestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // no match — not an error
		}
		return nil, err
	}
	return q, nil
}

func (r *QuestionRepo) FindSemantic(ctx context.Context, embedding []float32, threshold float64) (*domain.Question, error) {
	maxDist := 1.0 - threshold
	row := r.pool.QueryRow(ctx, questionSelectBase+`
		WHERE q.embedding IS NOT NULL
		  AND q.embedding <=> $1 <= $2
		ORDER BY q.embedding <=> $1
		LIMIT 1
	`, pgvector.NewVector(embedding), maxDist)

	q, err := scanQuestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return q, nil
}

func (r *QuestionRepo) UpdateFromVerification(ctx context.Context, id string, confidence float64, explanation string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE questions SET confidence = $1, explanation = $2, updated_at = now()
		WHERE id = $3
	`, confidence, explanation, id)
	if err != nil {
		return fmt.Errorf("update from verification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update from verification: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *QuestionRepo) ListForUser(ctx context.Context, sessionID string, statusFilter string, limit, offset int) ([]*storage.QuestionWithSession, error) {
	query := `
		SELECT q.id, q.number, q.question, q.choices, q.answers,
		       q.choice_labeling, q.confidence, q.status,
		       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
		FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		WHERE sq.session_id = $1`
	args := []interface{}{sessionID}
	idx := 2
	if statusFilter != "" {
		query += fmt.Sprintf(` AND q.status = $%d`, idx)
		args = append(args, statusFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY sq.extracted_number LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list questions for user: %w", err)
	}
	defer rows.Close()

	var results []*storage.QuestionWithSession
	for rows.Next() {
		qws, err := scanQuestionWithSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		results = append(results, qws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list questions for user: %w", err)
	}
	return results, nil
}

func (r *QuestionRepo) ListForModeration(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*domain.Question, error) {
	query := questionSelectBase
	args := []interface{}{}
	idx := 1
	if tagFilter != "" {
		query = `
		SELECT DISTINCT q.id, q.number, q.question, q.question_normalized, q.question_hash,
		       q.choices, q.answers, q.choice_labeling,
		       q.confidence, q.explanation,
		       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       q.verified_by::text,
		       q.status
		FROM questions q JOIN question_tags qt ON qt.question_id = q.id JOIN tags t ON t.id = qt.tag_id`
	}
	query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
	args = append(args, statusFilter)
	idx++
	if tagFilter != "" {
		query += fmt.Sprintf(` AND t.name = $%d`, idx)
		args = append(args, tagFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY q.created_at LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list for moderation: %w", err)
	}
	defer rows.Close()

	var questions []*domain.Question
	for rows.Next() {
		q, err := scanQuestionRow(rows)
		if err != nil {
			return nil, err
		}
		q.Tags, _ = r.getTags(ctx, q.ID)
		questions = append(questions, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list for moderation: %w", err)
	}
	return questions, nil
}

func (r *QuestionRepo) FindExpertByID(ctx context.Context, id string) (*storage.QuestionExpertView, error) {
	row := r.pool.QueryRow(ctx, questionExpertSelectBase+` WHERE q.id = $1`, id)
	ev, err := scanQuestionExpert(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find expert question: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find expert question: %w", err)
	}
	ev.Tags, _ = r.getTags(ctx, ev.ID)
	return ev, nil
}

func (r *QuestionRepo) ListForModerationExpert(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*storage.QuestionExpertView, error) {
	query := questionExpertSelectBase
	args := []interface{}{}
	idx := 1
	query += fmt.Sprintf(` WHERE q.status = $%d`, idx)
	args = append(args, statusFilter)
	idx++
	if tagFilter != "" {
		query += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM question_tags qt
			JOIN tags t ON t.id = qt.tag_id
			WHERE qt.question_id = q.id AND t.name = $%d)`, idx)
		args = append(args, tagFilter)
		idx++
	}
	query += fmt.Sprintf(` ORDER BY q.created_at LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list moderation expert: %w", err)
	}
	defer rows.Close()

	results := make([]*storage.QuestionExpertView, 0)
	for rows.Next() {
		ev, err := scanQuestionExpert(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		ev.Tags, _ = r.getTags(ctx, ev.ID)
		results = append(results, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list moderation expert: %w", err)
	}
	return results, nil
}

// FindForUserByID returns the question only if it is linked to a session owned
// by userID (enforces ownership at the repo level; 404 otherwise). It reuses
// QuestionWithSession and picks the earliest-linked session deterministically.
func (r *QuestionRepo) FindForUserByID(ctx context.Context, questionID, userID string) (*storage.QuestionWithSession, error) {
	query := `
		SELECT q.id, q.number, q.question, q.choices, q.answers,
		       q.choice_labeling, q.confidence, q.status,
		       sq.session_id, sq.image_id, sq.extracted_number, sq.extracted_confidence
		FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		JOIN sessions s ON s.id = sq.session_id
		WHERE sq.question_id = $1 AND s.user_id = $2
		ORDER BY sq.id
		LIMIT 1`
	row := r.pool.QueryRow(ctx, query, questionID, userID)
	qws, err := scanQuestionWithSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user question: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user question: %w", err)
	}
	return qws, nil
}

func (r *QuestionRepo) UpdateByExpert(ctx context.Context, id string, answers, choices []string, explanation string, confidence float64, tags []string, expertID string) error {
	choicesJSON, _ := json.Marshal(choices)
	answersJSON, _ := json.Marshal(answers)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update by expert: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE questions
		SET answers = $1, choices = $2, explanation = $3, confidence = $4,
		    status = 'verified', verified_at = now(), verified_by = $5, updated_at = now()
		WHERE id = $6
	`, answersJSON, choicesJSON, explanation, confidence, expertID, id)
	if err != nil {
		return fmt.Errorf("update by expert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update by expert: %w", domain.ErrNotFound)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM question_tags WHERE question_id = $1`, id); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, tagName := range tags {
		if err := r.linkTagTx(ctx, tx, id, tagName); err != nil {
			return fmt.Errorf("link tag %q: %w", tagName, err)
		}
	}

	// --- Image-byte cleanup (spec §3.5), same transaction as the status flip ---
	if err := cleanupImageBytesTx(ctx, tx, id); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update by expert: %w", err)
	}
	return nil
}

// cleanupImageBytesTx nulls out original+enhanced bytes for every image linked
// to the patched question that no longer has any sibling question in
// 'moderation' or 'error'. Runs inside the caller's tx so it is atomic with
// the status flip. (CountUnresolvedForImage uses r.pool and can't be reused
// here; the count SQL is inlined tx-scoped.)
//
// Best-effort under READ COMMITTED: two concurrent expert PATCHes on different
// questions sharing the same image may both see the other's question still
// unresolved and both skip the byte null (benign — bytes retained until a
// later resolution touches the image; no correctness/visibility impact).
func cleanupImageBytesTx(ctx context.Context, tx pgx.Tx, questionID string) error {
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT sq.image_id FROM session_questions sq WHERE sq.question_id = $1`,
		questionID)
	if err != nil {
		return fmt.Errorf("select linked images: %w", err)
	}
	defer rows.Close()

	var imageIDs []string
	for rows.Next() {
		var imgID string
		if err := rows.Scan(&imgID); err != nil {
			return fmt.Errorf("scan image id: %w", err)
		}
		imageIDs = append(imageIDs, imgID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate linked images: %w", err)
	}

	for _, imgID := range imageIDs {
		var unresolved int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM session_questions sq
			JOIN questions q ON q.id = sq.question_id
			WHERE sq.image_id = $1 AND q.status IN ('moderation', 'error')
		`, imgID).Scan(&unresolved); err != nil {
			return fmt.Errorf("count unresolved for image %s: %w", imgID, err)
		}
		if unresolved == 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE images SET original = NULL, enhanced = NULL WHERE id = $1`, imgID); err != nil {
				return fmt.Errorf("clean image bytes %s: %w", imgID, err)
			}
		}
	}
	return nil
}

func (r *QuestionRepo) CountUnresolvedForImage(ctx context.Context, imageID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM session_questions sq
		JOIN questions q ON q.id = sq.question_id
		WHERE sq.image_id = $1 AND q.status IN ('moderation', 'error')
	`, imageID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unresolved: %w", err)
	}
	return count, nil
}

func (r *QuestionRepo) linkTag(ctx context.Context, questionID, tagName string) error {
	var tagID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO tags (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`, tagName).Scan(&tagID)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO question_tags (question_id, tag_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, questionID, tagID)
	return err
}

func (r *QuestionRepo) linkTagTx(ctx context.Context, tx pgx.Tx, questionID, tagName string) error {
	var tagID string
	err := tx.QueryRow(ctx, `
		INSERT INTO tags (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`, tagName).Scan(&tagID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO question_tags (question_id, tag_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, questionID, tagID)
	return err
}

func (r *QuestionRepo) getTags(ctx context.Context, questionID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.name FROM question_tags qt
		JOIN tags t ON t.id = qt.tag_id
		WHERE qt.question_id = $1
	`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tags = append(tags, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get tags: %w", err)
	}
	return tags, nil
}

const questionSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status
	FROM questions q`

// questionExpertSelectBase is questionSelectBase's column list plus the
// representative image_id and verification_report-presence flag, expressed as
// correlated subqueries so the queue can keep its ORDER BY q.created_at
// (DISTINCT ON would force ORDER BY q.id first).
const questionExpertSelectBase = `
	SELECT q.id, q.number, q.question, q.question_normalized, q.question_hash,
	       q.choices, q.answers, q.choice_labeling,
	       q.confidence, q.explanation,
	       to_char(q.verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	       q.verified_by::text,
	       q.status,
	       (SELECT sq.image_id FROM session_questions sq
	           WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1) AS image_id,
	       COALESCE((SELECT im.verification_report IS NOT NULL
	          FROM session_questions sq JOIN images im ON im.id = sq.image_id
	          WHERE sq.question_id = q.id ORDER BY sq.id LIMIT 1), false) AS has_verification_report
	FROM questions q`

func scanQuestion(row pgx.Row) (*domain.Question, error) {
	q := &domain.Question{}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	err := row.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(choices, &q.Choices)
	json.Unmarshal(answers, &q.Answers)
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verifiedBy
	return q, nil
}

func scanQuestionRow(rows pgx.Rows) (*domain.Question, error) {
	q := &domain.Question{}
	var choices, answers []byte
	var verifiedAt, verifiedBy *string
	err := rows.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verifiedBy, &q.Status,
	)
	if err != nil {
		return nil, fmt.Errorf("scan question: %w", err)
	}
	json.Unmarshal(choices, &q.Choices)
	json.Unmarshal(answers, &q.Answers)
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verifiedBy
	return q, nil
}

// scanQuestionExpert scans the questionSelectBase columns plus image_id and has_report.
// Accepts anything with a Scan(...) method (pgx.Row and pgx.Rows both qualify).
func scanQuestionExpert(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionExpertView, error) {
	var (
		q                   domain.Question
		choices, answers    []byte
		verifiedAt, verBy   *string
	)
	var imageID *string
	var hasReport bool
	if err := row.Scan(
		&q.ID, &q.Number, &q.Text, &q.TextNorm, &q.TextHash,
		&choices, &answers, &q.ChoiceLabeling,
		&q.Confidence, &q.Explanation, &verifiedAt, &verBy, &q.Status,
		&imageID, &hasReport,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(choices, &q.Choices)
	_ = json.Unmarshal(answers, &q.Answers)
	q.VerifiedAt = verifiedAt
	q.VerifiedBy = verBy
	ev := &storage.QuestionExpertView{Question: &q, HasVerificationReport: hasReport}
	if imageID != nil {
		ev.ImageID = *imageID
	}
	return ev, nil
}

// scanQuestionWithSession scans the 13-column SELECT used by ListForUser and
// FindForUserByID. Accepts both pgx.Row and pgx.Rows.
func scanQuestionWithSession(row interface {
	Scan(dest ...any) error
}) (*storage.QuestionWithSession, error) {
	qws := &storage.QuestionWithSession{Question: &domain.Question{}}
	var choices, answers []byte
	if err := row.Scan(
		&qws.ID, &qws.Number, &qws.Text,
		&choices, &answers, &qws.ChoiceLabeling, &qws.Confidence, &qws.Status,
		&qws.SessionID, &qws.ImageID, &qws.ExtractedNumber, &qws.ExtractedConfidence,
	); err != nil {
		return nil, err
	}
	json.Unmarshal(choices, &qws.Choices)
	json.Unmarshal(answers, &qws.Answers)
	return qws, nil
}
