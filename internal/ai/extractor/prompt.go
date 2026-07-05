package extractor

import "github.com/vlgrigoriev/coeus/skills"

// systemPrompt is the system prompt sent to the vision model. It is the full,
// tested extract-questions-from-image skill — embedded verbatim from
// skills/extract-questions-from-image/SKILL.md, the single source of truth. The
// "tags" field and Russian-output rules are part of the skill itself.
var systemPrompt = skills.ExtractPrompt
