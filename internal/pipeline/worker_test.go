package pipeline

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	pgstore "github.com/vlgrigoriev/coeus/internal/storage/postgres"
)

// --- Unit tests (no DB) ---

func TestWorkerPool_StopWithoutStart(t *testing.T) {
	wp := NewWorkerPool(nil, nil,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		"unused", slog.Default())
	// Stop on an unstarted pool should be a safe no-op
	wp.Stop()
}

func TestWorkerPool_StartAndStop(t *testing.T) {
	jq := newFakeJobQueue()
	p := NewPipeline(
		newFakeImageRepo(nil), newFakeQuestionRepo(), jq,
		&fakeEnhancer{}, &fakeExtractor{}, &fakeVerifier{}, &fakeEmbedder{},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		quietLogger(),
	)
	wp := NewWorkerPool(jq, p,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: time.Second, StaleThreshold: time.Minute},
		"postgres://unused", quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Workers will fail to connect (bad DSN) but should not crash
	wp.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	wp.Stop()
}

// --- Integration tests (Testcontainers) ---

func setupPipelineTestDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	container, err := tcpg.Run(ctx,
		"pgvector/pgvector:pg16",
		tcpg.WithDatabase("coeus_test"),
		tcpg.WithUsername("test"),
		tcpg.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		container.Terminate(ctx)
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pgstore.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pool, connStr
}

func TestWorkerPool_IntegrationProcessesJob(t *testing.T) {
	pool, dsn := setupPipelineTestDB(t)

	ctx := context.Background()
	userRepo := pgstore.NewUserRepo(pool)
	sessRepo := pgstore.NewSessionRepo(pool)
	imgRepo := pgstore.NewImageRepo(pool)
	qRepo := pgstore.NewQuestionRepo(pool)
	jq := pgstore.NewJobQueue(pool)

	user, _ := userRepo.Create(ctx, "worker@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 100, 100)

	// Pipeline with fakes that always succeed
	pipeline := NewPipeline(imgRepo, qRepo, jq,
		&fakeEnhancer{enhanced: []byte("enhanced")},
		&fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
			{Number: 1, Text: "Test question?", Answers: []Answer{{"A", "yes"}}},
		}}},
		&fakeVerifier{result: VerifyResult{Summary: VerificationSummary{Results: []VerifiedQuestion{
			{Index: 0, Confidence: 0.9, Explanation: "ok"},
		}}}},
		&fakeEmbedder{embedding: make([]float32, 1536)},
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92,
			ReaperInterval: 2 * time.Second, StaleThreshold: time.Minute},
		quietLogger(),
	)

	wp := NewWorkerPool(jq, pipeline,
		config.WorkersConfig{Count: 2},
		config.PipelineConfig{ReaperInterval: 2 * time.Second, StaleThreshold: time.Minute},
		dsn, quietLogger())

	wpCtx, cancel := context.WithCancel(context.Background())
	wp.Start(wpCtx)
	defer func() {
		cancel()
		wp.Stop()
	}()

	// Enqueue a job — NOTIFY should wake a worker
	jobID, err := jq.Enqueue(ctx, imgID, sess.ID)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Poll for job completion (max 10s)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, _ := jq.FindByImageID(ctx, imgID)
		if job != nil && job.Status == domain.JobStatusDone {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	job, _ := jq.FindByImageID(ctx, imgID)
	if job == nil || job.Status != domain.JobStatusDone {
		t.Fatalf("job %s did not complete, status=%v", jobID, job)
	}
}

func TestWorkerPool_IntegrationReaperReclaims(t *testing.T) {
	pool, _ := setupPipelineTestDB(t)

	ctx := context.Background()
	userRepo := pgstore.NewUserRepo(pool)
	sessRepo := pgstore.NewSessionRepo(pool)
	imgRepo := pgstore.NewImageRepo(pool)
	jq := pgstore.NewJobQueue(pool)

	user, _ := userRepo.Create(ctx, "reaper2@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 100, 100)

	// Enqueue and manually claim (marks as processing with stale_threshold=0)
	jq.Enqueue(ctx, imgID, sess.ID)
	claimed, _ := jq.Claim(ctx)
	if claimed == nil {
		t.Fatal("expected to claim a job")
	}

	// Reaper with 0 threshold reclaims immediately (attempts=1 < maxAttempts)
	reclaimed, failed, err := jq.ReaperReclaim(ctx, 0*time.Second, 3)
	if err != nil {
		t.Fatalf("reaper: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", reclaimed)
	}
	if failed != 0 {
		t.Fatalf("expected 0 failed, got %d", failed)
	}

	// Job should be claimable again
	job, _ := jq.Claim(ctx)
	if job == nil {
		t.Fatal("expected job after reclaim")
	}
}
