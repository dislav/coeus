package dto

// CreateSessionRequest is the body of POST /api/v1/sessions.
type CreateSessionRequest struct {
	DurationSeconds int `json:"duration_seconds" binding:"required,min=1"`
	BufferSeconds   int `json:"buffer_seconds" binding:"min=0"`
}

// CreateQuestionRequest is the body of POST /api/v1/questions (expert-only, spec §3.5).
// `number` is intentionally absent (defaults to 0 in the DB); manual questions are
// free-standing canonical entries, not tied to a session or image.
type CreateQuestionRequest struct {
	Question        string   `json:"question" binding:"required"`
	Choices         []string `json:"choices" binding:"required,min=2"`
	Answers         []string `json:"answers" binding:"required,min=1"`
	MultipleCorrect bool     `json:"multiple_correct"`
	ChoiceLabeling  string   `json:"choice_labeling"`
	Explanation     string   `json:"explanation"`
	Tags            []string `json:"tags"`
	Confidence      *float64 `json:"confidence"`
}
