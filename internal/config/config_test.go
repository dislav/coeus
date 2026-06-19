package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_KIMI_API_KEY", "kimi-key")
	t.Setenv("COEUS_AI_DEEPSEEK_API_KEY", "ds-key")
	t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")

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

func TestEnvOverridesYAML(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_KIMI_API_KEY", "kimi-key")
	t.Setenv("COEUS_AI_DEEPSEEK_API_KEY", "ds-key")
	t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
	t.Setenv("COEUS_SERVER_ADDR", ":9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("server.addr = %q, want %q", cfg.Server.Addr, ":9090")
	}
}

func TestAllowedMimesMap(t *testing.T) {
	upload := UploadConfig{
		AllowedMimes: []string{"application/pdf", "TEXT/PLAIN", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	}
	m := upload.AllowedMimesMap()

	want := []string{"application/pdf", "text/plain", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}
	if len(m) != len(want) {
		t.Errorf("allowed mimes map length = %d, want %d", len(m), len(want))
	}
	for _, mime := range want {
		if !m[mime] {
			t.Errorf("expected mime %q to be present in map", mime)
		}
	}
}

func TestValidate_MissingSecrets(t *testing.T) {
	t.Run("missing postgres dsn", func(t *testing.T) {
		t.Setenv("COEUS_JWT_SECRET", "test-secret")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		err = cfg.Validate()
		if err == nil {
			t.Fatal("Validate() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "postgres.dsn") {
			t.Errorf("Validate() error = %q, expected to mention postgres.dsn", err.Error())
		}
	})

	t.Run("missing jwt secret", func(t *testing.T) {
		t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		err = cfg.Validate()
		if err == nil {
			t.Fatal("Validate() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "jwt.secret") {
			t.Errorf("Validate() error = %q, expected to mention jwt.secret", err.Error())
		}
	})
}

func TestValidate_WithSecrets(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}
