package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// quietLogger discards all output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Fakes ---

type fakeEnhancer struct {
	enhanced []byte
	err      error
	called   bool
}

func (f *fakeEnhancer) Enhance(_ context.Context, data []byte, _ string) ([]byte, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	if f.enhanced != nil {
		return f.enhanced, nil
	}
	return data, nil
}

type fakeExtractor struct {
	result ExtractResult
	err    error
	calls  int
}

func (f *fakeExtractor) Extract(ctx context.Context, _ []byte, _ string) (ExtractResult, error) {
	f.calls++
	if ctx.Err() != nil {
		return ExtractResult{}, ctx.Err()
	}
	return f.result, f.err
}

type fakeVerifier struct {
	result VerifyResult
	err    error
	called bool
}

func (f *fakeVerifier) Verify(_ context.Context, _ []ExtractedQuestion) (VerifyResult, error) {
	f.called = true
	return f.result, f.err
}

type fakeEmbedder struct {
	embedding []float32
	err       error
	called    bool
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	f.called = true
	return f.embedding, f.err
}

// --- Fake repos (full interface implementations) ---

type fakeImageRepo struct {
	images              map[string]*domain.Image
	enhanced            map[string][]byte
	extractErr          map[string][]byte
	verificationReports map[string][]byte // imageID -> report bytes
	nextID              int
}

func newFakeImageRepo(img *domain.Image) *fakeImageRepo {
	r := &fakeImageRepo{
		images:              make(map[string]*domain.Image),
		enhanced:            make(map[string][]byte),
		extractErr:          make(map[string][]byte),
		verificationReports: make(map[string][]byte),
	}
	if img != nil {
		r.images[img.ID] = img
	}
	return r
}

func (r *fakeImageRepo) Create(_ context.Context, sessionID string, original []byte, mime string, w, h int) (string, error) {
	r.nextID++
	id := fmt.Sprintf("img-%d", r.nextID)
	r.images[id] = &domain.Image{ID: id, SessionID: sessionID, Original: original, Mime: mime, Width: w, Height: h}
	return id, nil
}
func (r *fakeImageRepo) FindByID(_ context.Context, id string) (*domain.Image, error) {
	img, ok := r.images[id]
	if !ok {
		return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
	}
	return img, nil
}
func (r *fakeImageRepo) ListBySession(_ context.Context, sessionID string) ([]*domain.Image, error) {
	var out []*domain.Image
	for _, img := range r.images {
		if img.SessionID == sessionID {
			out = append(out, img)
		}
	}
	return out, nil
}
func (r *fakeImageRepo) UpdateEnhanced(_ context.Context, id string, enhanced []byte) error {
	r.enhanced[id] = enhanced
	if img, ok := r.images[id]; ok {
		img.Enhanced = enhanced
	}
	return nil
}
func (r *fakeImageRepo) UpdateVerificationReport(_ context.Context, id string, report []byte) error {
	r.verificationReports[id] = report
	return nil
}
func (r *fakeImageRepo) UpdateExtractionError(_ context.Context, id string, errJSON []byte) error {
	r.extractErr[id] = errJSON
	return nil
}
func (r *fakeImageRepo) CleanBytes(context.Context, string) error { return nil }
func (r *fakeImageRepo) CountBySession(_ context.Context, sessionID string) (int, error) {
	c := 0
	for _, img := range r.images {
		if img.SessionID == sessionID {
			c++
		}
	}
	return c, nil
}

type fakeQuestionRepo struct {
	byHash          map[string]*domain.Question // hash → question
	semantMatch     *domain.Question            // returned by FindSemantic if non-nil
	created         []*domain.Question
	updatedFromVer  []struct{ id string; conf float64; expl string }
	links           []struct{ sessionID, imageID, questionID string; num int; conf float64 }
	findExactCalls  int
	findSemantCalls int
	nextID          int
}

func newFakeQuestionRepo() *fakeQuestionRepo {
	return &fakeQuestionRepo{byHash: make(map[string]*domain.Question)}
}

