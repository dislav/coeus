---
name: verify-extracted-questions
description: Use after extract-questions-from-image to ANSWER the parsed questions. This skill is the authoritative answerer — it receives questions transcribed from an exam image, solves each one by reasoning, and writes the correct answers into the output JSON. Those output answers are canonical and replace whatever the parser extracted. Also use it to validate JSON structure, re-evaluate confidence, and prepare questions for human review. Use when the user says "answer these questions", "solve them", "verify this", "check the answers", or provides JSON extracted from test/exam images and wants correct answers.
---

# Verify Extracted Questions (Authoritative Answerer)

## Overview

Take the JSON output from `extract-questions-from-image` and **answer every question**. You are the smarter, reasoning model in the pipeline; the extractor only transcribed the image and deliberately did not solve anything. Your job is to determine the correct answer for each question, write it into the output, and explain your reasoning.

**Your `answers` array is authoritative.** Whatever you put in `answers` becomes the canonical answer shown to the user — it replaces the extractor's `answers` (which at most contains what was visually marked in the image, and is often empty). Do not merely "flag" disagreements in a note; **set the correct answer directly.**

This skill does NOT have access to the original image. It works from the extracted JSON, the question text, the choices, its own domain knowledge, and — when present — the `image_context` field, which is a text transcription of any graph/diagram/table/figure the extractor read from the image. Treat `image_context` as authoritative visual data (like `choices` and `answers`) and use it to solve the question.

## When to Use

- After `extract-questions-from-image` has produced its JSON output.
- The user wants the parsed questions **answered** / solved.
- The user wants a second, authoritative pass over extracted answers before manual review.
- The user needs structural validation of the extracted JSON.

## When NOT to Use

- Extraction has not been performed yet (use `extract-questions-from-image` first).
- The user wants to re-extract from the image (this skill never sees the image).

## Input

One or more JSON files matching the `extract-questions-from-image` output schema:

```json
{
  "questions": [
    {
      "number": 1,
      "question": "...",
      "choices": ["...", "..."],
      "answers": [{"id": "A", "value": "..."}],
      "tags": ["химия"],
      "confidence": 0.92,
      "explanation": "...",
      "image_context": "..."
    }
  ]
}
```

Treat the input `answers` as **evidence about what was visually marked in the image**, not as a claim of correctness. Frequently it will be empty (`[]`) — that just means the question was not marked and you must solve it.

**Formulas arrive as inline LaTeX.** The extractor transcribes every formula as `$...$` LaTeX (e.g. `$H_2SO_4$`, `$7 \times 8$`, `$v = 5~\text{м/с}^2$`) in `question`, `choices`, `answers[].value`, and `image_context`. Keep this format everywhere in your output: when you copy a choice into `answers[].value`, copy it verbatim *including its LaTeX*; when you compute an open-ended answer, write it as inline LaTeX too. Never convert formulas back to Unicode sub/superscripts (`H₂SO₄`, `м/с²`).

May also include the optional `error` field from partial extractions.

**Batch:** Accept a directory of JSON files or a single JSON file with multiple independent question sets. Process each independently.

## Answering Process

Perform these steps for every question. Work question by question.

### 1. Structural Validation

Check and **fix** these issues in the output JSON you produce:

| Check | Action if fails |
|-------|----------------|
| All required keys present: `number`, `question`, `choices`, `answers`, `confidence`, `explanation` | Add missing keys with sensible defaults (`choices: []`, `answers: []`, `confidence: 0.5`, `explanation: ""`) |
| `answers[].id` maps to a valid position in `choices` (A→0, B→1, ..., or 1→0, 2→1, ...) | Fix the `id` to match the actual position of `value` in `choices`. If `value` is not in `choices`, keep the original but note the mismatch |
| `answers[].value` matches `choices[index]` for the given `id` | If `id` maps to a different choice than `value`, fix `value` to match what's actually at that position |
| `confidence` is a number between 0.0 and 1.0 | Clamp to range; if missing or non-numeric, default to 0.5 |
| No duplicate `number` values | Renumber sequentially if duplicates found |
| Question numbers are sequential (1, 2, 3...) | Fix gaps but preserve original order; note renumbering in `_verification` |

