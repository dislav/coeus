// Package extractor implements pipeline.AIExtractor using a vision LLM via the
// OpenAI-compatible Chat Completions API (Moonshot/Kimi by default). The image
// is uploaded to Moonshot Files and referenced as ms://<file_id>.
package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Extractor satisfies the port.
var _ pipeline.AIExtractor = (*Extractor)(nil)

type Extractor struct {
	client     *openai.Client
	model      string
	baseURL    string
	apiKey     string
	httpClient *http.Client
	thinking   bool
	log        *slog.Logger
}

func New(cfg config.VisionConfig, log *slog.Logger) *Extractor {
	if log == nil {
		log = slog.Default()
	}
	return &Extractor{
		client:     oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout),
		model:      cfg.Model,
		baseURL:    strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		thinking:   cfg.Thinking,
		log:        log,
	}
}

// Extract uploads the image, sends it to the vision model as an ms:// reference,
// and returns an ExtractResult. The uploaded file is deleted (best-effort) on
// return.
//
// Return-shape contract (consumed by pipeline.extractWithRetries):
//   - transport failure (upload error, HTTP/network/timeout, JSON parse)  → (zero, err)   [retried]
//   - content failure (Error != nil in model JSON)                       → (result, nil) [by code]
//   - success                                                            → (result, nil)
//
// "parse model JSON" failure is treated as transport-class so the pipeline
// retries — a prose-wrapped response may succeed on the next attempt.
func (e *Extractor) Extract(ctx context.Context, image []byte, mime string) (pipeline.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}

	fileID, err := e.uploadImage(ctx, image, mime)
	if err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: upload: %w", err)
	}
	defer func() {
		// Cleanup context decoupled from caller cancellation so a cancelled
		// request cannot leak the uploaded file, but bounded to 5s because the
		// DELETE travels through the same lossy proxy that may reset it.
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := e.deleteFile(delCtx, fileID); err != nil {
			e.log.Warn("delete uploaded file", "file_id", fileID, "error", err)
		}
	}()

	userText := "Extract all questions from this exam image. Respond as a JSON object matching this schema:\n\n" + extractionSchemaJSON

	rf := shared.NewResponseFormatJSONObjectParam()
	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(e.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("```schema\n" + userText + "\n```\n\nNow extract from this image:"),
				imageURLPart("ms://" + fileID),
			}),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &rf,
		},
	}
	// k2.6 enables "thinking" by default; disabling it cuts latency. The field
	// is Moonshot-specific and injected via SetExtraFields (a promoted method on
	// every generated param in openai-go v1.12.0).
	if !e.thinking {
		params.SetExtraFields(map[string]any{
			"thinking": map[string]any{"type": "disabled"},
		})
	}

	completion, err := e.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}
	if len(completion.Choices) == 0 {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: empty choices")
	}

	raw := completion.Choices[0].Message.Content
	cleaned := stripCodeFence(raw)

	var resp extractionResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: parse model JSON: %w", err)
	}
	return toPipeline(resp), nil
}

// stripCodeFence removes a leading ```json / ``` and trailing ``` fence if the
// model wrapped its JSON despite the json_object response format. Best-effort.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
