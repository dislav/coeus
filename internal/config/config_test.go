package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_VISION_API_KEY", "kimi-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "ds-key")
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
	t.Setenv("COEUS_AI_VISION_API_KEY", "kimi-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "ds-key")
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

	t.Run("missing ai api keys", func(t *testing.T) {
		t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
		t.Setenv("COEUS_JWT_SECRET", "test-secret")
		t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
		t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")
		// vision key intentionally omitted

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}

		err = cfg.Validate()
		if err == nil {
			t.Fatal("Validate() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ai.vision.api_key") {
			t.Errorf("Validate() error = %q, expected to mention ai.vision.api_key", err.Error())
		}
	})
}

func TestValidate_WithSecrets(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_VISION_API_KEY", "vision-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
	t.Setenv("COEUS_AI_EMBEDDER_API_KEY", "emb-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error: %v", err)
	}
}

func TestValidate_EmbedderOptional(t *testing.T) {
	t.Setenv("COEUS_POSTGRES_DSN", "postgres://test:test@localhost:5432/coeus?sslmode=disable")
	t.Setenv("COEUS_JWT_SECRET", "test-secret")
	t.Setenv("COEUS_AI_VISION_API_KEY", "vision-key")
	t.Setenv("COEUS_AI_REVIEWER_API_KEY", "reviewer-key")
	// embedder key intentionally omitted

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() should succeed without embedder key, got: %v", err)
	}
}

func TestValidate_RejectsWildcardWithCredentials(t *testing.T) {
	cfg := &Config{
		Postgres: PostgresConfig{DSN: "x"},
		JWT:      JWTConfig{Secret: "x"},
		AI: AIConfig{
			Vision:   VisionConfig{APIKey: "x"},
			Reviewer: ReviewerConfig{APIKey: "x"},
		},
		Server: ServerConfig{CORS: CORSConfig{
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: wildcard origin + allow_credentials must error")
	}
	if !strings.Contains(err.Error(), "cors") && !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("Validate: error message should mention cors/wildcard, got: %v", err)
	}
}

func TestValidate_AllowsSpecificOriginWithCredentials(t *testing.T) {
	cfg := &Config{
		Postgres: PostgresConfig{DSN: "x"},
		JWT:      JWTConfig{Secret: "x"},
		AI: AIConfig{
			Vision:   VisionConfig{APIKey: "x"},
			Reviewer: ReviewerConfig{APIKey: "x"},
		},
		Server: ServerConfig{CORS: CORSConfig{
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowCredentials: true,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("specific origin + credentials should be valid: %v", err)
	}
}

func TestEnvOverrides_CORSAllowedOrigins(t *testing.T) {
	// whitespace-padded, leading/trailing commas, empty middle part
	t.Setenv("COEUS_CORS_ALLOWED_ORIGINS", " https://a.com , , https://b.com ,")
	cfg := &Config{}
	if err := applyEnvOverrides(cfg); err != nil {
		t.Fatalf("applyEnvOverrides: %v", err)
	}
	want := []string{"https://a.com", "https://b.com"}
	if len(cfg.Server.CORS.AllowedOrigins) != len(want) {
		t.Fatalf("origins: got %v want %v", cfg.Server.CORS.AllowedOrigins, want)
	}
	for i, o := range want {
		if cfg.Server.CORS.AllowedOrigins[i] != o {
			t.Errorf("origins[%d]: got %q want %q", i, cfg.Server.CORS.AllowedOrigins[i], o)
		}
	}
}

func TestEnvOverrides_CORSOriginsAllEmptyKeepsDefault(t *testing.T) {
	t.Setenv("COEUS_CORS_ALLOWED_ORIGINS", ",,")
	cfg := &Config{Server: ServerConfig{CORS: CORSConfig{AllowedOrigins: []string{"*"}}}}
	if err := applyEnvOverrides(cfg); err != nil {
		t.Fatalf("applyEnvOverrides: %v", err)
	}
	if len(cfg.Server.CORS.AllowedOrigins) != 1 || cfg.Server.CORS.AllowedOrigins[0] != "*" {
		t.Errorf("all-empty origins should keep YAML default: got %v", cfg.Server.CORS.AllowedOrigins)
	}
}

func TestEnvOverrides_CORSAllowCredentials(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"true", true}, {"1", true}, {"TRUE", true}, {"True", true},
		{"false", false}, {"0", false}, {"FALSE", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Setenv("COEUS_CORS_ALLOW_CREDENTIALS", tc.in)
			cfg := &Config{}
			if err := applyEnvOverrides(cfg); err != nil {
				t.Fatalf("applyEnvOverrides(%q): %v", tc.in, err)
			}
			if cfg.Server.CORS.AllowCredentials != tc.want {
				t.Errorf("AllowCredentials(%q): got %v want %v", tc.in, cfg.Server.CORS.AllowCredentials, tc.want)
			}
		})
	}
}

func TestEnvOverrides_CORSAllowCredentialsInvalidErrors(t *testing.T) {
	for _, bad := range []string{"yes", "on", "maybe", "2"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("COEUS_CORS_ALLOW_CREDENTIALS", bad)
			cfg := &Config{}
			err := applyEnvOverrides(cfg)
			if err == nil {
				t.Errorf("applyEnvOverrides(%q): expected error, got nil", bad)
			}
		})
	}
}
