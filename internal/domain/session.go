package domain

// SessionStatus values.
const (
	SessionStatusOpen    = "open"
	SessionStatusClosed  = "closed"
	SessionStatusExpired = "expired"
)

// JobStatus values.
const (
	JobStatusPending    = "pending"
	JobStatusProcessing = "processing"
	JobStatusDone       = "done"
	JobStatusFailed     = "failed"
)

// Session is a user's timed test window.
type Session struct {
	ID              string
	UserID          string
	DurationSeconds int
	BufferSeconds   int
	StartedAt       string // ISO timestamp
	ExpiresAt       string // ISO timestamp
	Status          string
}

// Image is an uploaded exam photo.
type Image struct {
	ID                 string
	SessionID          string
	Original           []byte // nil after post-review cleanup
	Enhanced           []byte // nil after post-review cleanup
	Mime               string
	Width              int
	Height             int
	VerificationReport []byte // raw JSON, nil if none
	ExtractionError    []byte // raw JSON, nil if none
	CreatedAt          string
}

// Job is a queued pipeline task.
type Job struct {
	ID         string
	ImageID    string
	SessionID  string
	Status     string
	Attempts   int
	LastError  string
	QueuedAt   string
	StartedAt  string
	FinishedAt string
}
