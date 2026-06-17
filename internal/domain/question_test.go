package domain

import "testing"

func TestQuestionStatusConstants(t *testing.T) {
	if QuestionStatusModeration != "moderation" {
		t.Errorf("moderation = %q, want %q", QuestionStatusModeration, "moderation")
	}
	if QuestionStatusVerified != "verified" {
		t.Errorf("verified = %q, want %q", QuestionStatusVerified, "verified")
	}
	if QuestionStatusError != "error" {
		t.Errorf("error = %q, want %q", QuestionStatusError, "error")
	}
}

func TestInferChoiceLabeling(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{"letters", []string{"A", "B", "C"}, "letter"},
		{"numbers", []string{"1", "2", "3"}, "number"},
		{"cyrillic letters", []string{"а", "б", "в"}, "letter"},
		{"empty input", []string{}, "letter"},
		{"nil input", nil, "letter"},
		{"empty string id", []string{""}, "letter"},
		{"leading empty id", []string{"", "1"}, "number"},
		{"mixed content", []string{"A1"}, "letter"},
		{"single letter", []string{"A"}, "letter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferChoiceLabeling(tt.ids)
			if got != tt.want {
				t.Errorf("InferChoiceLabeling(%v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}
