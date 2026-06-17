package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	os.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	os.Setenv("COEUS_JWT_SECRET", "test-secret")
	os.Setenv("COEUS_AI_KIMI_API_KEY", "kimi-key")
	os.Setenv("COEUS_AI_DEEPSEEK_API_KEY", "ds-key")
	os.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
	defer func() {
		os.Unsetenv("COEUS_POSTGRES_DSN")
		os.Unsetenv("COEUS_JWT_SECRET")
		os.Unsetenv("COEUS_AI_KIMI_API_KEY")
		os.Unsetenv("COEUS_AI_DEEPSEEK_API_KEY")
		os.Unsetenv("COEUS_AI_EMBEDDER_API_KEY")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("server.addr = %q, want %q", cfg.Server.Addr, ":8080")
	}
	if cfg.JWT.Secret != "test-secret" {
		t.Errorf("jwt.secret = %q, want %q", cfg.JWT.Secret, "test-secret")
	}
	if cfg.Pipeline.ExtractMaxAttempts != 3 {
		t.Errorf("extract_max_attempts = %d, want 3", cfg.Pipeline.ExtractMaxAttempts)
	}
	if cfg.Workers.Count != 4 {
		t.Errorf("workers.count = %d, want 4", cfg.Workers.Count)
	}
	if cfg.JWT.AccessTTL != time.Hour {
		t.Errorf("jwt.access_ttl = %v, want %v", cfg.JWT.AccessTTL, time.Hour)
	}
}
