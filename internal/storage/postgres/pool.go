package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/vlgrigoriev/coeus/internal/config"
)

// NewPool creates a PGX connection pool from config.
func NewPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	// The pool registers the pgvector `vector` type on every connection via the
	// AfterConnect hook below, which also runs during Ping. The `vector` type
	// only exists once the pgvector extension is installed, so the extension
	// MUST be present before the pool is created — otherwise a freshly
	// initialized database (e.g. a brand-new Docker Compose stack) fails at
	// startup with "vector type not found in the database" before the app's
	// migrations (which create the extension) ever run. Ensure it here.
	if err := ensurePgvectorExtension(ctx, cfg.DSN); err != nil {
		return nil, fmt.Errorf("ensure pgvector extension: %w", err)
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns

	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("register pgvector types: %w", err)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// ensurePgvectorExtension opens a short-lived connection and installs the
// pgvector extension if it is missing. CREATE EXTENSION IF NOT EXISTS is
// idempotent, so this is a cheap no-op once the extension exists. Migration
// 0001_extensions.sql repeats the statement for good measure.
func ensurePgvectorExtension(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("create extension: %w", err)
	}
	return nil
}

// RunMigrations applies all embedded SQL files in order.
// Migrations are idempotent (CREATE TABLE IF NOT EXISTS).
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		slog.Info("migration applied", "file", name)
	}
	return nil
}
