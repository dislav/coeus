package pipeline

import (
	"context"
	"encoding/json"
)

// ImageEnhancer improves image quality before extraction (upscaler, denoiser, etc.).
// Implementations live in Plan 3 (real AI client); tests use fakes.
type ImageEnhancer interface {
	Enhance(ctx context.Context, original []byte, mime string) ([]byte, error)
}

// AIExtractor reads questions and answers from an enhanced exam image.
// Returns an ExtractResult — never a raw error for content-level failures
// (those are reported via ExtractResult.Error). Transport-level failures
// return a normal Go error.
type AIExtractor interface {
	Extract(ctx context.Context, image []byte, mime string) (ExtractResult, error)
}

// AIVerifier answers extracted questions using a second, reasoning model. It is
// the authoritative answerer in the pipeline: the extractor only transcribes the
// image, while the verifier solves each question and returns the canonical
// answer. Failures are best-effort: the caller proceeds with unverified questions.
type AIVerifier interface {
	Verify(ctx context.Context, questions []ExtractedQuestion) (VerifyResult, error)
}

// AIEmbedder produces a vector embedding of question text for semantic dedup.
type AIEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Answer is a single choice or correct answer extracted from the image.
type Answer struct {
	ID   string // label like "A", "1", "i" — used for choice labeling inference
	Text string // the answer text — stored value-only in the DB
}

// ExtractedQuestion is one question parsed from the exam image.
type ExtractedQuestion struct {
	Number          int
	Text            string
	Choices         []Answer
	Answers         []Answer
	Confidence      float64  `json:"confidence,omitempty"`
	Tags            []string `json:"tags,omitempty"` // subject tags from Kimi
}

// ExtractionErrorCode identifies why extraction failed or was partial.
const (
	ExtractionCodeUnreadableImage = "unreadable_image" // image too blurry, corrupted
	ExtractionCodeNoQuestions     = "no_questions_found"
	ExtractionCodePartial         = "partial_extraction" // some questions parsed, some not
	ExtractionCodeAIUnavailable   = "ai_unavailable"     // transport / service error
)

// ExtractionError is set on ExtractResult when the AI responded but could not
// fully process the image. A nil ExtractResult.Error means full success.
type ExtractionError struct {
	Code   string
	Detail string
}

// ExtractResult is the output of AIExtractor.Extract.
type ExtractResult struct {
	Questions []ExtractedQuestion
	Error     *ExtractionError // non-nil for partial or terminal failures
}

// VerifiedQuestion is the verification result for one question in the input slice.
// Index matches the position in the []ExtractedQuestion passed to Verify.
//
// Answers holds the verifier's authoritative answer(s). It is nil/empty when the
// verifier could not solve the question. The pipeline decides whether to persist
// these or preserve the extractor's stored answer — see resolveVerifiedAnswers.
type VerifiedQuestion struct {
	Index       int
	Answers     []Answer
	Confidence  float64
	Explanation string
}

// VerificationSummary aggregates per-question verification results.
type VerificationSummary struct {
	Results []VerifiedQuestion
}

// VerifyResult wraps the verification summary and the raw _verification JSON
// returned by the verifier (persisted to images.verification_report).
type VerifyResult struct {
	Summary VerificationSummary
	Report  json.RawMessage `json:"_verification"` // raw JSON for images.verification_report
}
