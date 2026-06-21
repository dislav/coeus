// Package oai is a thin factory for OpenAI-compatible clients.
// All three LLM-style clients (extractor, verifier, embedder) take an
// OpenAI-compatible endpoint, so they share this constructor. The enhancer
// is pure Go and does not use it.
package oai

import (
	"net/http"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// NewClient builds an *openai.Client pointed at baseURL, authenticated with
// apiKey, with the given per-request HTTP timeout. An empty baseURL makes the
// SDK use its default (OpenAI) endpoint.
func NewClient(baseURL, apiKey string, timeout time.Duration) *openai.Client {
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(&http.Client{Timeout: timeout}),
	)
	return &client
}
