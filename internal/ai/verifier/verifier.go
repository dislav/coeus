// Package verifier implements pipeline.AIVerifier using a text LLM via the
// OpenAI-compatible Chat Completions API (DeepSeek by default).
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/vlgrigoriev/coeus/internal/ai/oai"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Verifier satisfies the port.
var _ pipeline.AIVerifier = (*Verifier)(nil)

type Verifier struct {
	client *openai.Client
	model  string
	log    *slog.Logger
}

func New(cfg config.ReviewerConfig, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{
		client: oai.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Timeout),
		model:  cfg.Model,
		log:    log,
	}
}

// Verify sends the extracted questions to the reviewer model and returns the
// adjusted confidences + explanations plus the raw _verification report.
// Any error is returned to the caller; the pipeline treats it as best-effort.
func (v *Verifier) Verify(ctx context.Context, questions []pipeline.ExtractedQuestion) (pipeline.VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err)
	}

	userPayload, err := json.Marshal(fromPipeline(questions))
	if err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: marshal input: %w", err)
	}
	userMsg := "Verify the following extracted questions:\n```json\n" + string(userPayload) + "\n```"

	rf := shared.NewResponseFormatJSONObjectParam()
	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(v.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMsg),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &rf,
		},
	}

	completion, err := v.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: %w", err)
	}
	if len(completion.Choices) == 0 {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: empty choices")
	}

	raw := completion.Choices[0].Message.Content
	cleaned := stripCodeFence(raw)

	var resp verificationResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return pipeline.VerifyResult{}, fmt.Errorf("verify: parse model JSON: %w", err)
	}
	return toPipeline(resp, len(questions)), nil
}

// stripCodeFence removes a leading ```json / ``` and trailing ``` fence if the
// model wrapped its JSON despite the json_object response format. Best-effort.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return s
	}
	// Drop the trailing fence.
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
