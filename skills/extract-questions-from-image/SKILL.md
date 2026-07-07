---
name: extract-questions-from-image
description: Use when the user provides an image of a quiz, test, exam, or questionnaire and wants the questions and answer choices transcribed into structured JSON. This skill is a PARSER ONLY — it reads what the image literally shows (question text, choices, and any visibly-marked answers) and does NOT solve, calculate, or reason about correct answers. A separate downstream model answers the questions. Also use when the image may be rotated, contain handwritten or printed text, or include checkboxes.
---

# Extract Questions from Image

## Overview

Analyze the provided image and **transcribe** it: extract every question, its answer choices, and any answers that are **visibly marked** in the image (checked boxes, filled circles, highlighted/bold choices). Return everything as structured JSON.

**You are a parser, not a solver.** Your only job is to read what is printed and marked on the page. Do not determine correctness by reasoning, calculation, or domain knowledge. When no answer is visibly marked, leave `answers` empty — the downstream model will solve it.

The image may be a screenshot, a scanned paper, a rotated photo, or any other visual test/quiz document. Questions may be in any language.

## When to Use

- The user uploads or references an image that contains one or more quiz/exam/test questions.
- The user asks for question extraction, parsing, OCR, or structured JSON from such an image.
- The image is rotated, low quality, or partially readable.

## When NOT to Use

