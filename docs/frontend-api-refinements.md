# Frontend API Refinements — Questions Service

**Status:** Final · **Effective:** 2026-07-02 · **Backend version:** `main` @ `dc2f117`

This document specifies the backend contract changes that the frontend **must** adapt to. It is written for the frontend team. Each section ends with an explicit **Frontend action required** list.

Two user-reported defects drove these changes, plus one structural cleanup:

1. *"Creating a new session shows questions from other sessions."* — Listing is now scoped by `session_id` for **both** roles.
2. *"Updating a question during moderation clears other fields because only `status` is sent."* — Moderation update is now a validating **full-replace `PUT`**; partial bodies are rejected, not applied.
3. `multiple_correct` is now derived (no frontend action needed — see §3).

> **Severity note:** Items 1 and 2 are **breaking** for existing frontend behavior. The moderation endpoint verb changed from `PATCH` to `PUT`, and the listing semantics changed for the expert role.

---

## 1. Question listing — `GET /api/v1/questions`

### 1.1 What changed

Scoping is now driven by the **`session_id` query parameter**, not by the user's role. Previously the **expert** role ignored `session_id` and always returned the global moderation queue — so an expert opening any session saw every session's questions. That is fixed.

| Role | `session_id` present | `session_id` absent |
|------|----------------------|---------------------|
| **user** | `200` — that session's questions **only if the user owns the session**; otherwise `403` | **`403 Forbidden`** (users may not access the global queue) |
| **expert** | `200` — that session's questions (experts may inspect any session) | `200` — global moderation queue (all sessions) |

> When `session_id` is present, **both roles receive the same session-scoped data**; only the response *shape* differs by role (see §4). The global queue survives as an **expert-only** view, reachable only by **omitting** `session_id`.

### 1.2 Status filter — now optional, "all" = omit the param

`status` is now **optional and uniform** across both modes:

| `status` value | Result |
|----------------|--------|
| **omitted / empty** | **All statuses** returned (no filter) |
| `moderation` | only `moderation` |
| `verified` | only `verified` |
| `error` | only `error` |
| anything else | `400 Bad Request` |

> ⚠️ The expert queue no longer defaults to `moderation`. To show "all", **omit the `status` param entirely** — do **not** send `status=all` (that returns `400`).

Other query params (unchanged): `page` (default `1`), `per_page` (default `20`, max `100`). The expert global queue also accepts `tag` (optional tag filter); session-scoped requests ignore `tag`.

### 1.3 Examples

**Expert viewing a specific session (the bug fix):**
```
GET /api/v1/questions?session_id=abc-123
→ 200, only session abc-123's questions
```
Previously this returned the global queue for experts. It now scopes correctly.

**Expert global moderation queue:**
```
GET /api/v1/questions
→ 200, questions from ALL sessions
```

**Filter the global queue:**
```
GET /api/v1/questions?status=moderation
→ 200, only moderation questions across all sessions
```

**User with no session:**
```
GET /api/v1/questions
→ 403 Forbidden   (was previously 400)
```

**User reading another user's session:**
```
GET /api/v1/questions?session_id=<someone-elses-session>
→ 403 Forbidden
```

### 1.4 Frontend action required

- **Expert session view must send `session_id`** when a session is selected. If your expert UI currently calls `GET /api/v1/questions` without `session_id` while a session is open, it is now hitting the global queue, not the session.
- **Global "all sessions" view** = omit `session_id` (expert only).
- **Status filter UI**: map the four tabs to: `all` → omit `status`; `moderation`/`verified`/`error` → send that value. Do **not** send `status=all`.
- **Handle `403`** on the listing call (user with no session, or user reading a non-owned session). Previously the no-session case returned `400`; it now returns `403`.

---

## 2. Moderation update — `PATCH` → `PUT` (full-replace)

### 2.1 What changed

The endpoint verb changed and the contract is now **full-replace with mandatory backend validation**.

| | Before | After |
|---|--------|-------|
| Verb | `PATCH /api/v1/questions/:id` | **`PUT /api/v1/questions/:id`** |
| Body | partial (e.g. `{status}` alone "worked") | **complete** editable object |
| Partial body | silently **cleared** the un-sent fields (the bug) | **rejected with `400`**, row untouched |
| `status` | forced to `verified` | expert chooses `moderation` \| `verified` \| `error` |

### 2.2 Request contract

