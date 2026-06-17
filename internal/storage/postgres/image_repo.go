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

type ImageRepo struct {
	pool *pgxpool.Pool
}

func NewImageRepo(pool *pgxpool.Pool) *ImageRepo {
	return &ImageRepo{pool: pool}
}

var _ storage.ImageRepo = (*ImageRepo)(nil)

func (r *ImageRepo) Create(ctx context.Context, sessionID string, original []byte, mime string, width, height int) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO images (session_id, original, mime, width, height)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, sessionID, original, mime, width, height).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create image: %w", err)
	}
	return id, nil
}

func (r *ImageRepo) FindByID(ctx context.Context, id string) (*domain.Image, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, session_id, original, enhanced, mime, width, height,
		       verification_report, extraction_error,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM images WHERE id = $1
	`, id)

	var img domain.Image
	err := row.Scan(&img.ID, &img.SessionID, &img.Original, &img.Enhanced,
		&img.Mime, &img.Width, &img.Height,
		&img.VerificationReport, &img.ExtractionError, &img.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find image: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find image: %w", err)
	}
	return &img, nil
}

func (r *ImageRepo) ListBySession(ctx context.Context, sessionID string) ([]*domain.Image, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, session_id, original, enhanced, mime, width, height,
		       verification_report, extraction_error,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM images WHERE session_id = $1 ORDER BY created_at
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	var images []*domain.Image
	for rows.Next() {
		var img domain.Image
		if err := rows.Scan(&img.ID, &img.SessionID, &img.Original, &img.Enhanced,
			&img.Mime, &img.Width, &img.Height,
			&img.VerificationReport, &img.ExtractionError, &img.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		images = append(images, &img)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	return images, nil
}

func (r *ImageRepo) UpdateEnhanced(ctx context.Context, id string, enhanced []byte) error {
	tag, err := r.pool.Exec(ctx, `UPDATE images SET enhanced = $1 WHERE id = $2`, enhanced, id)
	if err != nil {
		return fmt.Errorf("update enhanced: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update enhanced: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *ImageRepo) UpdateVerificationReport(ctx context.Context, id string, report []byte) error {
	tag, err := r.pool.Exec(ctx, `UPDATE images SET verification_report = $1 WHERE id = $2`, report, id)
	if err != nil {
		return fmt.Errorf("update verification report: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update verification report: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *ImageRepo) UpdateExtractionError(ctx context.Context, id string, errJSON []byte) error {
	tag, err := r.pool.Exec(ctx, `UPDATE images SET extraction_error = $1 WHERE id = $2`, errJSON, id)
	if err != nil {
		return fmt.Errorf("update extraction error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update extraction error: %w", domain.ErrNotFound)
	}
	return nil
}

func (r *ImageRepo) CleanBytes(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE images SET original = NULL, enhanced = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("clean image bytes: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("clean image bytes: %w", domain.ErrNotFound)
	}
	return nil
}