func (r *fakeQuestionRepo) Create(_ context.Context, q *domain.Question) (string, error) {
	r.nextID++
	id := fmt.Sprintf("q-%d", r.nextID)
	q.ID = id
	r.created = append(r.created, q)
	r.byHash[q.TextHash] = q
	return id, nil
}
func (r *fakeQuestionRepo) FindByID(_ context.Context, id string) (*domain.Question, error) {
	for _, q := range r.byHash {
		if q.ID == id {
			return q, nil
		}
	}
	return nil, fmt.Errorf("find: %w", domain.ErrNotFound)
}
func (r *fakeQuestionRepo) FindExact(_ context.Context, hash string) (*domain.Question, error) {
	r.findExactCalls++
	return r.byHash[hash], nil
}
func (r *fakeQuestionRepo) FindSemantic(_ context.Context, _ []float32, _ float64) (*domain.Question, error) {
	r.findSemantCalls++
	return r.semantMatch, nil
}
func (r *fakeQuestionRepo) UpdateFromVerification(_ context.Context, id string, c float64, e string) error {
	r.updatedFromVer = append(r.updatedFromVer, struct{ id string; conf float64; expl string }{id, c, e})
	return nil
}
func (r *fakeQuestionRepo) ListForUser(context.Context, string, string, int, int) ([]*storage.QuestionWithSession, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) UpdateByExpert(context.Context, string, []string, []string, string, float64, []string, string) error {
	return nil
}
func (r *fakeQuestionRepo) FindExpertByID(context.Context, string) (*storage.QuestionExpertView, error) {
	return nil, domain.ErrNotFound
}
func (r *fakeQuestionRepo) ListForModerationExpert(context.Context, string, string, int, int) ([]*storage.QuestionExpertView, error) {
	return nil, nil
}
func (r *fakeQuestionRepo) FindForUserByID(context.Context, string, string) (*storage.QuestionWithSession, error) {
	return nil, domain.ErrNotFound
}
func (r *fakeQuestionRepo) CountUnresolvedForImage(context.Context, string) (int, error) { return 0, nil }
func (r *fakeQuestionRepo) LinkToSession(_ context.Context, sessionID, imageID, questionID string, num int, conf float64) error {
	r.links = append(r.links, struct{ sessionID, imageID, questionID string; num int; conf float64 }{sessionID, imageID, questionID, num, conf})
	return nil
}

type fakeJobQueue struct {
	completed []string
	failed    []struct{ id, msg string }
}

func newFakeJobQueue() *fakeJobQueue { return &fakeJobQueue{} }

func (q *fakeJobQueue) Enqueue(context.Context, string, string) (string, error) { return "job-1", nil }
func (q *fakeJobQueue) Claim(context.Context) (*domain.Job, error)              { return nil, nil }
func (q *fakeJobQueue) Complete(_ context.Context, id string) error {
	q.completed = append(q.completed, id)
	return nil
}
func (q *fakeJobQueue) Fail(_ context.Context, id, msg string) error {
	q.failed = append(q.failed, struct{ id, msg string }{id, msg})
	return nil
}
func (q *fakeJobQueue) ReaperReclaim(context.Context, time.Duration, int) (reclaimed int, failed int, err error) {
	return 0, 0, nil
}
func (q *fakeJobQueue) FindByImageID(context.Context, string) (*domain.Job, error) {
	return nil, nil
}
func (q *fakeJobQueue) FindJobStatusesBySession(context.Context, string) (map[string]string, error) {
	return nil, nil
}

// --- Helpers ---

func testPipeline(enh ImageEnhancer, ext AIExtractor, ver AIVerifier, emb AIEmbedder) (*Pipeline, *fakeImageRepo, *fakeQuestionRepo, *fakeJobQueue) {
	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	qRepo := newFakeQuestionRepo()
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, enh, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())
	return p, imgRepo, qRepo, jq
}

func sampleQuestions() []ExtractedQuestion {
	return []ExtractedQuestion{
		{Number: 1, Text: "What is 2+2?", Choices: []Answer{{"A", "3"}, {"B", "4"}}, Answers: []Answer{{"B", "4"}}},
		{Number: 2, Text: "Capital of France?", Choices: []Answer{{"A", "London"}, {"B", "Paris"}}, Answers: []Answer{{"B", "Paris"}}},
	}
}

