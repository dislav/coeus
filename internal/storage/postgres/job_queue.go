package postgres

import (
	"context"
	"errors"
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
	// Best-effort NOTIFY; workers also poll, so failure is non-fatal.
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
		if errors.Is(err, pgx.ErrNoRows) {
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
		UPDATE jobs SET status = 'done', finished_at = now()
		WHERE id = $1 AND status = 'processing'
	`, id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

func (q *JobQueue) Fail(ctx context.Context, id, errMsg string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', finished_at = now(), last_error = $1
		WHERE id = $2 AND status = 'processing'
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

func (q *JobQueue) ReaperReclaim(ctx context.Context, staleThreshold time.Duration, maxAttempts int) (reclaimed int, failed int, err error) {
	threshold := fmt.Sprintf("%f seconds", staleThreshold.Seconds())

	// 1. Fail jobs that have exhausted their attempts
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status='failed', finished_at=now()
		WHERE status='processing'
		  AND started_at < now() - $1::interval
		  AND attempts >= $2`,
		threshold, maxAttempts)
	if err != nil {
		return 0, 0, fmt.Errorf("reaper fail: %w", err)
	}
	failed = int(tag.RowsAffected())

	// 2. Reclaim (reset to pending) jobs that still have attempts left
	tag, err = q.pool.Exec(ctx, `
		UPDATE jobs SET status='pending', attempts=attempts+1, started_at=NULL
		WHERE status='processing'
		  AND started_at < now() - $1::interval
		  AND attempts < $2`,
		threshold, maxAttempts)
	if err != nil {
		return 0, failed, fmt.Errorf("reaper reclaim: %w", err)
	}
	reclaimed = int(tag.RowsAffected())

	return reclaimed, failed, nil
}

func (q *JobQueue) FindByImageID(ctx context.Context, imageID string) (*domain.Job, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, image_id, session_id, status, attempts,
		       COALESCE(last_error, ''),
		       to_char(queued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       COALESCE(to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
		       COALESCE(to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
		FROM jobs WHERE image_id = $1 ORDER BY queued_at DESC LIMIT 1
	`, imageID)

	var job domain.Job
	err := row.Scan(&job.ID, &job.ImageID, &job.SessionID, &job.Status,
		&job.Attempts, &job.LastError, &job.QueuedAt, &job.StartedAt, &job.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find job by image: %w", err)
	}
	return &job, nil
}
