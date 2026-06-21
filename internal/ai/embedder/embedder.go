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
