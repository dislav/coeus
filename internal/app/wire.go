package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

type App struct {
	Config   *config.Config
	Pool     *pgxpool.Pool
	UserRepo *postgres.UserRepo
	JWTMgr   *auth.JWTManager
	Server   *httpapi.Server
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
	jwtMgr := auth.NewJWTManager(cfg.JWT)
	server := httpapi.NewServer(userRepo, jwtMgr, pool)

	return &App{
		Config: cfg, Pool: pool, UserRepo: userRepo,
		JWTMgr: jwtMgr, Server: server,
	}, nil
}

func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
}
