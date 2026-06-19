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

	reclaimed, err := jq.ReaperReclaim(ctx, 0*time.Second)
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
