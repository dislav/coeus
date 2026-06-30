package extractor

import "github.com/vlgrigoriev/coeus/skills"

// tagsExtension is a pipeline-specific addition layered on top of the tested
// extract-questions-from-image skill. The skill itself is format-complete; this
// only asks for the extra "tags" field the pipeline uses for routing/dedup.
// (The JSON Schema sent in the user message also advertises this field; this
// note makes it explicit so the model reliably populates it.)
const tagsExtension = `

---

Pipeline extension (in addition to the output format above) — required field:
- "tags": an array of lowercase subject classifiers used for routing and
  de-duplication. Add it to every question object. Examples: "math",
  "chemistry", "physics", "biology", "history", "geography", "medicine",
  "literature", "informatics". Provide at least one tag per question when the
  subject is identifiable; use [] only when it genuinely cannot be determined.
  Example: "tags": ["chemistry"].`

// systemPrompt is the system prompt sent to the vision model. It is the full,
// tested extract-questions-from-image skill — embedded verbatim from
// skills/extract-questions-from-image/SKILL.md, the single source of truth —
// plus the tags extension above.
var systemPrompt = skills.ExtractPrompt + tagsExtension
