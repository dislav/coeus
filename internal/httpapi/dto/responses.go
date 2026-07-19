package dto

// SessionResponse is the minimal session shape returned in lists and on create.
type SessionResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
	Status    string `json:"status"`
}

// SessionDetailResponse is the enriched shape for GET /:id.
type SessionDetailResponse struct {
	SessionResponse
	DurationSeconds int    `json:"duration_seconds"`
	BufferSeconds   int    `json:"buffer_seconds"`
	StartedAt       string `json:"started_at"`
	ImageCount      int    `json:"image_count"`
}

// SessionListResponse wraps a paginated session list.
type SessionListResponse struct {
	Data    []SessionResponse `json:"data"`
	Page    int               `json:"page"`
	PerPage int               `json:"per_page"`
}

// ImageUploadResponse is returned on POST /:id/images (202 Accepted).
type ImageUploadResponse struct {
	ImageID string `json:"image_id"`
	JobID   string `json:"job_id"`
}

// ImageResponse is one image in a session's image list.
type ImageResponse struct {
	ID        string `json:"id"`
	Mime      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	JobStatus string `json:"job_status"`
	CreatedAt string `json:"created_at"`
}

// ImageListResponse wraps a list of images.
type ImageListResponse struct {
	Data []ImageResponse `json:"data"`
}

// UserResponse is the shared user shape returned by /profile and /users endpoints.
// It NEVER exposes password_hash or token_version (spec §Shared UserResponse).
type UserResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

// UserListResponse wraps a paginated user list (no total field — matches
// QuestionListResponse / SessionListResponse precedent).
type UserListResponse struct {
	Data    []UserResponse `json:"data"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
}

// ResetPasswordResponse returns the generated plaintext exactly once.
type ResetPasswordResponse struct {
	Password string `json:"password"`
}

// ImportRowError is one failed row in an import report: 1-based file row
// number plus the validation/upsert message.
type ImportRowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportReportResponse is returned on POST /api/v1/questions/upload (200 OK)
// whenever the file itself parses — even if every row failed (spec §4.2).
type ImportReportResponse struct {
	TotalRows int              `json:"total_rows"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Failed    int              `json:"failed"`
	Errors    []ImportRowError `json:"errors"`
}
