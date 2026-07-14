package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

var _ storage.UserRepo = (*UserRepo)(nil)

func (r *UserRepo) Create(ctx context.Context, email, passwordHash, role string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, role, active, token_version,
		          to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`, email, passwordHash, role)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("create user: %w", domain.ErrDuplicate)
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE email = $1
	`, email)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user by email: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return &u, nil
}

func (r *UserRepo) FindByID(ctx context.Context, id string) (*storage.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE id = $1
	`, id)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find user by id: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &u, nil
}

// isUniqueViolation checks if err is a Postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
