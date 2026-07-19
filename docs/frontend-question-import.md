# Frontend Guide: Question Import (CSV/Excel Upload)

This document describes everything the frontend needs to support the new
bulk question-import feature. Backend branch: `feat/question-import`.
Only NEW/changed behavior is covered — all other endpoints are unchanged.

---

## 1. Feature Summary

Experts and admins can upload a CSV or Excel (.xlsx) file of exam questions.
The server parses the file, validates every row, creates new questions and
updates existing ones (matched by question text), and returns a per-row
report in a single synchronous response.

- **Who:** roles `expert` and `admin` only. Hide/disable the import UI for
  the `user` role (the server rejects it with 403 anyway).
- **Where:** suggested placement — the expert questions page, next to the
  manual "Create question" action.

---

## 2. Endpoint

```
POST /api/v1/questions/upload
Authorization: Bearer <access token>
Content-Type: multipart/form-data
```

| Aspect | Value |
|---|---|
| Form field name | `file` (exactly this name) |
| Accepted formats | CSV (UTF-8) and `.xlsx` (first sheet only) |
| NOT accepted | legacy `.xls` — the server rejects it with a specific message (see §5) |
| Max file size | 10 MB (10 485 760 bytes) |
| Max rows | 20 000 data rows |
| Processing | **Synchronous** — the response arrives only after the whole file is processed |

**Timeout:** large files with embeddings enabled can take tens of seconds.
Set the HTTP client timeout for this request to **at least 120 seconds**
(the server write timeout) and show a persistent loading state.

---

## 3. File Format (show this to users)

Headerless, fixed column order. For `.xlsx` only the **first sheet** is read.

| Column | Name | Content |
|---|---|---|
| 1 | `question` | Question text. Required, non-empty. |
| 2 | `choices` | Answer options separated by `;`. **Empty = free-response question.** |
| 3 | `answers` | Correct answer(s) separated by `;`. At least one. For multiple-choice each must match a choice exactly (case-sensitive). |
| 4 | `explanation` | Free text. May be empty. |
| 5 | `tags` | Tags separated by `;`. Max 20. May be empty. |

CSV example:

```csv
What is 2+2?;3;4;5;4;Basic arithmetic;math
Explain entropy.;;disorder increases;Open question;physics;thermo
```

- Row 1: multiple-choice (choices present) → correct answer is `4`.
- Row 2: free-response (empty choices column) → correct answer is `disorder increases`.
- No header row — the first row is already data.
- Multiple-choice questions need **at least 2 choices**.
- Choice labels (A, B, C…) are NOT stored in the file. The backend assigns
  `choice_labeling: "letter"` to every imported question — render labels in
  display order exactly as for manually created questions.

**Duplicate semantics:** if a question with the same text (after
normalization: trim, lowercase, collapse whitespace) already exists, the
file wins — the existing question is **updated in place** (choices, answers,
explanation, tags are replaced; ID and history are preserved). If the same
question appears twice in one file, the later row wins.

---

## 4. Success Response — 200 OK

Returned whenever the file itself is parseable — **even if every row failed**.
Always inspect `failed`/`errors`, not just the HTTP status.

```json
{
  "total_rows": 150,
  "created": 120,
  "updated": 25,
  "failed": 5,
  "errors": [
    { "row": 17, "message": "multiple_choice requires at least 2 choices" },
    { "row": 42, "message": "answer \"X\" is not among the choices" }
  ]
}
```

| Field | Meaning |
|---|---|
| `total_rows` | Data rows found in the file. Always equals `created + updated + failed`. |
| `created` | New questions inserted. |
| `updated` | Existing questions overwritten by the file (duplicates — see §3). |
| `failed` | Rows that failed validation or storage. **Always the true count.** |
| `errors` | Up to the **first 100** row errors: `row` = 1-based file row number, `message` = human-readable reason. If `failed` > 100, display "showing first 100 of N errors". |

**UX guidance:**

- Render a summary block: `✓ 120 created · ⟳ 25 updated · ✗ 5 failed`.
- If `failed > 0`, render the error table (row number → message) and tell
  the user: **fix the file and re-upload it whole** — re-upload is safe and
  idempotent (already-imported rows will simply count as `updated`).
- `updated` is informational, not a warning — it means the file replaced an
  existing question.

---

## 5. Error Responses

### 400 Bad Request — the file as a whole was rejected

Standard envelope, same as the rest of the API:

```json
{ "error": { "code": "validation", "message": "legacy .xls not supported — save as .xlsx" } }
```

Possible `message` values (display `message` to the user as-is):

| Message | Cause |
|---|---|
| `invalid input` | No file in the `file` field, or body over 10 MB. |
| `empty file` | Zero-byte / whitespace-only file. |
| `unsupported file format` | Content is neither CSV text nor an xlsx zip (detected by content, not extension). Note: any plain-text file is treated as CSV and fails at row level instead. |
| `legacy .xls not supported — save as .xlsx` | Old Excel 97–2003 binary format. |
| `too many rows` | More than 20 000 data rows. |
| `malformed csv: line N, column M: …` | Structurally broken CSV (e.g. unterminated quote) — the message includes the line/column since this release. |
| `malformed xlsx: …` | Corrupt or unreadable workbook. |

### 401 / 403

- `401` — missing/expired token (standard handling).
- `403` — role is `user`. Should not happen if the UI is hidden by role;
  treat as "insufficient permissions".

### Client-side pre-validation (recommended)

Check before uploading to give instant feedback:

1. Extension in `.csv` / `.xlsx` (still send whatever the user picks — the
   server sniffs content, but this catches obvious mistakes).
2. Size ≤ 10 MB.
3. File is non-empty.

---

## 6. Impact on Existing Screens

Imported questions are indistinguishable from manually created ones except
for their tag:

- **Status:** imported questions are immediately `verified` — they do **not**
  appear in the moderation queue.
- **Tag:** every imported question carries the tag **`import`** (in addition
  to tags from the file). If the questions list supports tag filtering or
  badges, `import` can be used to highlight/filter bulk-added questions.
- **`choice_labeling`:** always `letter` — no new rendering logic needed.
- **`confidence`:** `0.99` for all imported questions.
- **No API shape changes** on `GET /api/v1/questions` or any other endpoint.

---

## 7. Suggested UI Flow

1. "Import questions" button (expert/admin only) → file picker
   (accept `.csv,.xlsx`).
2. Client-side checks (§5) → upload with a blocking progress indicator
   (timeout ≥ 120 s).
3. On 200 → render the report per §4 (summary + error table).
4. On 400 → render `error.message` per §5.
5. Offer a downloadable template file (CSV example from §3) so users prepare
   data in the right format.
