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
		ids  []string
		want string
	}{
		{[]string{"A", "B", "C"}, "letter"},
		{[]string{"1", "2", "3"}, "number"},
		{[]string{"а", "б", "в"}, "letter"},
		{[]string{}, "letter"}, // default when no ids
	}
	for _, tt := range tests {
		got := InferChoiceLabeling(tt.ids)
		if got != tt.want {
			t.Errorf("InferChoiceLabeling(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}
