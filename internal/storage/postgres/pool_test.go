package postgres

import (
	"context"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/config"
)

// TestNewPoolOnFreshDatabase exercises the production startup path: NewPool is
// pointed at a freshly initialized database where the pgvector extension has
// NOT been installed and migrations have NOT run. NewPool must bootstrap the
// extension itself before its AfterConnect hook registers the `vector` type,
// otherwise startup fails with "vector type not found in the database".
func TestNewPoolOnFreshDatabase(t *testing.T) {
	ctx := context.Background()
	connStr := startTestContainer(t)

	cfg := config.PostgresConfig{DSN: connStr, MaxConns: 5, MinConns: 1}
	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool on fresh DB: %v", err)
	}
	t.Cleanup(pool.Close)

	// The `vector` type must be registered on pooled connections and usable.
	var v string
	if err := pool.QueryRow(ctx, "SELECT '[1,2,3]'::vector::text").Scan(&v); err != nil {
		t.Fatalf("query vector: %v", err)
	}
	if v != "[1,2,3]" {
		t.Fatalf("unexpected vector value: %q", v)
	}
}
