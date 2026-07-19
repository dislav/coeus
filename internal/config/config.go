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
	Import   ImportConfig   `yaml:"import"`
}

type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	CORS            CORSConfig    `yaml:"cors"`
}

// CORSConfig configures the gin-contrib/cors middleware (spec §4.2).
// Only AllowedOrigins and AllowCredentials get env overrides; the rest are
// stable enough to live in config.yaml.
type CORSConfig struct {
	AllowedOrigins   []string      `yaml:"allowed_origins"`
	AllowedMethods   []string      `yaml:"allowed_methods"`
	AllowedHeaders   []string      `yaml:"allowed_headers"`
	ExposeHeaders    []string      `yaml:"expose_headers"`
	AllowCredentials bool          `yaml:"allow_credentials"`
	MaxAge           time.Duration `yaml:"max_age"`
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
	Vision   VisionConfig   `yaml:"vision"`
	Reviewer ReviewerConfig `yaml:"reviewer"`
	Embedder EmbedderConfig `yaml:"embedder"`
}

type VisionConfig struct {
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`
	Timeout  time.Duration `yaml:"timeout"`
	Thinking bool          `yaml:"thinking"`
}

type ReviewerConfig struct {
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
	// Effort sets the reasoning_effort sent to the reviewer model. Empty means
	// the param is omitted (model default). Accepted values: low|medium|high|max.
	// "max" is provider-dependent (DeepSeek may reject it).
	Effort string `yaml:"effort"`
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

// ImportConfig bounds the synchronous question-import endpoint
// (spec §10). 20000 keeps the worst-case runtime within server.write_timeout.
type ImportConfig struct {
	MaxRows int `yaml:"max_rows"`
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
	if v := os.Getenv("COEUS_AI_VISION_API_KEY"); v != "" {
		cfg.AI.Vision.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_VISION_BASE_URL"); v != "" {
		cfg.AI.Vision.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_VISION_THINKING"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			cfg.AI.Vision.Thinking = true
		case "false", "0":
			cfg.AI.Vision.Thinking = false
		default:
			return fmt.Errorf("invalid COEUS_AI_VISION_THINKING %q: expected true|false|1|0", v)
		}
	}
	if v := os.Getenv("COEUS_AI_REVIEWER_API_KEY"); v != "" {
		cfg.AI.Reviewer.APIKey = v
	}
	if v := os.Getenv("COEUS_AI_REVIEWER_BASE_URL"); v != "" {
		cfg.AI.Reviewer.BaseURL = v
	}
	if v := os.Getenv("COEUS_AI_REVIEWER_EFFORT"); v != "" {
		switch e := strings.ToLower(strings.TrimSpace(v)); e {
		case "low", "medium", "high", "max":
			cfg.AI.Reviewer.Effort = e
		default:
			return fmt.Errorf("invalid COEUS_AI_REVIEWER_EFFORT %q: expected low|medium|high|max", v)
		}
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
	if v := os.Getenv("COEUS_CORS_ALLOWED_ORIGINS"); v != "" {
		parts := strings.Split(v, ",")
		origins := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				origins = append(origins, t)
			}
		}
		if len(origins) > 0 {
			cfg.Server.CORS.AllowedOrigins = origins
		}
	}
	if v := os.Getenv("COEUS_CORS_ALLOW_CREDENTIALS"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			cfg.Server.CORS.AllowCredentials = true
		case "false", "0":
			cfg.Server.CORS.AllowCredentials = false
		default:
			return fmt.Errorf("invalid COEUS_CORS_ALLOW_CREDENTIALS %q: expected true|false|1|0", v)
		}
	}
	if v := os.Getenv("COEUS_IMPORT_MAX_ROWS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid COEUS_IMPORT_MAX_ROWS %q: %w", v, err)
		}
		cfg.Import.MaxRows = n
	}
	return nil
}

// Validate checks that secrets required by the current plan are set.
// AI API keys are required so the app fails fast at startup rather than
// failing per-request inside the pipeline.
func (c *Config) Validate() error {
	if c.Postgres.DSN == "" {
		return fmt.Errorf("postgres.dsn is required (set COEUS_POSTGRES_DSN)")
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("jwt.secret is required (set COEUS_JWT_SECRET)")
	}
	if c.AI.Vision.APIKey == "" {
		return fmt.Errorf("ai.vision.api_key is required (set COEUS_AI_VISION_API_KEY)")
	}
	if c.AI.Reviewer.APIKey == "" {
		return fmt.Errorf("ai.reviewer.api_key is required (set COEUS_AI_REVIEWER_API_KEY)")
	}
	if c.Server.CORS.AllowCredentials {
		for _, o := range c.Server.CORS.AllowedOrigins {
			if o == "*" {
				return fmt.Errorf("server.cors: allow_credentials cannot be combined with wildcard origin \"*\" (set COEUS_CORS_ALLOWED_ORIGINS to specific origins)")
			}
		}
	}
	// Embedder is optional — when no key is set, the pipeline skips
	// semantic dedup and stores questions without embeddings.
	return nil
}

func (c *UploadConfig) AllowedMimesMap() map[string]bool {
	m := make(map[string]bool, len(c.AllowedMimes))
	for _, mime := range c.AllowedMimes {
		m[strings.ToLower(mime)] = true
	}
	return m
}
