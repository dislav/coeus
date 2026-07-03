package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// Pipeline orchestrates the per-job extraction → dedup → verify workflow.
type Pipeline struct {
	images    storage.ImageRepo
	questions storage.QuestionRepo
	jobs      storage.JobQueue
	enhancer  ImageEnhancer
	extractor AIExtractor
	verifier  AIVerifier
	embedder  AIEmbedder
	cfg       config.PipelineConfig
	log       *slog.Logger
	backoff   func(attempt int) time.Duration
}

const (
	backoffBase = 1 * time.Second
	backoffCap  = 8 * time.Second
)

// defaultBackoff returns a jittered exponential backoff for the given attempt
// (1-based). Base 1s, factor 2, cap 8s → centers 1s, 2s, 4s, 8s, 8s..., each
// with ±20% uniform jitter. Pure (no I/O); injectable via Pipeline.backoff.
func defaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := backoffBase << (attempt - 1) // base * 2^(attempt-1)
	if d > backoffCap || d < 0 {
		d = backoffCap
	}
	factor := 0.8 + rand.Float64()*0.4 // [0.8, 1.2)
	return time.Duration(float64(d) * factor)
}

func NewPipeline(
	images storage.ImageRepo,
	questions storage.QuestionRepo,
	jobs storage.JobQueue,
	enhancer ImageEnhancer,
	extractor AIExtractor,
	verifier AIVerifier,
	embedder AIEmbedder,
	cfg config.PipelineConfig,
	log *slog.Logger,
) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		images: images, questions: questions, jobs: jobs,
		enhancer: enhancer, extractor: extractor, verifier: verifier, embedder: embedder,
		cfg: cfg, log: log, backoff: defaultBackoff,
	}
}

// Run executes the pipeline for a single job. It owns the job lifecycle:
// calls Complete on success, Fail on infra errors. On shutdown (ctx canceled)
// it returns the error WITHOUT completing or failing — the reaper reclaims.
func (p *Pipeline) Run(ctx context.Context, job *domain.Job) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("pipeline panic: %v", r)
			p.log.Error("pipeline panic", "job", job.ID, "error", retErr)
			_ = p.jobs.Fail(context.Background(), job.ID, retErr.Error())
		}
	}()

	if err := p.execute(ctx, job); err != nil {
		if ctx.Err() == nil {
			_ = p.jobs.Fail(ctx, job.ID, err.Error())
		}
		return err
	}

	if err := p.jobs.Complete(ctx, job.ID); err != nil {
		if ctx.Err() == nil {
			_ = p.jobs.Fail(ctx, job.ID, "complete failed: "+err.Error())
		}
		return err
	}
	return nil
}

// execute runs the 10-step workflow. Returns nil on success (including
// extraction-failure cases where a placeholder question was created).
// Returns non-nil only for infrastructure errors or shutdown.
func (p *Pipeline) execute(ctx context.Context, job *domain.Job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Step 1: Load image bytes
	img, err := p.images.FindByID(ctx, job.ImageID)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}

	// Step 2: Enhance (best-effort — fall back to original on failure)
	decodeBytes := img.Original
	if enhanced, err := p.enhancer.Enhance(ctx, img.Original, img.Mime); err != nil {
		p.log.Warn("enhance failed, using original", "image", img.ID, "error", err)
	} else if len(enhanced) > 0 {
		if err := p.images.UpdateEnhanced(ctx, img.ID, enhanced); err != nil {
			return fmt.Errorf("persist enhanced: %w", err)
		}
		decodeBytes = enhanced
	}

	// Step 3: Extract with retries
	result, err := p.extractWithRetries(ctx, decodeBytes, img.Mime)
	if err != nil {
		if ctx.Err() != nil {
			return err // shutdown — reaper reclaims
		}
		// AI unavailable after all retries — create placeholder, complete job
		p.handleExtractionFailure(ctx, job, img, ExtractionCodeAIUnavailable, err.Error())
		return nil
	}

	// Terminal extraction failure (no questions after all retries)
	if len(result.Questions) == 0 {
		code := "extraction_failed"
		detail := "no questions extracted"
		if result.Error != nil {
			code = result.Error.Code
			detail = result.Error.Detail
		}
		p.handleExtractionFailure(ctx, job, img, code, detail)
		return nil
	}

	// Step 4: Process each extracted question
	//   4a normalize, 4b hash, 4c exact dedup, 4d embed/dedup, 4e create/link
	type newQuestion struct {
		id  string
		ext ExtractedQuestion
	}
	var newQs []newQuestion
	for _, eq := range result.Questions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		norm := domain.NormalizeQuestion(eq.Text)
		hash := domain.HashQuestion(norm)

		// 4c: Exact dedup
		existing, err := p.questions.FindExact(ctx, hash)
		if err != nil {
			return fmt.Errorf("exact dedup: %w", err)
		}
		if existing != nil {
			if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, existing.ID, eq.Number, 0); err != nil {
				return fmt.Errorf("link exact dedup: %w", err)
			}
			continue
		}

		// 4d: Embed (best-effort, skipped if embedder is not configured)
		var embedding []float32
		if p.embedder != nil {
			if emb, err := p.embedder.Embed(ctx, eq.Text); err != nil {
				p.log.Warn("embed failed, skipping semantic dedup", "image", img.ID, "error", err)
			} else {
				embedding = emb
			}
		}

		// 4e: Semantic dedup (only if embedding succeeded)
		if embedding != nil {
			existing, err = p.questions.FindSemantic(ctx, embedding, p.cfg.SemanticThreshold)
			if err != nil {
				return fmt.Errorf("semantic dedup: %w", err)
			}
			if existing != nil {
				if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, existing.ID, eq.Number, 0); err != nil {
					return fmt.Errorf("link semantic dedup: %w", err)
				}
				continue
			}
		}

		// 4f: Create new canonical question
		q := &domain.Question{
			Number:          eq.Number,
			Text:            eq.Text,
			TextNorm:        norm,
			TextHash:        hash,
			Choices:         answerTexts(eq.Choices),
			Answers:         answerTexts(eq.Answers),
			ChoiceLabeling:  domain.InferChoiceLabeling(answerIDs(eq.Choices)),
			Status:          domain.QuestionStatusModeration,
			Embedding:       embedding,
			Tags:            append([]string{"ai-generated"}, eq.Tags...),
		}
		id, err := p.questions.Create(ctx, q)
		if err != nil {
			return fmt.Errorf("create question: %w", err)
		}
		if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, id, eq.Number, 0); err != nil {
			return fmt.Errorf("link new question: %w", err)
		}
		newQs = append(newQs, newQuestion{id: id, ext: eq})
	}

	// Step 5: Verify new questions (best-effort — skip if all deduped)
	if len(newQs) > 0 {
		toVerify := make([]ExtractedQuestion, len(newQs))
		for i, nq := range newQs {
			toVerify[i] = nq.ext
		}
		vr, err := p.verifier.Verify(ctx, toVerify)
		if err != nil {
			p.log.Warn("verify failed, questions stay in moderation", "image", img.ID, "error", err)
		} else {
			for _, vq := range vr.Summary.Results {
				if vq.Index >= 0 && vq.Index < len(newQs) {
					nq := newQs[vq.Index]
					if err := p.questions.UpdateFromVerification(ctx, nq.id, vq.Confidence, vq.Explanation); err != nil {
						p.log.Warn("update from verification", "question", nq.id, "error", err)
					}
				}
			}
			// Persist verification report (raw _verification JSON)
			if err := p.images.UpdateVerificationReport(ctx, img.ID, vr.Report); err != nil {
				p.log.Warn("persist verification report", "image", img.ID, "error", err)
			}
		}
	}

	return nil
}

