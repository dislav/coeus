// Package skills exposes the canonical, tested system-prompt markdown for the
// extractor and verifier AI clients. The markdown files are the single source of
// truth for what the models are told; they are embedded at build time so the
// prompts sent to the models always match the tested skill text — no drift from
// hand-condensed paraphrases.
//
// The consuming packages (internal/ai/extractor, internal/ai/verifier) append a
// small pipeline-specific extension (the "tags" field) on top of this base text.
package skills

import _ "embed"

// ExtractPrompt is the full extract-questions-from-image skill, verbatim. It is
// the system prompt for the vision extractor (Kimi/Moonshot).
//
//go:embed extract-questions-from-image/SKILL.md
var ExtractPrompt string

// VerifyPrompt is the full verify-extracted-questions skill, verbatim. It is the
// system prompt for the reviewer model (DeepSeek).
//
//go:embed verify-extracted-questions/SKILL.md
var VerifyPrompt string
