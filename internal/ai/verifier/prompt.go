package verifier

import "github.com/vlgrigoriev/coeus/skills"

// systemPrompt is the system prompt sent to the verifier/answerer model. It is
// the full, tested verify-extracted-questions skill — embedded verbatim from
// skills/verify-extracted-questions/SKILL.md, the single source of truth. The
// "tags" carry-through rule and Russian-output rules are part of the skill
// itself.
var systemPrompt = skills.VerifyPrompt