// extractWithRetries calls Extract up to ExtractMaxAttempts times.
// Retries on unreadable_image, no_questions_found, and transport errors.
// partial_extraction and unknown codes are terminal (no retry).
// Between retried attempts it sleeps per Pipeline.backoff (exponential + jitter
// by default); the sleep honors ctx cancellation.
func (p *Pipeline) extractWithRetries(ctx context.Context, image []byte, mime string) (ExtractResult, error) {
	// result is zero-valued here — callers check err before inspecting result fields.
	var result ExtractResult
	var lastErr error
	for attempt := 1; attempt <= p.cfg.ExtractMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result, lastErr = p.extractor.Extract(ctx, image, mime)
		if lastErr == nil && result.Error == nil {
			return result, nil
		}

		if lastErr == nil && result.Error != nil {
			switch result.Error.Code {
			case ExtractionCodePartial:
				return result, nil // terminal
			case ExtractionCodeUnreadableImage, ExtractionCodeNoQuestions:
				p.log.Warn("extract retryable failure", "attempt", attempt, "code", result.Error.Code)
			default:
				return result, nil // terminal
			}
		} else {
			p.log.Warn("extract error", "attempt", attempt, "error", lastErr)
		}

		// Last attempt — stop without sleeping.
		if attempt == p.cfg.ExtractMaxAttempts {
			break
		}

		select {
		case <-time.After(p.backoff(attempt)):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	if lastErr != nil {
		return result, lastErr
	}
	return result, nil
}

// handleExtractionFailure stores the error on the image and creates an
// error-placeholder question so the client can see what went wrong.
func (p *Pipeline) handleExtractionFailure(ctx context.Context, job *domain.Job, img *domain.Image, code, detail string) {
	errJSON, _ := json.Marshal(map[string]string{"code": code, "detail": detail})
	if err := p.images.UpdateExtractionError(ctx, img.ID, errJSON); err != nil {
		p.log.Error("store extraction error", "image", img.ID, "error", err)
	}

	hash := domain.HashQuestion("error:" + img.ID)
	q := &domain.Question{
		Number:   0,
		Text:     "Extraction failed: " + code,
		TextNorm: "extraction failed " + code,
		TextHash: hash,
		Status:   domain.QuestionStatusError,
		Tags:     []string{"extraction-failed"},
	}
	id, err := p.questions.Create(ctx, q)
	if err != nil {
		p.log.Error("create error placeholder", "image", img.ID, "error", err)
		return
	}
	if err := p.questions.LinkToSession(ctx, job.SessionID, job.ImageID, id, 0, 0); err != nil {
		p.log.Error("link error placeholder", "image", img.ID, "error", err)
	}
}

func answerTexts(answers []Answer) []string {
	out := make([]string, len(answers))
	for i, a := range answers {
		out[i] = a.Text
	}
	return out
}

func answerIDs(answers []Answer) []string {
	out := make([]string, len(answers))
	for i, a := range answers {
		out[i] = a.ID
	}
	return out
}
