---
name: verify-extracted-questions
description: Use after extract-questions-from-image to independently verify answer correctness, validate JSON structure, and re-evaluate confidence scores. Use whenever the user mentions verifying, checking, reviewing, or validating extracted test/exam answers, wants a second opinion on answer accuracy, needs to prepare extracted questions for human review, or provides JSON that was extracted from test/exam images and wants to confirm the answers are correct. Also use when the user says "check my answers", "verify this", "review the extraction", or "double-check the results".
---

# Verify Extracted Questions

## Overview

Take the JSON output from `extract-questions-from-image` and perform a thorough second-pass verification. The goal is to catch errors before a human reviewer sees the results — structural problems get fixed, answer disagreements get flagged, and confidence scores get re-evaluated.

This skill does NOT have access to the original image. It works entirely from the extracted JSON and its own domain knowledge.

## When to Use

- After `extract-questions-from-image` has produced its JSON output.
- User wants a second opinion on answer accuracy before manual review.
- User needs structural validation of the extracted JSON.
- User is preparing a batch of extracted questions for a human checker and wants to pre-screen them.

## When NOT to Use

- The extraction has not been performed yet (use `extract-questions-from-image` first).
- The user wants to re-extract from the image (this skill only verifies existing JSON).
- The user wants a plain-language summary rather than a verified JSON.

## Input

One or more JSON files matching the `extract-questions-from-image` output schema:

```json
{
  "questions": [
    {
      "number": 1,
      "question": "...",
      "multiple_correct": false,
      "choices": ["...", "..."],
      "answers": [{"id": "A", "value": "..."}],
      "confidence": 0.92,
      "explanation": "..."
    }
  ]
}
```

May also include the optional `error` field from partial extractions.

**Batch:** Accept a directory of JSON files or a single JSON file with multiple independent question sets. Process each independently.

## Verification Process

Perform these three checks for every question. Work question by question, not all structure then all answers — this lets you catch cross-field inconsistencies.

### 1. Structural Validation

Check and **fix** these issues in the output JSON:

| Check | Action if fails |
|-------|----------------|
| All required keys present: `number`, `question`, `multiple_correct`, `choices`, `answers`, `confidence`, `explanation` | Add missing keys with sensible defaults (`multiple_correct: false`, `choices: []`, `answers: []`, `confidence: 0.5`, `explanation: ""`) |
| `answers[].id` maps to a valid position in `choices` (A→0, B→1, ..., or 1→0, 2→1, ...) | Fix the `id` to match the actual position of `value` in `choices`. If `value` is not in `choices`, keep the original but note the mismatch |
| `answers[].value` matches `choices[index]` for the given `id` | If `id` maps to a different choice than `value`, fix `value` to match what's actually at that position (the `id` is the source of truth since it reflects what was marked in the image) |
| `confidence` is a number between 0.0 and 1.0 | Clamp to range; if missing or non-numeric, default to 0.5 |
| `multiple_correct` is boolean | Coerce to boolean |
| No duplicate `number` values | Renumber sequentially if duplicates found |
| Question numbers are sequential (1, 2, 3...) | Fix gaps but preserve original order; note renumbering in `_verification` |

**Do NOT modify** `question` text, `choices` array content, or the `answers` array itself. These come from the original image and changing them would distort the source material.

### 2. Answer Correctness Verification

For each question, independently determine the correct answer and compare with the extracted answer.

**When to re-solve:**
- Confidence is below 0.80
- The `explanation` mentions inference, calculation, or uncertainty ("inferred from...", "calculated as...", "appears to be...", "possibly...")
- The question is from a domain where you have strong knowledge (math, science, logic, grammar, etc.)
- The answer seems obviously wrong even at a glance

**When to trust the extraction:**
- Confidence is 0.90+ and explanation says "visibly marked" or "clearly checked"
- The question requires visual inspection of the image (diagrams, highlighted text, handwritten marks) that you cannot see

**How to handle disagreements:**

