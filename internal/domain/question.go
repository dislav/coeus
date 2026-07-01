package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// QuestionUpdate is the expert-authored full replacement of a question's
// editable fields (spec §3.2.5). Server-managed fields (id, number, text*,
// embedding, verified_at, verified_by) are NOT carried here. Status drives the
// verified_at/verified_by invariant enforced in the repo.
type QuestionUpdate struct {
	Status      string
	Choices     []string
	Answers     []string
	Explanation string
	Tags        []string
	Confidence  float64
}

// Question is the canonical, deduplicated knowledge base entry.
type Question struct {
	ID              string
	Number          int
	Text            string
	TextNorm        string
	TextHash        string
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

// MultipleCorrect reports whether the question has more than one correct answer.
// Derived from len(Answers); there is no stored column (spec §3.3).
func (q Question) MultipleCorrect() bool { return len(q.Answers) > 1 }

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

// NormalizeQuestion folds a question string to a canonical form for dedup:
// trim, lowercase, collapse all runs of whitespace to single spaces.
// It is byte-for-byte identical to the former pipeline.normalizeQuestion —
// do NOT add punctuation stripping, it would invalidate stored hashes.
func NormalizeQuestion(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// HashQuestion returns the lowercase hex sha256 of the (already-normalized)
// question text. Used for exact-hash dedup against the questions.question_hash column.
func HashQuestion(norm string) string {
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}
