package domain

import (
	"testing"
)

func TestNormalizeQuestion_LowercasesAndFoldsWhitespace(t *testing.T) {
	got := NormalizeQuestion("  What IS   2+2?  ")
	want := "what is 2+2?"
	if got != want {
		t.Errorf("NormalizeQuestion: got %q want %q", got, want)
	}
}

func TestNormalizeQuestion_Idempotent(t *testing.T) {
	once := NormalizeQuestion("Foo\tBAR\n baz")
	twice := NormalizeQuestion(once)
	if once != twice {
		t.Errorf("NormalizeQuestion not idempotent: %q vs %q", once, twice)
	}
}

func TestNormalizeQuestion_EmptyAndWhitespaceOnly(t *testing.T) {
	if NormalizeQuestion("   ") != "" {
		t.Error("whitespace-only should normalize to empty")
	}
	if NormalizeQuestion("") != "" {
		t.Error("empty should stay empty")
	}
}

func TestHashQuestion_Deterministic(t *testing.T) {
	h1 := HashQuestion("what is 2+2?")
	h2 := HashQuestion("what is 2+2?")
	if h1 != h2 {
		t.Errorf("HashQuestion not deterministic: %q vs %q", h1, h2)
	}
}

func TestHashQuestion_KnownVector(t *testing.T) {
	// sha256("what is 2+2?") — pinned so a future algorithm change is caught.
	// (Computed with: printf '%s' 'what is 2+2?' | shasum -a 256)
	want := "61a3385003d9b9d390dc511fbe3e1eb6bee637ec8c3c9eeb555395cddf838f5e"
	got := HashQuestion("what is 2+2?")
	if len(got) != 64 {
		t.Fatalf("HashQuestion length: got %d want 64", len(got))
	}
	if got != want {
		t.Errorf("HashQuestion known vector: got %q want %q", got, want)
	}
}

func TestQuestionMultipleCorrectDerived(t *testing.T) {
	if (Question{Answers: []string{"A"}}).MultipleCorrect() {
		t.Errorf("single answer: got true, want false")
	}
	if !(Question{Answers: []string{"A", "B"}}).MultipleCorrect() {
		t.Errorf("two answers: got false, want true")
	}
	if (Question{Answers: nil}).MultipleCorrect() {
		t.Errorf("nil answers: got true, want false")
	}
}
