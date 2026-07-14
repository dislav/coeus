package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

func (r *UserRepo) List(ctx context.Context, filter storage.UserFilter, limit, offset int) ([]*storage.User, error) {
	query := `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users`
	args := []interface{}{}
	idx := 1
	var where []string
	if filter.Role != nil {
		where = append(where, fmt.Sprintf("role = $%d", idx))
		args = append(args, *filter.Role)
		idx++
	}
	if filter.Active != nil {
		where = append(where, fmt.Sprintf("active = $%d", idx))
		args = append(args, *filter.Active)
		idx++
	}
	if filter.Query != nil && *filter.Query != "" {
		where = append(where, fmt.Sprintf("email ILIKE $%d", idx))
		args = append(args, "%"+escapeLike(*filter.Query)+"%")
		idx++
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var results []*storage.User
	for rows.Next() {
		var u storage.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		results = append(results, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return results, nil
}

// isUniqueViolation checks if err is a Postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
