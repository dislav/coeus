package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
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
	WorkerPool   *pipeline.WorkerPool // TODO(plan-3): constructed once AI clients exist
}

func Build(ctx context.Context, cfg *config.Config) (*App, error) {
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
		userRepo, sessionRepo, imageRepo, jobQueue,
		jwtMgr, pool, cfg.Upload,
	)

	// TODO(plan-3): construct Pipeline + WorkerPool + spawn workers.
	// Real ImageEnhancer / AIExtractor / AIVerifier / AIEmbedder implementations
	// are deferred to Plan 3 — until then, uploaded images stay in 'pending'.
	//
	// pip := pipeline.NewPipeline(imageRepo, questionRepo, jobQueue,
	//     enhancer, extractor, verifier, embedder, cfg.Pipeline, log)
	// wp := pipeline.NewWorkerPool(jobQueue, pip, cfg.Workers, cfg.Pipeline, cfg.Postgres.DSN, log)
	// wp.Start(ctx)
	// app.WorkerPool = wp

	return &App{
		Config: cfg, Pool: pool,
		UserRepo: userRepo, SessionRepo: sessionRepo,
		ImageRepo: imageRepo, QuestionRepo: questionRepo,
		JobQueue: jobQueue, JWTMgr: jwtMgr, Server: server,
	}, nil
}

func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
}
