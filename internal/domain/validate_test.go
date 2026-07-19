package domain

import "testing"

func TestValidateDraft(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		choices []string
		answers []string
		typ     string
		wantErr string // "" means nil error
	}{
		{"valid multiple_choice", "q?", []string{"a", "b"}, []string{"a"}, QuestionTypeMultipleChoice, ""},
		{"valid multiple_choice multi-answer", "q?", []string{"a", "b", "c"}, []string{"a", "c"}, QuestionTypeMultipleChoice, ""},
		{"valid free_response", "q?", nil, []string{"42"}, QuestionTypeFreeResponse, ""},
		{"empty text", "", []string{"a", "b"}, []string{"a"}, QuestionTypeMultipleChoice, "question text is required"},
		{"empty text free_response", "", nil, []string{"42"}, QuestionTypeFreeResponse, "question text is required"},
		{"no answers multiple_choice", "q?", []string{"a", "b"}, nil, QuestionTypeMultipleChoice, "at least one answer is required"},
		{"no answers free_response", "q?", nil, nil, QuestionTypeFreeResponse, "at least one answer is required"},
		{"multiple_choice one choice", "q?", []string{"a"}, []string{"a"}, QuestionTypeMultipleChoice, "multiple_choice requires at least 2 choices"},
		{"multiple_choice zero choices", "q?", nil, []string{"a"}, QuestionTypeMultipleChoice, "multiple_choice requires at least 2 choices"},
		{"answer not in choices", "q?", []string{"a", "b"}, []string{"c"}, QuestionTypeMultipleChoice, "answers must be a subset of choices"},
		{"answer subset is case-sensitive", "q?", []string{"Paris", "London"}, []string{"paris"}, QuestionTypeMultipleChoice, "answers must be a subset of choices"},
		{"free_response with choices", "q?", []string{"a", "b"}, []string{"a"}, QuestionTypeFreeResponse, "free_response must not have choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDraft(tt.text, tt.choices, tt.answers, tt.typ)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDraft() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateDraft() = nil, want error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("ValidateDraft() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}
