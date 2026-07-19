// Package embedder implements pipeline.AIEmbedder using the OpenAI
// embeddings API via the shared oai client factory.
package embedder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/openai/openai-go"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// embedderDefaultTimeout caps the per-request HTTP timeout. EmbedderConfig has
// no Timeout field (intentionally), so the constant lives here.
const embedderDefaultTimeout = 30 * time.Second

// Compile-time guarantee that Embedder satisfies the port.
var _ pipeline.AIEmbedder = (*Embedder)(nil)

type Embedder struct {
	client *openai.Client
	dim    int
	model  string
	log    *slog.Logger
}

func New(cfg config.EmbedderConfig, log *slog.Logger) *Embedder {
	if log == nil {
		log = slog.Default()
	}
	return &Embedder{
		client: oai.NewClient(cfg.BaseURL, cfg.APIKey, embedderDefaultTimeout),
		dim:    cfg.Dim,
		model:  cfg.Model,
		log:    log,
	}
}

// Embed calls the embeddings endpoint and returns a float32 vector. On any
// failure (transport, malformed response, dimension mismatch, empty input) it
// returns (nil, err) — the pipeline skips semantic dedup for that question.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) == 0 {
		return nil, fmt.Errorf("embed: empty input")
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: StringInput{text}.FromString(),
	}

	resp, err := e.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("embed: empty response data")
	}

	in := resp.Data[0].Embedding
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}

	if e.dim > 0 && len(out) != e.dim {
		return nil, fmt.Errorf("embed: dimension mismatch: got %d, want %d", len(out), e.dim)
	}
	return out, nil
}

// EmbedBatch calls the embeddings endpoint once for many texts and returns one
// vector per input, aligned by the response index field. On any failure
// (transport, count mismatch, dimension mismatch, empty input) it returns
// (nil, err) — the caller treats batch embedding as best-effort.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("embed batch: empty input")
	}

	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(e.model),
		Input: StringsInput(texts).FromStrings(),
	}

	resp, err := e.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("embed batch: %w", err)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embed batch: got %d embeddings for %d inputs", len(resp.Data), len(texts))
	}

	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= int64(len(texts)) {
			return nil, fmt.Errorf("embed batch: response index %d out of range", d.Index)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		if e.dim > 0 && len(vec) != e.dim {
			return nil, fmt.Errorf("embed batch: dimension mismatch: got %d, want %d", len(vec), e.dim)
		}
		out[d.Index] = vec
	}
	return out, nil
}
