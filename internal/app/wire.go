package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/ai/embedder"
	"github.com/vlgrigoriev/coeus/internal/ai/enhancer"
	"github.com/vlgrigoriev/coeus/internal/ai/extractor"
	"github.com/vlgrigoriev/coeus/internal/ai/verifier"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

type App struct {
	Config       *config.Config
	Pool         *pgxpool.Pool
	UserRepo     *postgres.UserRepo
	SessionRepo  *postgres.SessionRepo
	ImageRepo    *postgres.ImageRepo
	QuestionRepo *postgres.QuestionRepo
	JobQueue     *postgres.JobQueue
	JWTMgr       *auth.JWTManager
	Server       *httpapi.Server
	WorkerPool   *pipeline.WorkerPool
}

func Build(ctx context.Context, cfg *config.Config) (*App, error) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("build pool: %w", err)
	}

	if err := postgres.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	userRepo := postgres.NewUserRepo(pool)
	sessionRepo := postgres.NewSessionRepo(pool)
	imageRepo := postgres.NewImageRepo(pool)
	questionRepo := postgres.NewQuestionRepo(pool)
	jobQueue := postgres.NewJobQueue(pool)
	jwtMgr := auth.NewJWTManager(cfg.JWT)

	server := httpapi.NewServer(
		userRepo, sessionRepo, imageRepo, questionRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)

	vips.Startup(nil)

	enh := enhancer.New(log)
	ext := extractor.New(cfg.AI.Vision, log)
	ver := verifier.New(cfg.AI.Reviewer, log)

	// Embedder is optional — skip when no API key is configured.
	var emb pipeline.AIEmbedder
	if cfg.AI.Embedder.APIKey != "" {
		emb = embedder.New(cfg.AI.Embedder, log)
		log.Info("embedder enabled", "model", cfg.AI.Embedder.Model)
	} else {
		log.Info("embedder disabled — semantic dedup skipped (set COEUS_AI_EMBEDDER_API_KEY to enable)")
	}

	pip := pipeline.NewPipeline(imageRepo, questionRepo, jobQueue,
		enh, ext, ver, emb, cfg.Pipeline, log)

	wp := pipeline.NewWorkerPool(jobQueue, pip,
		cfg.Workers, cfg.Pipeline, cfg.Postgres.DSN, log)

	return &App{
		Config: cfg, Pool: pool,
		UserRepo: userRepo, SessionRepo: sessionRepo,
		ImageRepo: imageRepo, QuestionRepo: questionRepo,
		JobQueue: jobQueue, JWTMgr: jwtMgr, Server: server,
		WorkerPool: wp,
	}, nil
}

func (a *App) Close() {
	if a.WorkerPool != nil {
		a.WorkerPool.Stop()
	}
	vips.Shutdown()
	if a.Pool != nil {
		a.Pool.Close()
	}
}
