package domain

import (
	"unicode"
	"unicode/utf8"
)

// QuestionStatus values — the three lifecycle states.
const (
	QuestionStatusModeration = "moderation"
	QuestionStatusVerified   = "verified"
	QuestionStatusError      = "error"
)

// ChoiceLabeling values — how answer ids are rendered for display.
const (
	ChoiceLabelingLetter = "letter"
	ChoiceLabelingNumber = "number"
)

// Question is the canonical, deduplicated knowledge base entry.
type Question struct {
	ID              string
	Number          int
	Text            string
	TextNorm        string
	TextHash        string
	MultipleCorrect bool
	Choices         []string
	Answers         []string // value-only, shuffle-safe
	ChoiceLabeling  string
	Confidence      float64
	Explanation     string
	Embedding       []float32
	Status          string
	VerifiedAt      *string // ISO timestamp, nil if not verified
	VerifiedBy      *string // user UUID, nil if not verified
	Tags            []string
}

// InferChoiceLabeling determines whether answer ids use letters or numbers.
// Defaults to "letter" when no ids are present.
func InferChoiceLabeling(ids []string) string {
	for _, id := range ids {
		if id == "" {
			continue
		}
		r, _ := utf8.DecodeRuneInString(id)
		if unicode.IsDigit(r) {
			return ChoiceLabelingNumber
		}
		return ChoiceLabelingLetter
	}
	return ChoiceLabelingLetter
}
