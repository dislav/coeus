package config

import (
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed config.yaml
var defaultConfigYAML []byte

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Postgres PostgresConfig `yaml:"postgres"`
	JWT      JWTConfig      `yaml:"jwt"`
	AI       AIConfig       `yaml:"ai"`
	Pipeline PipelineConfig `yaml:"pipeline"`
	Workers  WorkersConfig  `yaml:"workers"`
	Upload   UploadConfig   `yaml:"upload"`
}

type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type PostgresConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int32  `yaml:"max_conns"`
	MinConns int32  `yaml:"min_conns"`
}

type JWTConfig struct {
	Secret     string        `yaml:"secret"`
	AccessTTL  time.Duration `yaml:"access_ttl"`
	RefreshTTL time.Duration `yaml:"refresh_ttl"`
}

type AIConfig struct {
	Kimi     KimiConfig     `yaml:"kimi"`
	DeepSeek DeepSeekConfig `yaml:"deepseek"`
	Embedder EmbedderConfig `yaml:"embedder"`
}

type KimiConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}

type DeepSeekConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
}

type EmbedderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	Dim     int    `yaml:"dim"`
}

type PipelineConfig struct {
	ExtractMaxAttempts int           `yaml:"extract_max_attempts"`
	SemanticThreshold  float64       `yaml:"semantic_threshold"`
	ReaperInterval     time.Duration `yaml:"reaper_interval"`
	StaleThreshold     time.Duration `yaml:"stale_threshold"`
	MaxQueueAttempts   int           `yaml:"max_queue_attempts"`
}

type WorkersConfig struct {
	Count int `yaml:"count"`
}

type UploadConfig struct {
	MaxBytes     int64    `yaml:"max_bytes"`
	AllowedMimes []string `yaml:"allowed_mimes"`
}

// Load reads the embedded config.yaml and applies env overrides.
// Secrets (DSN, JWT secret, API keys) must come from env.
func Load() (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(defaultConfigYAML, &cfg); err != nil {
		return nil, fmt.Errorf("parse embedded config.yaml: %w", err)
	}
	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, fmt.Errorf("apply env overrides: %w", err)
	}
	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) error {
	if v := os.Getenv("COEUS_POSTGRES_DSN"); v != "" {
		cfg.Postgres.DSN = v
	}
	if v := os.Getenv("COEUS_JWT_SECRET"); v != "" {
		cfg.JWT.Secret = v
	}
	if v := os.Getenv("COEUS_AI_KIMI_API_KEY"); v != "" {
		cfg.AI.Kimi.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_KIMI_BASE_URL"); v != "" {
		cfg.AI.Kimi.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_DEEPSEEK_API_KEY"); v != "" {
		cfg.AI.DeepSeek.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_DEEPSEEK_BASE_URL"); v != "" {
		cfg.AI.DeepSeek.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_EMBEDDER_API_KEY"); v != "" {
		cfg.AI.Embedder.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_EMBEDDER_BASE_URL"); v != "" {
		cfg.AI.Embedder.BaseURL = v
	}
	if v := os.Getenv("COEUS_SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("COEUS_WORKERS_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid COEUS_WORKERS_COUNT %q: %w", v, err)
		}
		cfg.Workers.Count = n
	}
	return nil
}

// Validate checks that required secrets and connection strings are configured.
func (c *Config) Validate() error {
	if c.Postgres.DSN == "" {
		return fmt.Errorf("postgres.dsn is required (set COEUS_POSTGRES_DSN)")
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt.secret is required (set COEUS_JWT_SECRET)")
	}
	if c.AI.Kimi.APIKey == "" {
		return fmt.Errorf("ai.kimi.api_key is required (set COEUS_AI_KIMI_API_KEY)")
	}
	if c.AI.DeepSeek.APIKey == "" {
		return fmt.Errorf("ai.deepseek.api_key is required (set COEUS_AI_DEEPSEEK_API_KEY)")
	}
	if c.AI.Embedder.APIKey == "" {
		return fmt.Errorf("ai.embedder.api_key is required (set COEUS_AI_EMBEDDER_API_KEY)")
	}
	return nil
}

func (c *UploadConfig) AllowedMimesMap() map[string]bool {
	m := make(map[string]bool, len(c.AllowedMimes))
	for _, mime := range c.AllowedMimes {
		m[strings.ToLower(mime)] = true
	}
	return m
}
