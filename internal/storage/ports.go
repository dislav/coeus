package storage

import (
	"context"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// User is the storage-level user record (includes password hash for auth).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    string
}

// QuestionWithSession is a question joined with its session_questions link.
type QuestionWithSession struct {
	*domain.Question
	SessionID           string
	ImageID             string
	ExtractedNumber     int
	ExtractedConfidence float64
}

// QuestionExpertView is a question joined with a single representative image link,
// for the expert moderation UI. The ImageID is the first session_questions row
// by id (deterministic representative); HasVerificationReport reflects that image.
type QuestionExpertView struct {
	*domain.Question
	ImageID               string
	HasVerificationReport bool
}

// UserRepo manages user records.
type UserRepo interface {
	Create(ctx context.Context, email, passwordHash, role string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByID(ctx context.Context, id string) (*User, error)
}

// SessionRepo manages session records.
type SessionRepo interface {
	Create(ctx context.Context, userID string, durationSec, bufferSec int) (*domain.Session, error)
	FindByID(ctx context.Context, id string) (*domain.Session, error)
	ListByUser(ctx context.Context, userID string, limit, offset int) ([]*domain.Session, error)
	Close(ctx context.Context, id string) error
}

// ImageRepo manages uploaded image records.
type ImageRepo interface {
	Create(ctx context.Context, sessionID string, original []byte, mime string, width, height int) (string, error)
	FindByID(ctx context.Context, id string) (*domain.Image, error)
	ListBySession(ctx context.Context, sessionID string) ([]*domain.Image, error)
	UpdateEnhanced(ctx context.Context, id string, enhanced []byte) error
	UpdateVerificationReport(ctx context.Context, id string, report []byte) error
	UpdateExtractionError(ctx context.Context, id string, errJSON []byte) error
	CleanBytes(ctx context.Context, id string) error
	CountBySession(ctx context.Context, sessionID string) (int, error)
}

// QuestionRepo manages the canonical question knowledge base.
type QuestionRepo interface {
	Create(ctx context.Context, q *domain.Question) (string, error)
	FindByID(ctx context.Context, id string) (*domain.Question, error)
	FindExact(ctx context.Context, hash string) (*domain.Question, error)
	FindSemantic(ctx context.Context, embedding []float32, threshold float64) (*domain.Question, error)
	UpdateFromVerification(ctx context.Context, id string, confidence float64, explanation string) error
	ListForUser(ctx context.Context, sessionID string, statusFilter string, limit, offset int) ([]*QuestionWithSession, error)
	ListForModeration(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*domain.Question, error)
	UpdateByExpert(ctx context.Context, id string, answers []string, choices []string, explanation string, confidence float64, tags []string, expertID string) error
	// Read-side projections for the HTTP surface.
	FindExpertByID(ctx context.Context, id string) (*QuestionExpertView, error)
	ListForModerationExpert(ctx context.Context, statusFilter, tagFilter string, limit, offset int) ([]*QuestionExpertView, error)
	FindForUserByID(ctx context.Context, questionID, userID string) (*QuestionWithSession, error)
	CountUnresolvedForImage(ctx context.Context, imageID string) (int, error)
	LinkToSession(ctx context.Context, sessionID, imageID, questionID string, number int, confidence float64) error
}

// JobQueue manages the Postgres-backed job queue.
type JobQueue interface {
	Enqueue(ctx context.Context, imageID, sessionID string) (string, error)
	Claim(ctx context.Context) (*domain.Job, error)
	Complete(ctx context.Context, id string) error
	Fail(ctx context.Context, id, errMsg string) error
	ReaperReclaim(ctx context.Context, staleThreshold time.Duration, maxAttempts int) (reclaimed int, failed int, err error)
	FindByImageID(ctx context.Context, imageID string) (*domain.Job, error)
	FindJobStatusesBySession(ctx context.Context, sessionID string) (map[string]string, error)
}