**Do NOT modify** `question` text, `choices` array content, `tags`, or `image_context`. `image_context` is authoritative input data — consume it to solve, never rewrite or paraphrase it. The `question` and `choices` come from the original image and changing them would distort the source material; the `tags` are lowercase Russian subject classifiers the extractor produced, and they drive downstream routing and de-duplication, so they must be carried through for every question, unchanged. You **may and must** rewrite the `answers` array — that is the whole point of this skill. The only exception for `tags`: if the input had none and the subject is unmistakable, you may add a single lowercase Russian subject tag (e.g. `"химия"`).

### 2. Solve Each Question (your primary job)

For each question, **independently determine the correct answer** using your domain knowledge, calculation, and reasoning. Then **write that answer into the output `answers` array.** This is the canonical answer.

**How to solve:**

- **Multiple-choice with choices:** Pick the correct choice(s). Set `answers` to `{"id": "<letter/number>", "value": "<exact choice text from choices>"}`. The `value` must be copied verbatim from the `choices` array — including its inline-LaTeX form (e.g. `"$\\mathrm{HBr}$"`, not `HBr` or `НВг`).
- **Multiple correct answers:** If the question says "choose all that apply" / "один или несколько" / "выберите верные утверждения", include **every** correct choice in `answers` and exclude every incorrect one. Multi-ness is derived from the answer count.
- **Open-ended (no choices):** Compute or state the answer directly in inline LaTeX, e.g. `{"value": "$2~\\text{м/с}^2$"}` (no `id`), and set `choices: []`.
- **Question with `image_context`:** When `image_context` is non-empty, it contains the concrete data (axes, coordinates, table cells, labels) of a graph/diagram/figure the question depends on. Use it as the visual data needed to solve the question — read values from it, perform calculations on the transcribed data points, etc. It is authoritative; do not reply that you "cannot retrieve the image" — the data is already transcribed for you.
- **Calculation required:** Show the work briefly in `explanation` (formulas, substitution, result), then put the final answer in `answers`.

**How to treat the input answers (evidence from the image):**

- If the input had **no marked answer** (`answers: []`): solve the question from scratch and write your answer. This is the common case.
- If the input had a **visibly marked answer**: treat it as a hint about what was marked on the page. Solve independently anyway.
  - If your solution **agrees** with the marking: keep that answer, set confidence high, and note confirmation in `explanation` (e.g., `"Совпадает с отметкой на изображении."`).
  - If your solution **disagrees** with the marking: **your answer wins.** Set `answers` to your solution. The marking may have been a student's wrong answer or a misread; the reasoning model is authoritative. Note the disagreement in `explanation` and record it in `_verification.answers_overridden`.
- If you **cannot solve** a question (genuinely outside your knowledge, or the question text is too garbled to understand): set `answers: []`, set `confidence` below 0.5, and explain what blocked you. Do not guess.

### 3. Confidence Scoring (answer confidence)

