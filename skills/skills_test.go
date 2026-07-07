package skills

import (
	"strings"
	"testing"
)

// TestExtractPromptDocumentsImageContext guards that the extractor skill tells
// the vision model to transcribe graph/figure data into image_context. If this
// fails, the skill edit was lost or reverted — the downstream verifier would
// again receive no visual data for graph questions.
func TestExtractPromptDocumentsImageContext(t *testing.T) {
	if !strings.Contains(ExtractPrompt, "image_context") {
		t.Fatal("ExtractPrompt must document image_context so the vision model transcribes graph/figure data for the text-only verifier")
	}
}

// TestVerifyPromptDocumentsImageContext guards that the verifier skill tells the
// model to consume image_context when solving graph/figure questions. If this
// fails, the verifier would again ignore transcribed visual data.
func TestVerifyPromptDocumentsImageContext(t *testing.T) {
	if !strings.Contains(VerifyPrompt, "image_context") {
		t.Fatal("VerifyPrompt must document image_context so the verifier uses the extractor's transcribed visual data")
	}
}
