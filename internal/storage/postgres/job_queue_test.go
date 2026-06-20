package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestJobQueue_EnqueueAndClaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "job@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jobID, err := jq.Enqueue(ctx, imgID, sess.ID)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job, err := jq.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if job == nil || job.ID != jobID {
		t.Fatalf("expected job %s, got %v", jobID, job)
	}
	if job.Status != domain.JobStatusProcessing {
		t.Errorf("status = %q, want processing", job.Status)
	}
}

func TestJobQueue_ClaimEmpty(t *testing.T) {
	pool := setupTestDB(t)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	job, err := jq.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if job != nil {
		t.Error("expected nil on empty queue")
	}
}

func TestJobQueue_ConcurrentClaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "conc@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)

	for i := 0; i < 10; i++ {
		imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)
		jq.Enqueue(ctx, imgID, sess.ID)
	}

	var claimed int64
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := jq.Claim(context.Background())
				if err != nil || job == nil {
					return
				}
				atomic.AddInt64(&claimed, 1)
			}
		}()
	}
	wg.Wait()

	if claimed != 10 {
		t.Errorf("claimed = %d, want 10 (each job claimed exactly once)", claimed)
	}
}

func TestJobQueue_ReaperReclaim(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "reaper@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jq.Enqueue(ctx, imgID, sess.ID)
	jq.Claim(ctx) // mark as processing

	reclaimed, _, err := jq.ReaperReclaim(ctx, 0*time.Second, 5)
	if err != nil {
		t.Fatalf("ReaperReclaim: %v", err)
	}
	if reclaimed != 1 {
		t.Errorf("reclaimed = %d, want 1", reclaimed)
	}

	job, err := jq.Claim(ctx)
	if err != nil || job == nil {
		t.Fatal("expected job after reclaim")
	}
}

func TestJobQueue_FindByImageID(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "findjb@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jobID, _ := jq.Enqueue(ctx, imgID, sess.ID)

	job, err := jq.FindByImageID(ctx, imgID)
	if err != nil {
		t.Fatalf("FindByImageID: %v", err)
	}
	if job == nil || job.ID != jobID {
		t.Fatalf("expected job %s, got %v", jobID, job)
	}
	if job.Status != domain.JobStatusPending {
		t.Errorf("status = %q, want pending", job.Status)
	}

	// Not found returns nil, nil
	job2, err := jq.FindByImageID(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("FindByImageID miss: %v", err)
	}
	if job2 != nil {
		t.Error("expected nil for non-existent image")
	}
}

func TestJobQueue_ReaperReclaimMaxAttempts(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	jq := NewJobQueue(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "reaper-max@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	jobID, _ := jq.Enqueue(ctx, imgID, sess.ID)

	// Manually push the job to processing with attempts=3 and a stale started_at
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status='processing', attempts=3,
			started_at = now() - interval '1 hour'
		WHERE id = $1`, jobID)
	if err != nil {
		t.Fatalf("seed stale job: %v", err)
	}

	reclaimed, failed, err := jq.ReaperReclaim(ctx, time.Minute, 3)
	if err != nil {
		t.Fatalf("ReaperReclaim: %v", err)
	}
	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 (attempts exhausted)", reclaimed)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}

	// Job should now be 'failed'
	job, _ := jq.FindByImageID(ctx, imgID)
	if job == nil || job.Status != domain.JobStatusFailed {
		t.Errorf("job status = %v, want failed", job)
	}
}
