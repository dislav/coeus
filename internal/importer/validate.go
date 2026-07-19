package importer

import (
	"errors"
	"fmt"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// maxFileTags caps tags supplied by the file. The "import" marker is appended
// AFTER this check, so a stored row may carry 21 tags total — mirroring how
// Create appends "manual-entry" after binding (spec §5.5).
const maxFileTags = 20

// importTag marks every imported question (spec §5.6).
const importTag = "import"

// importConfidence is constant: the file has no confidence column, and 0.99
// fits the numeric(3,2) column (spec §5.6).
const importConfidence = 0.99

// buildQuestion validates one shape-normalized row and, on success, builds
// the canonical verified question with the §5.6 constant field values.
// Embedding stays nil — the Service's embed step assigns it later.
func buildQuestion(cols [5]string, userID string, now time.Time) (*domain.Question, error) {
	text := cols[0]
	choices := splitMulti(cols[1])
	answers := splitMulti(cols[2])
	explanation := cols[3]
	tags := splitMulti(cols[4])

	// No type column in the file: empty choices => free_response.
	typ := domain.InferQuestionType(choices)
	if err := domain.ValidateDraft(text, choices, answers, typ); err != nil {
		return nil, err
	}

	if len(tags) > maxFileTags {
		return nil, fmt.Errorf("too many tags (max %d)", maxFileTags)
	}
	for _, tg := range tags {
		if tg == "" { // defensive: splitMulti already drops empty items
			return nil, errors.New("empty tag")
		}
	}

	norm := domain.NormalizeQuestion(text)
	verifiedAt := now.UTC().Format(time.RFC3339)

	// tags = file tags + ["import"]; copy to avoid aliasing (Create precedent).
	fullTags := make([]string, 0, len(tags)+1)
	fullTags = append(fullTags, tags...)
	fullTags = append(fullTags, importTag)

	return &domain.Question{
		Number:         0,
		Text:           text,
		TextNorm:       norm,
		TextHash:       domain.HashQuestion(norm),
		Choices:        choices,
		Answers:        answers,
		ChoiceLabeling: domain.ChoiceLabelingLetter,
		Type:           typ,
		Confidence:     importConfidence,
		Explanation:    explanation,
		Status:         domain.QuestionStatusVerified,
		VerifiedAt:     &verifiedAt,
		VerifiedBy:     &userID,
		Tags:           fullTags,
	}, nil
}
