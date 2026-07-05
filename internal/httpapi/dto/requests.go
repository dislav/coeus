package dto

// CreateSessionRequest is the body of POST /api/v1/sessions.
type CreateSessionRequest struct {
	DurationSeconds int `json:"duration_seconds" binding:"required,min=1"`
	BufferSeconds   int `json:"buffer_seconds" binding:"min=0"`
}

// UpdateQuestionRequest is the body of PUT /api/v1/questions/:id (expert-only,
// full-replace). multiple_correct is intentionally absent — it is derived
// (spec §3.2.2, §3.3).
type UpdateQuestionRequest struct {
	Status      string   `json:"status"      binding:"required,oneof=moderation verified error"`
	Type        string   `json:"type"        binding:"required,oneof=multiple_choice free_response"`
	Choices     []string `json:"choices"     binding:"dive,required"`
	Answers     []string `json:"answers"     binding:"required,min=1,dive,required"`
	Explanation string   `json:"explanation"`
	Tags        []string `json:"tags,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
}

// CreateQuestionRequest is the body of POST /api/v1/questions (expert-only, spec §3.5).
// `number` is intentionally absent (defaults to 0 in the DB); manual questions are
// free-standing canonical entries, not tied to a session or image.
type CreateQuestionRequest struct {
	Question        string   `json:"question" binding:"required"`
	Type            string   `json:"type" binding:"required,oneof=multiple_choice free_response"`
	Choices         []string `json:"choices" binding:"dive,required"`
	Answers         []string `json:"answers" binding:"required,min=1"`
	ChoiceLabeling  string   `json:"choice_labeling"`
	Explanation     string   `json:"explanation"`
	Tags            []string `json:"tags"`
	Confidence      *float64 `json:"confidence"`
}