func job() *domain.Job {
	return &domain.Job{ID: "job-1", ImageID: "img-1", SessionID: "sess-1", Status: domain.JobStatusProcessing}
}

// --- Test cases ---

func TestPipeline_HappyPath(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()}}
	ver := &fakeVerifier{result: VerifyResult{
		Summary: VerificationSummary{Results: []VerifiedQuestion{
			{Index: 0, Confidence: 0.95, Explanation: "correct"},
			{Index: 1, Confidence: 0.90, Explanation: "correct"},
		}},
		Report: json.RawMessage(`{"score":0.9}`),
	}}
	emb := &fakeEmbedder{embedding: []float32{0.1, 0.2}}
	p, imgRepo, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, emb)

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(jq.completed) != 1 {
		t.Errorf("expected 1 complete, got %d", len(jq.completed))
	}
	if len(qRepo.created) != 2 {
		t.Errorf("expected 2 created, got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 2 {
		t.Errorf("expected 2 links, got %d", len(qRepo.links))
	}
	if len(qRepo.updatedFromVer) != 2 {
		t.Errorf("expected 2 verifications, got %d", len(qRepo.updatedFromVer))
	}
	if !ver.called {
		t.Error("verifier not called")
	}
	if imgRepo.verificationReports["img-1"] == nil {
		t.Error("verification report should be persisted for img-1")
	}
}

func TestPipeline_ExactDedupSkipsVerify(t *testing.T) {
	// Pre-seed an exact-dedup match for question 1's hash
	qRepo := newFakeQuestionRepo()
	norm := domain.NormalizeQuestion("What is 2+2?")
	hash := domain.HashQuestion(norm)
	existing := &domain.Question{ID: "existing-1", TextHash: hash}
	qRepo.byHash[hash] = existing

	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}} // only q1
	ver := &fakeVerifier{}
	emb := &fakeEmbedder{embedding: []float32{0.1}}

	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, &fakeEnhancer{}, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(qRepo.created) != 0 {
		t.Errorf("expected 0 created (all deduped), got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 1 {
		t.Errorf("expected 1 link to existing, got %d", len(qRepo.links))
	}
	if ver.called {
		t.Error("verifier should NOT be called when all deduped")
	}
}

func TestPipeline_SemanticDedup(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	ver := &fakeVerifier{}
	emb := &fakeEmbedder{embedding: []float32{0.1}}

	qRepo := newFakeQuestionRepo()
	qRepo.semantMatch = &domain.Question{ID: "semant-existing"} // semantic dedup returns this
	imgRepo := newFakeImageRepo(&domain.Image{ID: "img-1", SessionID: "sess-1", Original: []byte("raw"), Mime: "image/jpeg"})
	jq := newFakeJobQueue()
	p := NewPipeline(imgRepo, qRepo, jq, &fakeEnhancer{}, ext, ver, emb,
		config.PipelineConfig{ExtractMaxAttempts: 3, SemanticThreshold: 0.92}, quietLogger())

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if qRepo.findSemantCalls == 0 {
		t.Error("FindSemantic should have been called")
	}
	if len(qRepo.created) != 0 {
		t.Errorf("expected 0 created (semantic match links existing), got %d", len(qRepo.created))
	}
	if len(qRepo.links) != 1 {
		t.Errorf("expected 1 link to semantic match, got %d", len(qRepo.links))
	}
	if qRepo.links[0].questionID != "semant-existing" {
		t.Errorf("should link to semant-existing, got %s", qRepo.links[0].questionID)
	}
	if ver.called {
		t.Error("verifier should NOT be called when all deduped")
	}
}

