package dto

// CreateSessionRequest is the body of POST /api/v1/sessions.
type CreateSessionRequest struct {
	DurationSeconds int `json:"duration_seconds" binding:"required,min=1"`
	BufferSeconds   int `json:"buffer_seconds" binding:"min=0"`
}
