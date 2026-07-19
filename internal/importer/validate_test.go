package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

var importNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func TestBuildQuestion_ValidMultipleChoice(t *testing.T) {
	cols := [5]string{"What is 2+2?", "3;4", "4", "basic math", "arith;easy"}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}

	if q.Type != domain.QuestionTypeMultipleChoice {
		t.Errorf("Type = %q, want multiple_choice", q.Type)
	}
	if q.Number != 0 {
		t.Errorf("Number = %d, want 0", q.Number)
	}
	if q.ChoiceLabeling != domain.ChoiceLabelingLetter {
		t.Errorf("ChoiceLabeling = %q, want letter", q.ChoiceLabeling)
	}
	if q.Confidence != 0.99 {
		t.Errorf("Confidence = %v, want 0.99", q.Confidence)
	}
	if q.Status != domain.QuestionStatusVerified {
		t.Errorf("Status = %q, want verified", q.Status)
	}
	if q.VerifiedAt == nil || *q.VerifiedAt != "2026-07-19T12:00:00Z" {
		t.Errorf("VerifiedAt = %v, want 2026-07-19T12:00:00Z", q.VerifiedAt)
	}
	if q.VerifiedBy == nil || *q.VerifiedBy != "user-1" {
		t.Errorf("VerifiedBy = %v, want user-1", q.VerifiedBy)
	}
	wantNorm := domain.NormalizeQuestion("What is 2+2?")
	if q.TextNorm != wantNorm || q.TextHash != domain.HashQuestion(wantNorm) {
		t.Errorf("norm/hash mismatch: %q / %q", q.TextNorm, q.TextHash)
	}
	if q.Explanation != "basic math" {
		t.Errorf("Explanation = %q", q.Explanation)
	}
	// tags = file tags + "import"
	if len(q.Tags) != 3 || q.Tags[0] != "arith" || q.Tags[1] != "easy" || q.Tags[2] != "import" {
		t.Errorf("Tags = %v, want [arith easy import]", q.Tags)
	}
	if q.Embedding != nil {
		t.Errorf("Embedding = %v, want nil (assigned later by the embed step)", q.Embedding)
	}
}

func TestBuildQuestion_ValidFreeResponse(t *testing.T) {
	cols := [5]string{"Explain entropy.", "", "disorder increases", "", ""}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}
	if q.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response", q.Type)
	}
	if len(q.Choices) != 0 {
		t.Errorf("Choices = %v, want empty", q.Choices)
	}
}

func TestBuildQuestion_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		cols    [5]string
		wantErr string
	}{
		{"empty question", [5]string{"", "a;b", "a", "", ""}, "question text is required"},
		{"no answers", [5]string{"q", "a;b", "", "", ""}, "at least one answer is required"},
		{"one choice", [5]string{"q", "a", "a", "", ""}, "multiple_choice requires at least 2 choices"},
		{"answer not a choice", [5]string{"q", "a;b", "c", "", ""}, "answers must be a subset of choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildQuestion(tt.cols, "user-1", importNow)
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("buildQuestion() err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildQuestion_TooManyTags(t *testing.T) {
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = strings.Repeat("t", i+1)
	}
	cols := [5]string{"q", "a;b", "a", "", strings.Join(tags, ";")}
	_, err := buildQuestion(cols, "user-1", importNow)
	if err == nil || err.Error() != "too many tags (max 20)" {
		t.Errorf("err = %v, want too many tags (max 20)", err)
	}
}

func TestBuildQuestion_Exactly20TagsAllowed(t *testing.T) {
	tags := make([]string, 20)
	for i := range tags {
		tags[i] = strings.Repeat("t", i+1)
	}
	cols := [5]string{"q", "a;b", "a", "", strings.Join(tags, ";")}
	q, err := buildQuestion(cols, "user-1", importNow)
	if err != nil {
		t.Fatalf("buildQuestion: %v", err)
	}
	// 20 file tags + "import" marker = 21 stored (spec §5.5).
	if len(q.Tags) != 21 || q.Tags[20] != "import" {
		t.Errorf("Tags len = %d, want 21 with trailing import marker", len(q.Tags))
	}
}
