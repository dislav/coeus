package verifier

import "github.com/vlgrigoriev/coeus/skills"

// tagsExtension is a pipeline-specific addition layered on top of the tested
// verify-extracted-questions skill. It only asks the verifier to carry the
// "tags" array through unchanged so downstream stages keep their subject
// classification.
const tagsExtension = `

---

Pipeline extension (in addition to the rules above) — required field:
- "tags": carry this array through for every question, unchanged. Do not drop,
  rename, or reorder the input tags. You may add a single lowercase subject tag
  only if the input had none and the subject is unmistakable. This is the only
  field you may add; every other "do not modify" rule still applies.`

// systemPrompt is the system prompt sent to the reviewer model. It is the full,
// tested verify-extracted-questions skill — embedded verbatim from
// skills/verify-extracted-questions/SKILL.md, the single source of truth —
// plus the tags extension above.
var systemPrompt = skills.VerifyPrompt + tagsExtension