- The user provides only text (no image).
- The user wants the questions **answered** or solved. Parsing is this skill's job; **answering is a different skill** (`verify-extracted-questions`). Do not attempt to answer here.
- The user wants a plain summary or translation of the image without question/choice extraction.

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
      "tags": ["химия"],
      "confidence": 0.92,
      "explanation": "Что вы увидели на изображении; без рассуждений (на русском)",
      "image_context": ""
    }
  ]
}
```

**Required keys for every question object:** `number`, `question`, `choices`, `answers`, `tags`, `confidence`, `explanation`, `image_context`. Do not rename, omit, or replace any of these keys.

### Field rules

- `number`: The question number shown in the image. If there is no number, use `1` and increment sequentially.
- `question`: The full question text, transcribed verbatim. Preserve line breaks with `\\n` only when the question explicitly contains multiple lines (e.g., a table or a poem). Normally use a single line.
- `choices`: Array of choice **strings** in the order they appear in the image. **Do not include the leading label prefix** (e.g., `"A) "`, `"1. "`, `"б) "`) in the string — store only the choice text itself. If the question has no explicit choices (open-ended), set this to `[]`.
- `answers`: Array of objects, each with `id` and `value`, describing **only what is visibly marked in the image**:
  - `id`: The bare label used in the image, stripped of any trailing punctuation. Use only the letter or number: `"A"`, `"B"`, `"1"`, `"2"`, `"а"`, etc. Do not include `)`, `.`, or any other characters.
  - `value`: The full text of the marked choice, exactly as it appears in `choices` (without the label prefix).
  - **If nothing is visibly marked, set `answers: []`.** Do not fill this from reasoning or calculation.
  - For open-ended questions with a visible handwritten/printed answer, record it as `{"value": "the visible answer"}` (no `id`).
- `confidence`: A number from `0.0` to `1.0` representing your confidence that you **transcribed** the question, choices, and markings correctly. This is about legibility and clarity of what is on the page — **not** about whether an answer is correct. See the rubric below.
- `explanation`: A brief note, **in Russian**, on what you **observed**, to help the downstream answering model. Describe markings and legibility only. Examples: `"Отмечен вариант C; других отметок нет."`, `"Ответ на изображении не отмечен."`, `"Вариант B частично размыт."`. **Do not include calculations, reasoning, or claims about correctness.**
- `image_context`: When the question depends on a graph, diagram, table, chart, or figure, transcribe **all concrete visual data** needed to solve it: axes with units, data points/coordinates, table cells, labels, legends, and the curve or shape. Write it at a level of detail sufficient for a text-only solver to reproduce the answer — e.g. `"Ось X: время (с), 0→10. Ось Y: скорость (м/с), 0→20. Точки: (0,0),(2,5),(4,10),(6,15),(8,20). Возрастающая прямая."`. When the question is purely textual and depends on no visual, set `image_context: ""` (empty string — the field is **required**, never omit it). This is transcription, not solving: record what the visual shows, do not compute the answer.
- `tags`: An array of **lowercase Russian** subject classifiers used downstream for routing and de-duplication. Add it to every question object. Provide at least one tag per question when the subject is identifiable; use `[]` only when it genuinely cannot be determined. Suggested vocabulary: `математика`, `химия`, `физика`, `биология`, `история`, `география`, `медицина`, `литература`, `информатика`. Example: `"tags": ["химия"]`.

### Output Language

All human-readable prose you author must be in **Russian**:
- `explanation` for every question.
- Error `message` and `details` (when returning an error).

JSON keys (`number`, `question`, `choices`, etc.) and error `code` values (`unreadable_image`, `partial_extraction`, `no_questions_found`) stay in English — they are identifiers. Transcribe `question` and `choices` exactly as they appear in the image, whatever language that is. Only the prose you *write* is Russian.

## Handling Answers (parser rules)

1. **Visibly marked answers** — If the image shows checked checkboxes, filled circles, bold/highlighted choices, or any other marking, transcribe those markings into `answers`. This is a visual observation, not a judgment of correctness. Set `confidence` high when the markings are clear and unambiguous.
2. **Multiple marked answers** — If the question text says "choose all that apply" / "один или несколько" / "выберите верные утверждения" / or the UI uses checkboxes and several are marked, transcribe every marked choice into `answers`.
3. **No visible marking** — If the image does not mark an answer for a question, set `answers: []`. **Do not solve, calculate, reason, or guess.** Leave solving entirely to the downstream model. Lower `confidence` only if the question *text itself* was hard to read — not because the answer is absent.
4. **Open-ended with no visible answer** — Set `answers: []` and `choices: []` unless a handwritten/printed answer is visibly present.
5. **Cannot read the question text** — Transcribe what you can and return a `partial_extraction` error naming the affected questions.

> The critical rule: **an empty `answers` array is the correct, expected output for any question whose correct answer is not visually marked.** A blank answer here is not a failure — it tells the downstream model "this question needs to be solved."

## Recognizing Free-Response Questions

Some questions have **no answer choices** — instead they have an **input field** the solver must fill in. These are free-response questions. Emit them with `choices: []`.

### Visual signals that indicate free-response (set `choices: []`)

- Underscore runs or blank lines acting as an answer field:
  `Ответ: ______`, `Answer: ____`, a trailing blank line.
- An answer prompt (`Ответ:`, `Answer:`, `=`, `?`) with **no enumerated choices following it**.
- Digital form placeholders: `[input]`, `[____]`, an empty text box glyph.
- A gap inside a sentence or equation the solver is meant to fill:
  `v = ___ м/с`, `The capital of France is ___`.

### Guidance

When these signals are present, **confidently emit `choices: []`**. This is a positive, deliberate recognition — not an absence of data. Keep the surrounding transcription `confidence` high; the missing choices are expected, not a parse failure.

If the image shows a pre-filled answer in the input field (e.g. a worked exam), transcribe it into `answers` as usual. If the field is blank, emit `answers: []` and let the verifier fill it.

### Free-response examples

**Blank field (no visible answer):**

```json
{
  "number": 7,
  "question": "Чему равно ускорение тела через 2 с?",
  "choices": [],
  "answers": [],
  "tags": ["физика"],
  "confidence": 0.95,
  "explanation": "Вопрос с открытым ответом; поле ответа пустое."
}
```

**Pre-filled answer visible in image:**

```json
{
  "number": 7,
  "question": "Чему равно ускорение тела через 2 с?",
  "choices": [],
  "answers": [{"value": "5 м/с²"}],
  "tags": ["физика"],
  "confidence": 0.95,
  "explanation": "Вопрос с открытым ответом; в поле ответа вписано «5 м/с²»."
}
```

## Confidence Scoring (transcription confidence)

Confidence reflects how reliably you read the page, not whether an answer is right:

- `0.95–1.0`: Text and markings are crisp and unambiguous; no doubt about what is written or marked.
- `0.80–0.94`: Text is readable but small, slightly blurry, or a marking is a little faint.
- `0.50–0.79`: Parts are hard to read, rotated, or partially obscured; you are unsure of some characters or whether a mark is present.
- `0.0–0.49`: Major parts are unreadable or you are guessing at the text.

Set per-question confidence. An unmarked-but-clearly-legible question should still be `0.90+` — the absence of a marking is not uncertainty about the text.

## Image Orientation

Images may be rotated (including upside down or 90°). Mentally rotate the image to the correct orientation before reading. Do not mention rotation in the output unless it caused partial extraction failure.

## Error Handling

If the image cannot be parsed correctly, return JSON in this exact error format:

```json
{
  "error": {
    "code": "unreadable_image" | "partial_extraction" | "no_questions_found",
    "message": "Читаемое человеком описание того, что пошло не так (на русском)",
    "details": "Необязательно: какие вопросы затронуты, например: «не удалось прочитать вопросы 1 и 7»",
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
3. Transcribe the question text and choices verbatim, stripping label prefixes from choices.
4. Record only **visibly marked** answers. If nothing is marked, leave `answers: []`.
5. Set `confidence` based on how legibly you could read the text and markings.
6. Write `explanation` describing what you observed (markings, legibility) — no reasoning.
7. Build the JSON object and review it for completeness before responding.
8. Return only the JSON. Do not wrap it in markdown code blocks unless the user explicitly asks for markdown.

## Example

For an image with:

> 1. Укажите, какие из данных формул соответствуют кислотам:
> Выберите один или несколько ответов:
> □ Fe(OH)₂
> ☑ HBr
> □ Na₂CO₃
> ☑ H₂SO₄

Output (note: only the visibly checked boxes are recorded):

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
      "tags": ["химия"],
      "confidence": 0.98,
      "explanation": "Отмечены варианты C (HBr) и E (H₂SO₄); весь текст чёткий."
    }
  ]
}
```

For an image with an **unmarked** question:

> 2. Чему равно значение выражения 7 × 8?

Output (no answer is marked, so `answers` is empty — the downstream model will solve it):

```json
{
  "questions": [
    {
      "number": 2,
      "question": "Чему равно значение выражения 7 × 8?",
      "choices": [],
      "answers": [],
      "tags": ["математика"],
      "confidence": 0.97,
      "explanation": "Вопрос с открытым ответом; ответ на изображении не отмечен."
    }
  ]
}
```

For an image with a **graph-dependent** question:

```json
{
  "questions": [
    {
      "number": 3,
      "question": "По графику определите скорость тела через 2 с после начала движения.",
      "choices": [],
      "answers": [],
      "tags": ["физика"],
      "confidence": 0.93,
      "explanation": "Вопрос с открытым ответом; ответ на изображении не отмечен. К графику приложены данные в image_context.",
      "image_context": "График зависимости скорости от времени. Ось X: время (с), 0→10. Ось Y: скорость (м/с), 0→20. Линия — возрастающая прямая через точки: (0,0), (2,5), (4,10), (6,15), (8,20)."
    }
  ]
}
```

## Common Mistakes

- **Solving or guessing an answer when none is visibly marked.** This is the most important mistake to avoid. Leave `answers: []` and let the downstream model solve it.
- Including calculations, reasoning, or correctness claims in `explanation`.
- Lowering `confidence` just because a question has no marked answer. Confidence is about legibility, not answer presence.
- Returning only one marked answer when several boxes are checked.
- Returning `id` values that include punctuation like `"A)"`, `"1."`, or `"2)"`. IDs must be bare letters/numbers only.
- Leaving the label prefix inside `choices` strings like `"1) ..."` or `"A) ..."`. Strip the prefix and store only the choice text.
- Forgetting `confidence` on every question.
- Ignoring image rotation and producing garbled text.
- Including surrounding page headers, instructions, or stamps as part of the question text.
- Returning prose instead of the required JSON.
- **Omitting `image_context` or describing a visual too vaguely.** If a question depends on a graph/table/figure, `image_context` must carry the concrete data (values, coordinates, labels) — `"на графике показана зависимость"` is useless to the downstream solver. Transcribe the actual numbers and labels.
- **Leaving `image_context` off a question object.** It is a required key; use `""` for purely-textual questions, never drop the key entirely.

## Red Flags

- You feel tempted to compute an answer (math, chemistry, logic) — stop. If it is not visibly marked, leave `answers: []`.
- You are unsure whether a choice was marked but record it anyway without lowering confidence.
- You want to describe the image instead of returning JSON.
- You skip a hard-to-read question instead of returning a partial extraction error.