func TestPipeline_UnreadableThreeAttemptsPlaceholder(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Error: &ExtractionError{Code: ExtractionCodeUnreadableImage, Detail: "blurry"}}}
	p, imgRepo, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ext.calls != 3 {
		t.Errorf("expected 3 extract attempts, got %d", ext.calls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("expected 1 error placeholder, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Status != domain.QuestionStatusError {
		t.Errorf("placeholder status = %q, want error", qRepo.created[0].Status)
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete (placeholder captures error), got %d completes", len(jq.completed))
	}
	if imgRepo.extractErr["img-1"] == nil {
		t.Error("extraction error should be stored on image")
	}
}

func TestPipeline_PartialExtractionProceeds(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{
		Questions: sampleQuestions()[:1],
		Error:     &ExtractionError{Code: ExtractionCodePartial, Detail: "1 of 2 parsed"},
	}}
	ver := &fakeVerifier{result: VerifyResult{Summary: VerificationSummary{Results: []VerifiedQuestion{{Index: 0, Confidence: 0.8}}}}}
	p, _, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ext.calls != 1 {
		t.Errorf("partial should not retry, got %d calls", ext.calls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("expected 1 question from partial, got %d", len(qRepo.created))
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete, got %d", len(jq.completed))
	}
}

func TestPipeline_VerifierFailureBestEffort(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	ver := &fakeVerifier{err: errors.New("deepseek down")}
	p, _, qRepo, jq := testPipeline(&fakeEnhancer{}, ext, ver, &fakeEmbedder{embedding: []float32{0.1}})

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run should not fail on verifier error, got: %v", err)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("question should be created despite verify failure, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Status != domain.QuestionStatusModeration {
		t.Errorf("question should stay moderation, got %q", qRepo.created[0].Status)
	}
	if len(qRepo.updatedFromVer) != 0 {
		t.Errorf("no UpdateFromVerification should be called, got %d", len(qRepo.updatedFromVer))
	}
	if len(jq.completed) != 1 {
		t.Errorf("job should complete, got %d", len(jq.completed))
	}
}

func TestPipeline_ShutdownDuringExtract(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()}}
	p, _, _, jq := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := p.Run(ctx, job())
	if err == nil {
		t.Fatal("expected error on shutdown, got nil")
	}
	if len(jq.completed) != 0 {
		t.Errorf("job should NOT be completed on shutdown, got %d", len(jq.completed))
	}
	if len(jq.failed) != 0 {
		t.Errorf("job should NOT be failed on shutdown (reaper reclaims), got %d", len(jq.failed))
	}
}

func TestPipeline_EmbedderFailure(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	emb := &fakeEmbedder{err: errors.New("embed service down")}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, emb)

	err := p.Run(context.Background(), job())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !emb.called {
		t.Error("embedder should have been called")
	}
	if qRepo.findSemantCalls != 0 {
		t.Errorf("FindSemantic should be skipped when embed fails, got %d calls", qRepo.findSemantCalls)
	}
	if len(qRepo.created) != 1 {
		t.Errorf("question should be created without embedding, got %d", len(qRepo.created))
	}
	if qRepo.created[0].Embedding != nil {
		t.Error("created question should have nil embedding")
	}
}

func TestPipeline_AnswersValueOnly(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: []ExtractedQuestion{
		{Number: 1, Text: "Pick the capital", Choices: []Answer{{"A", "Rome"}, {"B", "Madrid"}}, Answers: []Answer{{"A", "Rome"}}},
	}}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	_ = p.Run(context.Background(), job())
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	q := qRepo.created[0]
	if len(q.Answers) != 1 || q.Answers[0] != "Rome" {
		t.Errorf("answers should be value-only [Rome], got %v", q.Answers)
	}
	if len(q.Choices) != 2 || q.Choices[0] != "Rome" || q.Choices[1] != "Madrid" {
		t.Errorf("choices should be value-only [Rome Madrid], got %v", q.Choices)
	}
}

func TestPipeline_AIGeneratedTag(t *testing.T) {
	ext := &fakeExtractor{result: ExtractResult{Questions: sampleQuestions()[:1]}}
	p, _, qRepo, _ := testPipeline(&fakeEnhancer{}, ext, &fakeVerifier{}, &fakeEmbedder{embedding: []float32{0.1}})

	_ = p.Run(context.Background(), job())
	if len(qRepo.created) != 1 {
		t.Fatalf("expected 1 created, got %d", len(qRepo.created))
	}
	q := qRepo.created[0]
	found := false
	for _, tag := range q.Tags {
		if tag == "ai-generated" {
			found = true
		}
	}
	if !found {
		t.Errorf("new question should have ai-generated tag, tags=%v", q.Tags)
	}
}
