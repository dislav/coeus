package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type JobQueue struct {
	pool *pgxpool.Pool
}

func NewJobQueue(pool *pgxpool.Pool) *JobQueue {
	return &JobQueue{pool: pool}
}

var _ storage.JobQueue = (*JobQueue)(nil)

func (q *JobQueue) Enqueue(ctx context.Context, imageID, sessionID string) (string, error) {
	var id string
	err := q.pool.QueryRow(ctx, `
		INSERT INTO jobs (image_id, session_id, status)
		VALUES ($1, $2, 'pending')
		RETURNING id
	`, imageID, sessionID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("enqueue job: %w", err)
	}
	q.pool.Exec(ctx, "NOTIFY jobs_new")
	return id, nil
}

func (q *JobQueue) Claim(ctx context.Context) (*domain.Job, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE jobs
		SET status = 'processing', started_at = now(), attempts = attempts + 1
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			ORDER BY queued_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, image_id, session_id, status, attempts,
		          to_char(queued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`)

	var job domain.Job
	err = row.Scan(&job.ID, &job.ImageID, &job.SessionID, &job.Status,
		&job.Attempts, &job.QueuedAt, &job.StartedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no job available
		}
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return &job, nil
}

func (q *JobQueue) Complete(ctx context.Context, id string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status = 'done', finished_at = now() WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

func (q *JobQueue) Fail(ctx context.Context, id, errMsg string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', finished_at = now(), last_error = $1 WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

func (q *JobQueue) ReaperReclaim(ctx context.Context, staleThreshold time.Duration) (int, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', started_at = NULL
		WHERE status = 'processing'
		  AND started_at < now() - $1::interval
	`, staleThreshold.String())
	if err != nil {
		return 0, fmt.Errorf("reaper reclaim: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
