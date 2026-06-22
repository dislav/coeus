package dto

import (
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

func TestDeriveAnswerRefs_LetterLabeling(t *testing.T) {
	choices := []string{"Fe(OH)2", "Cs2O", "HBr", "Na2CO3", "H2SO4"}
	answers := []string{"HBr", "H2SO4"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{
		{ID: "C", Value: "HBr"},   // index 2 -> C
		{ID: "E", Value: "H2SO4"}, // index 4 -> E
	}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_NumberLabeling(t *testing.T) {
	choices := []string{"A", "B", "C", "D"}
	answers := []string{"A", "C"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingNumber)
	want := []AnswerRef{
		{ID: "1", Value: "A"}, // index 0 -> 1
		{ID: "3", Value: "C"}, // index 2 -> 3
	}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_DuplicateChoiceFirstIndexWins(t *testing.T) {
	choices := []string{"X", "X", "Y"}
	answers := []string{"X"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "A", Value: "X"}}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_MissingValueEmptyID(t *testing.T) {
	choices := []string{"A", "B"}
	answers := []string{"Z"}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "", Value: "Z"}}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_EmptyChoiceText(t *testing.T) {
	choices := []string{"", "X"}
	answers := []string{""}
	got := DeriveAnswerRefs(choices, answers, domain.ChoiceLabelingLetter)
	want := []AnswerRef{{ID: "A", Value: ""}}
	assertRefsEqual(t, got, want)
}

func TestDeriveAnswerRefs_Empty(t *testing.T) {
	got := DeriveAnswerRefs([]string{"A"}, nil, domain.ChoiceLabelingLetter)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

func TestIndexToLetter_SpreadsheetStyle(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for i, want := range cases {
		if got := indexToLetter(i); got != want {
			t.Errorf("indexToLetter(%d) = %q, want %q", i, got, want)
		}
	}
}

func assertRefsEqual(t *testing.T, got, want []AnswerRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %#v want %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %#v want %#v", i, got[i], want[i])
		}
	}
}
