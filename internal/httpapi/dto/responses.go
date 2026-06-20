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