Confidence now reflects how certain you are that **your answer is correct** — not transcription quality (that was the extractor's job).

- `0.95–1.0`: Answer is unambiguously correct; verified by calculation or clear-cut knowledge.
- `0.80–0.94`: Answer is very likely correct; minor residual uncertainty.
- `0.50–0.79`: Significant uncertainty — human must verify.
- `0.0–0.49`: Answer is unreliable or could not be determined. Treat as unanswered.

## Free-response confidence tiers

For questions with `choices: []` (free-response), apply these confidence tiers:

- **Short objective answers** — formulas, numbers, single words, names, units (the common case). Solve at **confidence ≥ 0.80**.
- **Detailed / subjective answers** — algorithms, proofs, "развернутый ответ", multi-sentence explanations. Produce a **best-effort** answer at **0.50–0.79**. These typically require human review; the moderate confidence signals "plausible, needs verification."

### Answer format

Produce **exactly what fills the input field** — include units if implied by the prompt (`$2~\text{м/с}^2$`, not `2`), omit surrounding prose. The value must be directly droppable into the blank. Formulas and units follow the inline-LaTeX convention (`$...$` with `\text{}` for units; escape backslashes as `\\` in JSON).

## Garbled Text Detection

The extractor may have produced OCR/extraction errors. Watch for:

- Malformed or miscopied LaTeX: wrong subscripts/superscripts (`$H_2S0_4$` with a zero, `$CO_3^{2-}$` rendered as `$CO_32-$`), broken commands (`$\frak{1}{2}$` for `$\frac{1}{2}$`), unbalanced braces or `$` delimiters, `\mathrm{}` dropped or misplaced.
- Chemical formulas with wrong capitalization or subscript (e.g. `$H_2O$` mistyped as `$H_2O$` vs `$HO_2$`, `$Fe(OH)_3$` vs `$Fe(OH)_2$`).
- Numbers substituted for letters ("0" for "O", "1" for "l").
- Nonsensical word fragments, mismatched parentheses, incoherent sentences.

When you find garbled text: reason about what it most likely should be, solve based on the corrected reading, and note it in `explanation`: `[ПРИМЕЧАНИЕ: возможная ошибка распознавания в тексте вопроса — «{подозрительный фрагмент}» прочитан как «{вероятное исправление}»]`. Do **not** modify the `question` or `choices` text itself.

## Output Language

All human-readable prose you author must be in **Russian**:
- `explanation` for every question (your reasoning).
- Every `_verification` array entry: `structural_fixes`, `answers_overridden`, `confidence_adjustments`, `garbled_text_detected`, `unsolved`.
- `_verification.summary`.

JSON keys (`number`, `_verification`, `timestamp`, etc.) stay in English — they are identifiers. Transcribe `question` and `choices` exactly as received. `tags` stay as lowercase Russian subject classifiers, carried through unchanged. Only the prose you *write* is Russian.

## Output Format

Return the answered JSON with the same `{ questions: [...] }` structure, where each question's `answers` now holds **your authoritative answers**. Add a `_verification` summary object at the top level:

```json
{
  "_verification": {
    "timestamp": "2026-06-15T10:30:00Z",
    "questions_answered": 5,
    "structural_fixes": [
      "Вопрос 3: добавлено отсутствующее поле «confidence» (по умолчанию 0.5)"
    ],
    "answers_overridden": [
      "Вопрос 2: в исходных данных отмечено B; при проверке получен ответ D"
    ],
    "confidence_adjustments": [
      "Вопрос 1: 0.92 → 0.85 (в вопросе обнаружен искажённый текст)",
      "Вопрос 5: 0.60 → 0.95 (ответ подтверждён вычислением)"
    ],
    "garbled_text_detected": [
      "Вопрос 1: «$H_2S0_4$», возможно, «$H_2SO_4$»"
    ],
    "unsolved": [
      "Вопрос 4: текст вопроса слишком искажён, чтобы решить"
    ],
    "summary": "Обработано 5 вопросов. 5 решено. 1 структурная правка. 1 отметка исходных данных заменена. 0 нерешённых."
  },
  "questions": [...]
}
```

Every field in `_verification` is optional except `timestamp` and `summary`. Only include an array if it has entries. The `_verification` object is for the human reviewer's convenience and is persisted alongside the answers; downstream consumers that expect `{ questions: [...] }` can safely ignore it.

> The JSON key must remain `_verification` exactly — downstream code reads it by name.

## Handling the Error Case

If the input JSON contains an `error` field (from a partial extraction):
- Answer the successfully extracted questions normally.
- Note in `_verification.summary` that some questions were not extracted.
- Do not attempt to re-extract — that is the extractor's job.

## Batch Answering

When given multiple files or a directory:

1. Process each file independently.
2. Produce one answered JSON per input file. Name output files `<original_name>_verified.json`.
3. Print a batch summary at the end listing:
   - Total files processed
   - Total questions answered
   - Total structural fixes
   - Total input markings overridden
   - Total unsolved
   - Files with the most overrides / unsolved (prioritize human review of these)

## Example

**Input (`chemistry_test.json`):**
```json
{
  "questions": [
    {
      "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "choices": ["$\\mathrm{Fe(OH)_2}$", "$\\mathrm{Cs_2O}$", "$\\mathrm{HBr}$", "$\\mathrm{Na_2CO_3}$", "$H_2SO_4$"],
      "answers": [
        {"id": "C", "value": "$\\mathrm{HBr}$"}
      ],
      "tags": ["химия"],
      "confidence": 0.98,
      "explanation": "Отмечен вариант C (HBr); весь текст чёткий."
    }
  ]
}
```

**Answerer analysis:**
- Structural check: PASS.
- Solve: Acids are substances that donate H⁺. Among the choices: $\mathrm{Fe(OH)_2}$ is a base (hydroxide), $\mathrm{Cs_2O}$ is a basic oxide, **$\mathrm{HBr}$ is an acid** (hydrobromic acid), $\mathrm{Na_2CO_3}$ is a salt, **$H_2SO_4$ is an acid** (sulfuric acid). So the correct answers are HBr (C) and H₂SO₄ (E).
- The input only had HBr marked. The marking was partial/incorrect — the answerer adds H₂SO₄.
- Confidence: 0.97 (straightforward chemistry).

**Output (`chemistry_test_verified.json`):**
```json
{
  "_verification": {
    "timestamp": "2026-06-15T10:30:00Z",
    "questions_answered": 1,
    "answers_overridden": [
      "Вопрос 1: в исходных данных отмечен только HBr (C); при проверке добавлен H₂SO₄ (E) как вторая верная кислота"
    ],
    "summary": "Обработан 1 вопрос. 1 решён. 1 отметка исходных данных заменена."
  },
  "questions": [
    {
      "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "choices": ["$\\mathrm{Fe(OH)_2}$", "$\\mathrm{Cs_2O}$", "$\\mathrm{HBr}$", "$\\mathrm{Na_2CO_3}$", "$H_2SO_4$"],
      "answers": [
        {"id": "C", "value": "$\\mathrm{HBr}$"},
        {"id": "E", "value": "$H_2SO_4$"}
      ],
      "tags": ["химия"],
      "confidence": 0.97,
      "explanation": "Кислоты отдают H⁺. HBr (бромоводородная кислота) и H₂SO₄ (серная кислота) — кислоты; Fe(OH)₂ — основание, Cs₂O — основной оксид, Na₂CO₃ — соль. В исходных данных отмечен только HBr; добавлен H₂SO₄."
    }
  ]
}
```

## Process Summary

1. Read all input JSON files.
2. For each question in each file:
   - Validate and fix structure (but never edit `question` or `choices` text).
   - **Solve the question** and write the authoritative answer into `answers`.
   - Compare with any input marking; override if your solution disagrees.
   - Set confidence based on your certainty in the answer.
   - Check for garbled text; reason around it and note it.
   - If truly unsolvable, leave `answers: []` and record in `_verification.unsolved`.
3. Assemble the output JSON with `_verification` summary.
4. Write verified files.
5. Print batch summary (if multiple files).

## Common Mistakes

- **Merely flagging a disagreement instead of setting the correct answer.** You are the answerer — write the correct `answers` array directly.
- **Modifying question text or choices.** Never do this — those come from the original image. Only note garbled text in the explanation.
- **Copying the input answers verbatim without solving.** The input answers are evidence, not a solution. Always solve independently.
- **Skipping confidence re-evaluation.** Re-assess every question based on your certainty in your own answer.
- **Missing the `_verification` summary.** This is how the human reviewer quickly finds overrides and unsolved questions. Always include it.
- **Not checking multi-answer completeness.** When the question allows several answers, make sure every correct choice is present and no incorrect one slipped in.
- **Guessing when you cannot solve.** If you genuinely cannot determine the answer, leave `answers: []`, set confidence below 0.5, and record it in `unsolved`.
- **Converting formulas back to Unicode.** The extractor's inline LaTeX (`$H_2SO_4$`) must survive into your output: `answers[].value` copied from `choices` keeps its exact LaTeX, and computed open-ended answers are written as inline LaTeX too — never `H₂SO₄` or `2 м/с²`.

## Red Flags

- You are about to append a `[VERIFICATION FLAG]` note and leave the wrong answer in place — don't. Set the correct answer.
- You feel tempted to rewrite question text to fix OCR errors — don't, just note it and reason around it.
- You are copying the extractor's answers without solving — don't. Solve every question.
- You're spending too long on one question — set your best answer, lower confidence, and move on.
- The input JSON is so malformed you can't parse it — report the error clearly and stop.
- **Ignoring `image_context` for graph/figure questions.** When `image_context` is present, it carries the visual data you need. Do not reply "I cannot retrieve the image" — the data is already transcribed in `image_context`. Use it.
