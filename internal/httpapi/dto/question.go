package dto

import (
	"strconv"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// AnswerRef is a user-facing answer carrying a display id derived at read time.
type AnswerRef struct {
	ID    string `json:"id,omitempty"`
	Value string `json:"value"`
}

// UserQuestionResponse is the user-facing question shape (spec §4.6).
// No explanation; answers carry derived display ids.
type UserQuestionResponse struct {
	ID              string      `json:"id"`
	Number          int         `json:"number"`
	Question        string      `json:"question"`
	Type            string      `json:"type"`
	MultipleCorrect bool        `json:"multiple_correct"`
	Choices         []string    `json:"choices"`
	Answers         []AnswerRef `json:"answers"`
	Status          string      `json:"status"`
	Confidence      float64     `json:"confidence"`
}

// ExpertQuestionResponse is the expert-facing question shape (spec §4.6).
type ExpertQuestionResponse struct {
	ID                    string   `json:"id"`
	Number                int      `json:"number"`
	Question              string   `json:"question"`
	MultipleCorrect       bool     `json:"multiple_correct"`
	Choices               []string `json:"choices"`
	Answers               []string `json:"answers"`
	ChoiceLabeling        string   `json:"choice_labeling"`
	Type                  string   `json:"type"`
	Confidence            float64  `json:"confidence"`
	Explanation           string   `json:"explanation"`
	Tags                  []string `json:"tags"`
	Status                string   `json:"status"`
	ImageID               string   `json:"image_id"`
	HasVerificationReport bool     `json:"has_verification_report"`
	VerifiedAt            *string  `json:"verified_at"`
	VerifiedBy            *string  `json:"verified_by"`
}

// QuestionListResponse wraps a paginated question list.
// Data is []any because user and expert items have different shapes.
type QuestionListResponse struct {
	Data    []any `json:"data"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
}

// DeriveAnswerRefs maps stored value-only answers to user-facing {id,value} pairs
// using the question's choices and choice_labeling (spec §4.6). The id is the
// index of the value's FIRST occurrence in choices, rendered as a letter
// (0->A, 25->Z, 26->AA, ...) for "letter" labeling, or one-based number
// (0->1) for "number" labeling. A value not present in choices gets an empty id.
// First-index wins on duplicate choice texts (v1 edge case).
func DeriveAnswerRefs(choices, answers []string, labeling string) []AnswerRef {
	out := make([]AnswerRef, 0, len(answers))
	for _, v := range answers {
		out = append(out, AnswerRef{ID: idForValue(choices, v, labeling), Value: v})
	}
	return out
}

func idForValue(choices []string, value, labeling string) string {
	idx := -1
	for i, c := range choices {
		if c == value {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	return indexToLabel(idx, labeling)
}

func indexToLabel(i int, labeling string) string {
	if labeling == domain.ChoiceLabelingNumber {
		return strconv.Itoa(i + 1)
	}
	return indexToLetter(i)
}

// indexToLetter renders a 0-based index as a spreadsheet-style column label
// (0->A, 25->Z, 26->AA, ...). Used for "letter" choice labeling.
func indexToLetter(i int) string {
	n := i + 1
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}