```
PUT /api/v1/questions/:id
Authorization: Bearer <expert token>
Content-Type: application/json
```
```json
{
  "status": "verified",
  "choices": ["Paris", "London", "Berlin"],
  "answers": ["Paris"],
  "explanation": "Paris is the capital of France.",
  "tags": ["geography", "capitals"],
  "confidence": 0.95
}
```

| Field | Type | Required | Rule |
|-------|------|----------|------|
| `status` | string | **yes** | one of `moderation`, `verified`, `error` |
| `choices` | string[] | **yes** | ≥1 element; every element a non-empty string |
| `answers` | string[] | **yes** | ≥1 element; every element non-empty **and present in `choices`** (exact, case-sensitive match) |
| `explanation` | string | no | free text; may be empty |
| `tags` | string[] | no | ≤20 elements; every element non-empty |
| `confidence` | number | no | `0..1` inclusive; **defaults to `1.0`** when omitted |

> **`multiple_correct` is intentionally absent from the request body.** It is derived server-side from `len(answers) > 1` (see §3). Do not send it.

### 2.3 Why full-replace fixes the bug

The reported defect: the frontend sent only `{"status":"verified"}`, and the backend overwrote the stored `choices`/`answers` with empty/null. Under the new contract, that body is **rejected**:

```
PUT /api/v1/questions/:id
{ "status": "verified" }
→ 400 Bad Request   ("choices: required", "answers: required")
```
The stored row is **not modified** on a `400`. The frontend must send the entire, expert-reviewed question every time.

### 2.4 `status` semantics (now three-valued)

The expert can now move a question to any of the three statuses. The backend maintains a strict invariant:

| `status` sent | `verified_at` / `verified_by` in the stored row |
|---------------|---------------------------------------------------|
| `verified` | set to `now()` / the expert's id |
| `moderation` | cleared (set to `NULL`) |
| `error` | cleared (set to `NULL`) |

Invariant: **`verified_at` is non-null ⇔ `status == "verified"`**. The frontend can rely on this when rendering the "verified" badge — check `status === "verified"` (or `verified_at != null`), they are equivalent.

### 2.5 Responses

- **`200 OK`** — returns the full updated question as `ExpertQuestionResponse` (see §4.2).
  - *Edge case:* if the post-update re-fetch fails, the server returns `200` with a partial body `{"id": "<id>", "status": "<requested status>"}`. Handle both shapes.
- **`400 Bad Request`** — validation failure (missing/invalid fields, `answers ⊄ choices`, `confidence` out of range, too many tags). Row unchanged.
- **`403 Forbidden`** — caller is not an expert (route guard).
- **`404 Not Found`** — no question with that `id`.

### 2.6 Frontend action required

- **Change the HTTP verb from `PATCH` to `PUT`.** A `PATCH` request will now `404` (the route no longer exists).
- **Send the complete object on every save**, populated from the form: `status`, `choices`, `answers`, `explanation`, `tags`, and `confidence` (if the expert sets it).
- **Ensure `answers ⊆ choices`** before submit: every selected correct answer must be one of the `choices` values, matched exactly (case-sensitive). The creation form already lets the user pick correct answer(s) from the choices — make sure the submitted `answers` are the raw choice *values*, not display labels.
- **Validate client-side** to match §2.2 (non-empty choices/answers, ≤20 tags, `confidence` 0..1) so users get immediate feedback rather than a `400`.
- **Status selector**: offer `moderation`, `verified`, and `error` (not just "verify"). Reflect the `verified ⇔ verified_at` invariant in the UI.

---

## 3. `multiple_correct` field — derived, wire format unchanged

`multiple_correct` was a stored boolean; it is now **derived server-side** from `len(answers) > 1`. The column was dropped (migration `0004`).

**For the frontend: nothing changes.** The field is still present in both response shapes (see §4) with the same JSON name (`multiple_correct`) and type (`bool`). Its value is now computed rather than stored:

- `answers` has 0 or 1 entry → `multiple_correct: false`
- `answers` has 2+ entries → `multiple_correct: true`

The frontend confirmed it does not consume this field for rendering (it already infers multi-answer from the answer count), so this is a no-op. It is documented here for completeness: **do not send `multiple_correct` in any request body** (it is no longer accepted on `POST /api/v1/questions` either).

---

## 4. Response shapes (unchanged structure — for reference)

### 4.1 User-facing item — `UserQuestionResponse`

Returned in `data[]` for the **user** role (session-scoped only).

