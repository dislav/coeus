package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

const (
	workerPollInterval = 5 * time.Second // NOTIFY wait timeout + poll fallback
	workerClaimBackoff = 1 * time.Second // pause after a claim error
)

// WorkerPool runs N workers consuming jobs from the queue.
type WorkerPool struct {
	jobs        storage.JobQueue
	pipeline    *Pipeline
	workersCfg  config.WorkersConfig
	pipelineCfg config.PipelineConfig
	dsn         string
	log         *slog.Logger

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func NewWorkerPool(
	jobs storage.JobQueue,
	pipeline *Pipeline,
	workersCfg config.WorkersConfig,
	pipelineCfg config.PipelineConfig,
	dsn string,
	log *slog.Logger,
) *WorkerPool {
	if log == nil {
		log = slog.Default()
	}
	return &WorkerPool{
		jobs: jobs, pipeline: pipeline,
		workersCfg: workersCfg, pipelineCfg: pipelineCfg,
		dsn: dsn, log: log,
	}
}

// Start launches the reaper and N worker goroutines. It is safe to call once.
func (wp *WorkerPool) Start(ctx context.Context) {
	ctx, wp.cancel = context.WithCancel(ctx)

	// Startup reaper — reclaim any stale jobs from a previous crash
	wp.runReaperOnce(ctx)

	// Reaper ticker goroutine
	wp.wg.Add(1)
	go wp.reaperLoop(ctx)

	// Worker goroutines
	for i := 0; i < wp.workersCfg.Count; i++ {
		wp.wg.Add(1)
		go wp.worker(ctx, i)
	}
}

// Stop signals all goroutines to stop and waits for them to finish.
func (wp *WorkerPool) Stop() {
	if wp.cancel != nil {
		wp.cancel()
	}
	wp.wg.Wait()
}

func (wp *WorkerPool) runReaperOnce(ctx context.Context) {
	reclaimed, failed, err := wp.jobs.ReaperReclaim(ctx, wp.pipelineCfg.StaleThreshold, wp.pipelineCfg.MaxQueueAttempts)
	if err != nil {
		wp.log.Error("startup reaper", "error", err)
		return
	}
	if reclaimed > 0 {
		wp.log.Info("startup reaper reclaimed stale jobs", "count", reclaimed)
	}
	if failed > 0 {
		wp.log.Warn("startup reaper failed exhausted jobs", "count", failed)
	}
}

func (wp *WorkerPool) reaperLoop(ctx context.Context) {
	defer wp.wg.Done()

	// Handle zero interval gracefully
	interval := wp.pipelineCfg.ReaperInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reclaimed, failed, err := wp.jobs.ReaperReclaim(ctx, wp.pipelineCfg.StaleThreshold, wp.pipelineCfg.MaxQueueAttempts)
			if err != nil {
				wp.log.Error("reaper", "error", err)
				continue
			}
			if reclaimed > 0 {
				wp.log.Info("reaper reclaimed stale jobs", "count", reclaimed)
			}
			if failed > 0 {
				wp.log.Warn("reaper failed exhausted jobs", "count", failed)
			}
		}
	}
}

// worker is one consumer goroutine. It has a dedicated pgx.Conn for LISTEN
// jobs_new and claims jobs via the shared pool's Claim method.
func (wp *WorkerPool) worker(ctx context.Context, id int) {
	defer wp.wg.Done()

	conn, err := pgx.Connect(ctx, wp.dsn)
	if err != nil {
		wp.log.Error("worker connect failed", "worker", id, "error", err)
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN jobs_new"); err != nil {
		wp.log.Error("worker listen failed", "worker", id, "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := wp.jobs.Claim(ctx)
		if err != nil {
			wp.log.Error("claim", "worker", id, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(workerClaimBackoff):
			}
			continue
		}
		if job != nil {
			wp.runJob(ctx, job, id)
			continue
		}

		// No job — wait for NOTIFY or poll timeout
		waitCtx, waitCancel := context.WithTimeout(ctx, workerPollInterval)
		_, _ = conn.WaitForNotification(waitCtx)
		waitCancel()
	}
}

func (wp *WorkerPool) runJob(ctx context.Context, job *domain.Job, workerID int) {
	wp.log.Info("processing job", "worker", workerID, "job", job.ID, "image", job.ImageID)
	err := wp.pipeline.Run(ctx, job)
	if err != nil {
		wp.log.Error("pipeline returned error", "worker", workerID, "job", job.ID, "error", err)
		return
	}
	wp.log.Info("job done", "worker", workerID, "job", job.ID)
}
