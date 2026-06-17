package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

var _ storage.SessionRepo = (*SessionRepo)(nil)

func (r *SessionRepo) Create(ctx context.Context, userID string, durationSec, bufferSec int) (*domain.Session, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, duration_seconds, buffer_seconds, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $2 + $3))
		RETURNING id, user_id, duration_seconds, buffer_seconds,
		          to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          to_char(expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		          status
	`, userID, durationSec, bufferSec)

	var s domain.Session
	err := row.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
		&s.StartedAt, &s.ExpiresAt, &s.Status)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &s, nil
}

func (r *SessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, duration_seconds, buffer_seconds,
		       to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       status
		FROM sessions WHERE id = $1
	`, id)

	var s domain.Session
	err := row.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
		&s.StartedAt, &s.ExpiresAt, &s.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find session: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find session: %w", err)
	}
	return &s, nil
}

func (r *SessionRepo) ListByUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, duration_seconds, buffer_seconds,
		       to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       status
		FROM sessions WHERE user_id = $1
		ORDER BY started_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*domain.Session
	for rows.Next() {
		var s domain.Session
		if err := rows.Scan(&s.ID, &s.UserID, &s.DurationSeconds, &s.BufferSeconds,
			&s.StartedAt, &s.ExpiresAt, &s.Status); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

func (r *SessionRepo) Close(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE sessions SET status = 'closed' WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("close session: %w", domain.ErrNotFound)
	}
	return nil
}