```json
{
  "id": "uuid",
  "number": 3,
  "question": "What is the capital of France?",
  "multiple_correct": false,
  "choices": ["Paris", "London", "Berlin"],
  "answers": [ { "id": "A", "value": "Paris" } ],
  "status": "verified",
  "confidence": 0.95
}
```
> Note: for the user role, `answers` is an array of `{id, value}` objects where `id` is a display label derived from the choice position (`A`, `B`, … for letter labeling; `1`, `2`, … for number labeling). `choices` is the raw value strings.

### 4.2 Expert-facing item — `ExpertQuestionResponse`

Returned in `data[]` for the **expert** role (session-scoped **or** global queue), and as the body of `PUT /api/v1/questions/:id`.

```json
{
  "id": "uuid",
  "number": 3,
  "question": "What is the capital of France?",
  "multiple_correct": false,
  "choices": ["Paris", "London", "Berlin"],
  "answers": ["Paris"],
  "choice_labeling": "letter",
  "confidence": 0.95,
  "explanation": "Paris is the capital of France.",
  "tags": ["geography"],
  "status": "verified",
  "image_id": "uuid",
  "has_verification_report": false,
  "verified_at": "2026-07-02T12:00:00Z",
  "verified_by": "expert-uuid"
}
```
> Note: for the expert role, `answers` is an array of raw value strings (not `{id,value}` objects). `verified_at` and `verified_by` are `null` when `status != "verified"`.

**Session-scoped expert view caveat:** when an expert requests `?session_id=X`, each item is built from the session-scoped read path. `has_verification_report` always defaults to `false` on this path (it is a global-queue-only convenience flag). If your expert UI depends on `has_verification_report`, only rely on it in the global queue view (`GET /api/v1/questions` without `session_id`).

### 4.3 List envelope — `QuestionListResponse` (unchanged)

```json
{
  "data": [ /* UserQuestionResponse | ExpertQuestionResponse depending on role */ ],
  "page": 1,
  "per_page": 20
}
```

---

## 5. Error code reference

| Code | When | Frontend handling |
|------|------|-------------------|
| `400` | Invalid `status` value; moderation `PUT` validation failure (missing fields, `answers ⊄ choices`, `confidence` out of range, >20 tags) | Show field-level validation errors; do not retry unchanged |
| `403` | Non-expert calling expert endpoints; **user requesting the global queue** (`GET` without `session_id`); **user reading a session they don't own** | Redirect to session selection / show "no access" |
| `404` | `GET/PUT` on an unknown question id; session not found on the user list path | Show "not found" |
| `410 Gone` | User list path: session expired or closed | Prompt to start a new session |

---

## 6. Migration checklist

- [ ] **Listing:** expert session view sends `?session_id=` when a session is selected.
- [ ] **Listing:** global expert view omits `session_id`.
- [ ] **Listing:** "all" status tab omits the `status` param (no `status=all`).
- [ ] **Listing:** handle `403` (user, no session / not-owner) and the new no-session semantics.
- [ ] **Moderation:** switch the update request from `PATCH` to **`PUT`**.
- [ ] **Moderation:** send the **full** object (`status`, `choices`, `answers`, `explanation`, `tags`, optional `confidence`) on every save.
- [ ] **Moderation:** client-side validation matches §2.2; guarantee `answers ⊆ choices` (exact, case-sensitive).
- [ ] **Moderation:** status selector offers `moderation` / `verified` / `error`; UI reflects `verified ⇔ verified_at`.
- [ ] **Create (`POST /api/v1/questions`):** stop sending `multiple_correct` (no longer accepted).
- [ ] Verify response parsing tolerates the rare `PUT` partial-body fallback `{id, status}`.

---

## 7. Out of scope / unchanged

- `POST /api/v1/questions` (manual question creation) — unchanged except `multiple_correct` is no longer an accepted input (derived).
- `GET /api/v1/questions/:id` — unchanged.
- Authentication (JWT), sessions lifecycle, image upload, and the async extraction pipeline — unchanged.
- Pagination defaults (`page=1`, `per_page=20`, max `100`) — unchanged.

## 8. Reference

- Design spec: `docs/superpowers/specs/2026-07-01-question-listing-and-moderation-design.md`
- Implementation plan: `docs/superpowers/plans/2026-07-01-question-listing-and-moderation.md`
- Backend commits on `main`: `f94c247`, `1ff94b8`, `5c990e2`, `3df3fc7`, `dc2f117`.
