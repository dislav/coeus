// Package extractor implements pipeline.AIExtractor using a vision LLM via the
// OpenAI-compatible Chat Completions API (Moonshot/Kimi by default). The image
// is sent as a base64 data URL in a multimodal user message.
package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		thinking:   cfg.Thinking,
		log:        log,
	}
}

// Extract sends the image to the vision model and returns an ExtractResult.
//
// Return-shape contract (consumed by pipeline.extractWithRetries):
//   - transport failure (HTTP/network/timeout)      → (zero, err)        [retried]
//   - content failure (Error != nil in model JSON) → (result, nil)      [by code]
//   - success                                       → (result, nil)
//
// "parse model JSON" failure is treated as transport-class so the pipeline
// retries — a prose-wrapped response may succeed on the next attempt.
func (e *Extractor) Extract(ctx context.Context, image []byte, mime string) (pipeline.ExtractResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.ExtractResult{}, fmt.Errorf("extract: %w", err)
	}

	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)
	userText := "Extract all questions from this exam image. Respond as a JSON object matching this schema:\n\n" + extractionSchemaJSON

	rf := shared.NewResponseFormatJSONObjectParam()
	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(e.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
				openai.TextContentPart("```schema\n" + userText + "\n```\n\nNow extract from this image:"),
				imageURLPart(dataURL),
			}),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &rf,
		},
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
