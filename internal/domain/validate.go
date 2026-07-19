package domain

import "errors"

// ValidateDraft checks the shared structural rules for a question draft
// (spec §5.5): non-empty text, at least one answer for both types, and the
// type-conditional checks. It is called by the Create/Update question
// handlers (which map any error to ErrValidation) and by the bulk importer
// (which surfaces err.Error() as the row-level report message).
//
// The answers >= 1 rule applies to both types deliberately: without it,
// empty answers would vacuously pass the answers-subset-of-choices check
// for multiple_choice.
func ValidateDraft(text string, choices, answers []string, typ string) error {
	if text == "" {
		return errors.New("question text is required")
	}
	if len(answers) < 1 {
		return errors.New("at least one answer is required")
	}
	switch typ {
	case QuestionTypeMultipleChoice:
		if len(choices) < 2 {
			return errors.New("multiple_choice requires at least 2 choices")
		}
		if !answersSubsetOfChoices(answers, choices) {
			return errors.New("answers must be a subset of choices")
		}
	case QuestionTypeFreeResponse:
		if len(choices) != 0 {
			return errors.New("free_response must not have choices")
		}
	}
	return nil
}

// answersSubsetOfChoices reports whether every answer equals some choice using
// exact, case-sensitive Go string equality (no normalization). Duplicates in
// answers are fine as long as each is present in choices (spec §3.2.3).
// Moved here from internal/httpapi/handlers/questions.go; semantics unchanged.
func answersSubsetOfChoices(answers, choices []string) bool {
	for _, a := range answers {
		found := false
		for _, ch := range choices {
			if a == ch {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
