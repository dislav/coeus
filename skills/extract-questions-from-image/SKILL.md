---
name: extract-questions-from-image
description: Use when the user provides an image of a quiz, test, exam, or questionnaire and wants the questions, answer choices, correct answers, and confidence scores extracted into structured JSON. Also use when the image may be rotated, contain handwritten or printed text, include checkboxes, or have questions that require calculation or reasoning to determine the answer.
---

# Extract Questions from Image

## Overview

Analyze the provided image, extract all questions with their answer choices, determine the correct answer(s), and return everything as structured JSON.

The image may be a screenshot, a scanned paper, a rotated photo, or any other visual test/quiz document. Questions may be in any language and may require you to compute the answer when it is not visibly marked.

## When to Use

- The user uploads or references an image that contains one or more quiz/exam/test questions.
- The user asks for answers, question extraction, parsing, JSON output, or structured data from such an image.
- The image is rotated, low quality, or partially readable.

## When NOT to Use

- The user provides only text (no image).
- The user wants a plain summary or translation of the image without question/answer extraction.

## Output Format

Return a single JSON object with this exact structure. Every key shown is required and must be named exactly as shown:

```json
{
  "questions": [
    {
      "number": 1,
      "question": "Full question text as a single string",
      "choices": [
        "Fe(OH)₂",
        "Cs₂O",
        "HBr"
      ],
      "answers": [
        {"id": "C", "value": "HBr"}
      ],
      "confidence": 0.92,
      "explanation": "Brief reason for the answer; only as detailed as needed"
    }
  ]
}
```

**Required keys for every question object:** `number`, `question`, `choices`, `answers`, `confidence`, `explanation`. Do not rename, omit, or replace any of these keys.

### Field rules

- `number`: The question number shown in the image. If there is no number, use `1` and increment sequentially.
- `question`: The full question text. Preserve line breaks with `\\n` only when the question explicitly contains multiple lines (e.g., a table or a poem). Normally use a single line.
- `choices`: Array of choice **strings** in the order they appear in the image. **Do not include the leading label prefix** (e.g., `"A) "`, `"1. "`, `"б) "`) in the string — store only the choice text itself. If the question has no explicit choices (open-ended), set this to `[]` and put the answer in `answers`.
- `answers`: Array of objects, each with `id` and `value`:
  - `id`: The bare label used in the image, stripped of any trailing punctuation. Use only the letter or number: `"A"`, `"B"`, `"1"`, `"2"`, `"а"`, etc. Do not include `)`, `.`, or any other characters.
  - `value`: The full text of the correct choice, exactly as it appears in `choices` (without the label prefix).
  - If no answer can be determined, use an empty array `[]`.
  - This dual format preserves both the current ordering (via `id`) and the actual answer text (via `value`) for database storage.
- `confidence`: A number from `0.0` to `1.0` representing your confidence that the extracted data is correct.
- `explanation`: Brief note helping a human verify the answer. Include calculations or reasoning only when the answer is not directly visible. When the answer is inferred rather than visibly marked, explicitly state the source of uncertainty (e.g., "inferred from calculation", "text is slightly blurred", "graph values are unclear") so a downstream verifier knows what to double-check.

## Handling Correct Answers

1. **Visibly marked answers** — If the image shows checked checkboxes, filled circles, bold/highlighted choices, or any other marking, use those as the answer(s). Set confidence high if markings are clear.
2. **Single correct answer** — If the question asks for one answer and the image does not mark it, solve or reason to find the correct choice.
3. **Multiple correct answers** — If the question text says "choose all that apply" / "один или несколько" / "выберите верные утверждения" / or the UI uses checkboxes, list every correct choice in the `answers` array (multi-ness is derived from the answer count).
4. **No choices / open-ended** — If the question has no answer options, provide the answer directly in `answers` as an object with `value` only (e.g., `[{"value": "2 м/с²"}]`) and set `choices: []`.
5. **Cannot determine** — If the image is too unclear or the question requires domain knowledge you cannot confidently apply, leave `answers: []` and lower `confidence`.

## Confidence Scoring

Use the following guidance:

- `0.95–1.0`: Text and markings are very clear; little doubt.
- `0.80–0.94`: Text is readable but small, slightly blurry, or has some ambiguity.
- `0.50–0.79`: Parts are hard to read, rotated, or require significant inference.
- `0.0–0.49`: Major parts are unreadable or the answer is a guess.

Set per-question confidence, not per-answer confidence.

## Image Orientation

Images may be rotated (including upside down or 90°). Mentally rotate the image to the correct orientation before reading. Do not mention rotation in the output unless it caused partial extraction failure.

## Error Handling

If the image cannot be parsed correctly, return JSON in this exact error format:

```json
{
  "error": {
    "code": "unreadable_image" | "partial_extraction" | "no_questions_found",
    "message": "Human-readable description of what went wrong",
    "details": "Optional: which questions were affected, e.g., 'could not read questions 1 and 7'",
    "questions_extracted": 3,
    "questions_expected": 5
  },
  "questions": [
    ...any questions that were successfully extracted...
  ]
}
```

### Error codes

- `unreadable_image`: The entire image is unreadable (blurry, blank, no text).
- `partial_extraction`: Some questions were extracted, but others could not be read. Use `details` to list specific question numbers.
- `no_questions_found`: The image is readable but does not appear to contain any questions or answer choices.

Always include any successfully extracted questions in the `questions` array, even when returning an error.

## Process

1. Read the image carefully. If rotated, reorient mentally.
2. Identify each question and its boundaries.
3. Extract the question text, choices, and any visible markings.
4. Determine the correct answer(s):
   - From visible markings when present.
   - By calculation or reasoning when not marked.
   - Leave empty if neither is possible.
5. Build the JSON object.
6. Review the JSON for completeness and accuracy before responding.
7. Return only the JSON. Do not wrap it in markdown code blocks unless the user explicitly asks for markdown.

## Example

For an image with:

> 1. Укажите, какие из данных формул соответствуют кислотам:
> Выберите один или несколько ответов:
> □ Fe(OH)₂
> ☑ HBr
> □ Na₂CO₃
> ☑ H₂SO₄

Output:

```json
{
  "questions": [
    {
      "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "choices": [
        "Fe(OH)₂",
        "Cs₂O",
        "HBr",
        "Na₂CO₃",
        "H₂SO₄"
      ],
      "answers": [
        {"id": "C", "value": "HBr"},
        {"id": "E", "value": "H₂SO₄"}
      ],
      "confidence": 0.98,
      "explanation": "Checked boxes correspond to HBr and H₂SO₄, which are acids."
    }
  ]
}
```

## Common Mistakes

- Returning only one answer when multiple are correct.
- Returning id values that include punctuation like `"A)"`, `"1."`, or `"2)"`. IDs must be bare letters/numbers only.
- Leaving the label prefix inside `choices` strings like `"1) ..."` or `"A) ..."`. Strip the prefix and store only the choice text.
- Forgetting `confidence` on every question.
- Ignoring image rotation and producing garbled text.
- Including surrounding page headers, instructions, or stamps as part of the question text.
- Returning prose instead of the required JSON.
- Omitting confidence scores.

## Red Flags

- You feel tempted to describe the image instead of returning JSON.
- You are unsure whether a choice was selected but mark it anyway without lowering confidence.
- You skip a question because it is hard to read instead of returning a partial extraction error.

If you are unsure about a question, extract what you can and return a `partial_extraction` error with `details` explaining which questions were problematic.