If you believe the extracted answer is incorrect, do NOT change the `answers` array. Instead, append a verification flag to the `explanation` field using this exact format:

```
[VERIFICATION FLAG]
Original answer: {value} (id: {id})
Verifier suggests: {your answer} (id: {your id})
Reason: {brief reason for disagreement — calculation, logic, domain knowledge}
Action: awaiting human review
```

Keep the flag concise but specific. A human will read this and decide.

If you agree with the extracted answer, you may append a brief confirmation to the explanation (e.g., `[Verified: answer confirmed correct via independent calculation]`), but this is optional — don't bloat explanations that are already clear.

### 3. Confidence Re-evaluation

Re-assess the confidence score based on what you can determine from the JSON alone. Adjust the `confidence` field if the original score seems misaligned.

**Increase confidence when:**
- The question is straightforward and the answer is unambiguously correct
- The explanation is detailed and logically sound
- The question text is perfectly clear (no garbled characters, no truncation)

**Decrease confidence when:**
- The question text contains garbled characters, OCR artifacts, or obvious misreads (e.g., "H2S04" instead of "H₂SO₄", "Fe(OH)z" instead of "Fe(OH)₂")
- The explanation describes visual ambiguity ("mark is faint", "checkbox partially filled")
- The answer requires interpreting a graph, diagram, or image element you cannot see
- `multiple_correct` is `true` but the verifier can identify additional correct choices that were missed

**Confidence ranges** (same scale as extraction skill):
- `0.95–1.0`: Answer is clearly correct, no ambiguity
- `0.80–0.94`: Answer is likely correct, minor uncertainty
- `0.50–0.79`: Significant uncertainty — human must verify
- `0.0–0.49`: Answer is unreliable — treat as unchecked

### Garbled Text Detection

Watch for these signs of OCR/extraction errors in question text and choices:
- Chemical formulas with wrong capitalization or subscript (e.g., "H2O" → "H20", "CO2" → "C02")
- Numbers substituted for letters ("0" for "O", "1" for "l")
- Nonsensical word fragments
- Mismatched parentheses or brackets
- Text that doesn't form coherent sentences

When you find garbled text, note it in the explanation: `[NOTE: possible OCR error in question text — "{suspicious fragment}" may be "{likely correction}"]`. Do NOT fix the text itself.

## Output Format

Return the verified JSON with the same `{ questions: [...] }` structure. Add a `_verification` summary object at the top level:

```json
{
  "_verification": {
    "timestamp": "2026-06-15T10:30:00Z",
    "questions_verified": 5,
    "structural_fixes": [
      "Question 3: added missing 'confidence' field (defaulted to 0.5)",
      "Question 4: fixed answers[0].value to match choices at index B"
    ],
    "answers_flagged": [
      "Question 2: answer disagreement — verifier suggests D instead of B"
    ],
    "confidence_adjustments": [
      "Question 1: 0.92 → 0.85 (garbled text detected in question)",
      "Question 5: 0.60 → 0.75 (answer confirmed via calculation)"
    ],
    "garbled_text_detected": [
      "Question 1: 'H2S04' may be 'H₂SO₄'"
    ],
    "summary": "5 questions verified. 2 structural fixes applied. 1 answer flagged for human review. 0 questions could not be verified."
  },
  "questions": [...]
}
```

The `_verification` object is for the human reviewer's convenience. Downstream consumers that expect `{ questions: [...] }` can safely ignore it.

Every field in `_verification` is optional except `timestamp` and `summary`. Only include arrays (`structural_fixes`, `answers_flagged`, `confidence_adjustments`, `garbled_text_detected`) if they have entries.

## Handling the Error Case

If the input JSON contains an `error` field (from a partial extraction):
- Verify the successfully extracted questions normally
- Note in `_verification.summary` that some questions were not extracted
- Do not attempt to re-extract — that's the extraction skill's job

## Batch Verification

When given multiple files or a directory:

1. Process each file independently.
2. Produce one verified JSON per input file. Name output files as `<original_name>_verified.json`.
3. Print a batch summary at the end listing:
   - Total files processed
   - Total questions verified
   - Total structural fixes
   - Total answers flagged
   - Files with the most flags (prioritize human review of these)

If given a single JSON file with multiple question sets (unusual), preserve the grouping and verify each set independently.

## Example

**Input (`chemistry_test.json`):**
```json
{
  "questions": [
    {
      "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "multiple_correct": true,
      "choices": ["Fe(OH)₂", "Cs₂O", "HBr", "Na₂CO₃", "H₂SO₄"],
      "answers": [
        {"id": "C", "value": "HBr"}
      ],
      "confidence": 0.98,
      "explanation": "Checked boxes correspond to HBr and H₂SO₄, which are acids."
    }
  ]
}
```

**Verifier analysis:**
- Structural check: PASS — all fields present, IDs valid, confidence in range.
- Answer check: The explanation says "HBr **and** H₂SO₄" but only HBr is in the answers array. `multiple_correct` is `true`. H₂SO₄ (index 4, id "E") is also an acid and was described as checked. This is a structural inconsistency — the explanation describes two answers but only one is recorded.
- Confidence check: 0.98 is too high given the inconsistency.

**Output (`chemistry_test_verified.json`):**
```json
{
  "_verification": {
    "timestamp": "2026-06-15T10:30:00Z",
    "questions_verified": 1,
    "structural_fixes": [],
    "answers_flagged": [
      "Question 1: explanation mentions H₂SO₄ as checked but only HBr is in answers array"
    ],
    "confidence_adjustments": [
      "Question 1: 0.98 → 0.75 (answers array inconsistent with explanation)"
    ],
    "garbled_text_detected": [],
    "summary": "1 question verified. 0 structural fixes applied. 1 answer flagged for human review (possible missing answer)."
  },
  "questions": [
    {
      "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "multiple_correct": true,
      "choices": ["Fe(OH)₂", "Cs₂O", "HBr", "Na₂CO₃", "H₂SO₄"],
      "answers": [
        {"id": "C", "value": "HBr"}
      ],
      "confidence": 0.75,
      "explanation": "Checked boxes correspond to HBr and H₂SO₄, which are acids.\n\n[VERIFICATION FLAG]\nOriginal answer: HBr (id: C)\nVerifier notes: explanation states 'HBr and H₂SO₄' but only HBr is recorded. H₂SO₄ (id: E) is also an acid and appears to have been marked. Confidence lowered due to this inconsistency.\nAction: awaiting human review"
    }
  ]
}
```

## Process Summary

1. Read all input JSON files.
2. For each question in each file:
   - Validate and fix structure.
   - Independently verify the answer.
   - Re-evaluate confidence.
   - Check for garbled text.
   - Build the verified question object (with flags in explanation if needed).
3. Assemble the output JSON with `_verification` summary.
4. Write verified files.
5. Print batch summary (if multiple files).

## Common Mistakes

- **Modifying question text or choices.** Never do this — those come from the original image. Only flag garbled text in the explanation.
- **Changing the answers array.** Flag disagreements in the explanation instead. The human reviewer makes the final call.
- **Skipping confidence re-evaluation.** Even if the answer is correct, the confidence may be wrong. Re-evaluate every question.
- **Missing the `_verification` summary.** This is how the human reviewer quickly finds flagged questions. Always include it.
- **Forgetting to handle the `multiple_correct` flag.** If the flag is `true`, check whether all correct choices are selected and whether any incorrect ones slipped in.
- **Not detecting internal inconsistencies.** The explanation and the answers array should tell the same story. When they don't, flag it.

## Red Flags

- You feel tempted to rewrite the question text to fix OCR errors — don't, just note it.
- You're about to change an answer because you're "pretty sure" — flag it instead.
- You're spending too long on one question — flag it as uncertain and move on. The human will resolve it.
- The input JSON is so malformed you can't parse it — report the error clearly and stop.
