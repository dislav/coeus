# Coeus Frontend Integration Guide

Coeus is an HTTP service that ingests exam images, runs an async AI pipeline
(extract → dedup → verify), and exposes the parsed multiple-choice questions as
**answers** to end users and as a **moderation queue** to experts.

This document is the canonical reference for building a frontend against the
Coeus API. It lists every endpoint, what it is for, and the correct way to use
it.

---

## Table of contents

1. [Mental model](#1-mental-model)
2. [Conventions](#2-conventions)
3. [Authentication](#3-authentication)
4. [Core user flow (end-to-end)](#4-core-user-flow-end-to-end)
5. [Endpoint reference](#5-endpoint-reference)
6. [Endpoint details](#6-endpoint-details)
7. [Expert moderation flow](#7-expert-moderation-flow)
8. [Frontend integration patterns — "how to use correctly"](#8-frontend-integration-patterns--how-to-use-correctly)
9. [TypeScript type reference](#9-typescript-type-reference)
10. [Gotchas & common mistakes](#10-gotchas--common-mistakes)

---

## 1. Mental model

Two roles, two surfaces:

- **`user`** — the default role assigned on registration. Creates time-boxed
  **sessions**, uploads exam images, polls the async pipeline, and reads their
  extracted answers within the session window.
- **`expert`** — provisioned out-of-band (there is no public expert
  registration). Works a moderation queue: reviews each extracted question
  against the source image and the DeepSeek verification report, then verifies
  or corrects it.

**The pipeline is asynchronous.** Uploading an image does **not** block until
the answers are ready. Upload returns `202 Accepted` immediately with a job id.
The worker pool runs the pipeline in the background. The frontend **must poll**
`GET /sessions/:id/images` and inspect each image's `job_status` until it
reaches a terminal state (`done` or `failed`). Only then are the answers
available via `GET /questions?session_id=...`.

**Sessions are time-boxed.** A session has an `expires_at` = creation +
`duration_seconds` + `buffer_seconds`. Uploads and answer reads are only
allowed while the session is **open** (stored status) **and** before
`expires_at` (wall-clock). After expiry the API returns `410 Gone`. Treat
`expires_at` as authoritative for any client-side countdown.

---

## 2. Conventions

| Concern | Rule |
|---|---|
| Base path | All API routes are under `/api/v1`. Health endpoints (`/healthz`, `/readyz`) are not. |
| Auth header | `Authorization: Bearer <token>` on every authenticated request. |
| Request bodies | `application/json` for everything **except** image upload, which is `multipart/form-data`. |
| Timestamps | RFC 3339 UTC strings, e.g. `2026-06-27T14:00:00Z`. |
| IDs | Opaque UUID strings. Never assume ordering or structure. |
| Error shape | Uniform: `{"error": {"code": "<code>", "message": "<msg>"}}` (see [§8](#error-handling)). |
| Pagination | `?page=<n>&per_page=<n>`; `page` default `1` (min `1`), `per_page` default `20`, clamped to `[1, 100]`. Envelope: `{"data": [...], "page": N, "per_page": N}`. **There is no `total` field** — detect the last page by receiving fewer items than `per_page` (or an empty `data` array). |

> **CORS: the backend handles CORS.** By default the server allows all origins
> (`*`) without credentials. A browser SPA on any origin can call the API
> directly — preflight `OPTIONS` requests are answered with `204` before auth
> middleware runs (so they never return `401`).
>
> Two env vars control CORS:
> - `COEUS_CORS_ALLOWED_ORIGINS` — comma-separated origins (default `*`).
> - `COEUS_CORS_ALLOW_CREDENTIALS` — `true|false|1|0` (default `false`).
>
> **Credentials caveat:** when `allow_credentials=true`, you **must** set
> specific origins — the server refuses to start if credentials are enabled
> alongside a wildcard `*` origin. In that mode the browser sends cookies and
> the `Authorization` header cross-origin.
>
> The allowed methods/headers are set in `config.yaml` and include everything the
> API needs (`GET`, `POST`, `PATCH`, `DELETE`, `OPTIONS`; `Authorization`,
> `Content-Type`, `X-Request-Id`).
>
> For same-origin production deployments (reverse proxy), CORS is harmless —
> the browser never sends a preflight when origin matches.

---

## 3. Authentication

| Endpoint | Method | Auth | Purpose |
|---|---|---|---|
| `/api/v1/auth/register` | POST | none | Register a new `user` account |
| `/api/v1/auth/login` | POST | none | Exchange credentials for a JWT |
| `/api/v1/auth/refresh` | POST | Bearer (valid token) | Re-issue a fresh token before the current one expires |

### 3.1 Register

```
POST /api/v1/auth/register
Content-Type: application/json

{"email": "user@example.com", "password": "secret123"}
```

- `password` must be **≥ 8 characters**.
- Always creates a `user`-role account. There is no way to register as expert.
- **201** → `{"id": "...", "email": "...", "role": "user"}`
- **400** validation (bad email / short password).
- **409** duplicate email.

### 3.2 Login

```
POST /api/v1/auth/login
Content-Type: application/json

{"email": "user@example.com", "password": "secret123"}
```

- **200** → **`{"token": "<jwt>", "role": "user"}`**
- **401** wrong email or password. (The message is generic; do not distinguish
  "user not found" from "wrong password" — both are `unauthorized`.)

> **Important:** the login response is `{"token", "role"}` — a single access
> token and the user's role. There is **no refresh token** and no `expires_at`
> field. Read the role from this response; do not decode the JWT to get it.

### 3.3 Refresh — the model you must understand

```
POST /api/v1/auth/refresh
Authorization: Bearer <current-still-valid-token>
```

- **200** → `{"token": "<new-jwt>", "role": "..."}`
- **401** if the current token is missing or **already expired**.

The access token TTL is **1 hour**. The refresh endpoint requires a **valid
(non-expired)** token — it just mints a fresh one. Consequences:

- **You cannot refresh an expired token.** Once the 1h window passes, refresh
  returns `401` and the user must log in again.
- **Refresh proactively**, before expiry. Recommended: schedule a refresh at
  `expiresAt − 5 minutes`, and also refresh on app refocus / reconnect.
- **On any `401`** from any endpoint, the token is dead → clear stored
  credentials and redirect to login. Do not retry the request after a refresh,
  because the refresh itself will also fail.

There is **no logout / token-revocation endpoint**. Logout is client-side only:
discard the token. A token remains valid server-side until its `exp` claim.

### 3.4 Token storage recommendation

- Store the token + role **in memory** while the app is running (e.g. an auth
  context / store), and persist them in `sessionStorage` or `localStorage` only
  if you accept the XSS exposure tradeoff. `localStorage` survives reloads but
  is readable by any injected script; `sessionStorage` is cleared on tab close.
- Derive `expiresAt` by decoding the JWT payload (`exp` claim) client-side —
  the payload is base64url JSON; you do not need the secret to read it.

---

## 4. Core user flow (end-to-end)

```
register/login ──▶ create session ──▶ upload image(s) ──▶ poll job status
                                                                 │
                                                                 ▼
                                                       (job_status: done)
                                                                 │
                                                                 ▼
                                            GET /questions?session_id=...
                                            (only while session is open & unexpired)
```

1. **Login** → store `token` + `role`.
2. **Create session** with a `duration_seconds` (and optional `buffer_seconds`).
   Note the returned `expires_at`.
3. **Upload** one or more exam images to `POST /sessions/:id/images`. Each
   returns `202` with `{image_id, job_id}`.
4. **Poll** `GET /sessions/:id/images` every ~3 seconds. For each image, read
   `job_status`:
   - `pending` → queued, not started.
   - `processing` → worker is running extract → dedup → verify.
   - `done` → questions are stored; answers are now readable.
   - `failed` → extraction failed after retries; the image may still produce
     `error`-status placeholder questions awaiting manual expert entry.
   - `unknown` → the status query degraded; treat like `pending` and retry.
5. Once an image is `done`, **fetch answers** with
   `GET /questions?session_id=<id>`. The user sees `answers` as
   `{id, value}` pairs where `id` is a derived label (A/B/C… or 1/2/3…).
6. Stop polling when every image is `done` or `failed`.
7. The session can be closed manually (`POST /sessions/:id/close`) or left to
   expire. **After `expires_at`, answer reads return `410`** — surface this to
   the user as "session expired".

> Why poll instead of websockets/SSE? Coeus has none. The worker pool processes
> jobs claim-based with a 5 s poll and AI calls up to 90 s each, so a single
> image can take a couple of minutes. Polling at **2–5 s** is the intended
> pattern. A reaper reclaims jobs stuck in `processing` beyond 10 minutes and
> fails them after 3 attempts.

---

## 5. Endpoint reference

### Public (no auth)

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness probe → `{"status":"ok"}` |
| GET | `/readyz` | Readiness probe (pings DB) |
| POST | `/api/v1/auth/register` | Register user |
| POST | `/api/v1/auth/login` | Login |

### Authenticated (`user` or `expert`)

| Method | Path | Role | Purpose |
|---|---|---|---|
| POST | `/api/v1/auth/refresh` | any | Renew token (before expiry) |
| POST | `/api/v1/sessions` | user | Create timed session |
| GET | `/api/v1/sessions` | user | List own sessions (paginated) |
| GET | `/api/v1/sessions/:id` | user | Session detail |
| POST | `/api/v1/sessions/:id/close` | user | Close session |
| POST | `/api/v1/sessions/:id/images` | user | Upload exam image (multipart) → `202` |
| GET | `/api/v1/sessions/:id/images` | user | List session images + job status |
| GET | `/api/v1/questions` | **role-split** | User: answers in a session. Expert: moderation queue. |
| GET | `/api/v1/questions/:id` | **role-split** | User: one own answer. Expert: full question. |
| POST | `/api/v1/questions` | **expert only** | Hand-author a verified question (`user` → 403) |
| PATCH | `/api/v1/questions/:id` | **expert only** | Verify a question (`user` → 403) |
| GET | `/api/v1/images/:id` | **expert only** | Source image bytes |
| GET | `/api/v1/images/:id/verification-report` | **expert only** | DeepSeek verification report |

`GET /questions` and `GET /questions/:id` are **the same URL for both roles**
but behave differently based on the `role` claim in the token. The response
shape differs by role — branch on the role you stored at login, not on field
presence.

---

## 6. Endpoint details

### 6.1 Sessions

**Create** — `POST /api/v1/sessions`

```json
// request
{"duration_seconds": 3600, "buffer_seconds": 300}

// 201 response
{"id": "uuid", "expires_at": "2026-06-27T15:05:00Z", "status": "open"}
```

- `duration_seconds` is **required** (≥ 1). `buffer_seconds` is optional (≥ 0,
  default 0). `expires_at` = creation + duration + buffer. The buffer exists so
  in-flight uploads can still be processed after the core window closes.

**List** — `GET /api/v1/sessions?page=1&per_page=20`

```json
{"data": [{"id":"...","expires_at":"...","status":"open"}], "page": 1, "per_page": 20}
```

**Get detail** — `GET /api/v1/sessions/:id`

```json
{
  "id": "uuid", "expires_at": "...", "status": "open",
  "duration_seconds": 3600, "buffer_seconds": 300,
  "started_at": "2026-06-27T14:00:00Z", "image_count": 3
}
```

- **404** if the session does not exist or belongs to another user (no leak).

**Close** — `POST /api/v1/sessions/:id/close` → **204 No Content**.

> **Status caveat:** the stored `status` can read `"open"` even after the
> wall-clock `expires_at` has passed — there is no background job that flips it
> to `"expired"`. The server enforces expiry correctly (via a dual check), but
> the field you display may lag. For a client-side countdown, always use
> `expires_at`, not `status`.

### 6.2 Images

**Upload** — `POST /api/v1/sessions/:id/images`

```
Content-Type: multipart/form-data
field name: "image"
```

- Max body **10 MiB**. Allowed MIME (sniffed from bytes, **not** trusted from
  the `Content-Type` header): `image/jpeg`, `image/png`, `image/webp`.
- **202** → `{"image_id": "uuid", "job_id": "uuid"}`. Processing is async.
- **400** wrong type / too large / missing field. **404** session not owned.
  **410** session closed/expired.

When building the request in JS, use `FormData` and **do not set the
`Content-Type` header manually** — the browser must set the multipart boundary:

```js
const form = new FormData();
form.append("image", file);            // a File/Blob
await fetch(`${API}/api/v1/sessions/${sid}/images`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}` },  // no Content-Type!
  body: form,
});
```

**List** — `GET /api/v1/sessions/:id/images` → **200**

```json
{
  "data": [
    {"id":"uuid","mime":"image/png","width":800,"height":600,
     "job_status":"done","created_at":"2026-06-27T14:01:00Z"}
  ]
}
```

- **Not paginated** — returns all images for the session in one response.
- This is your polling endpoint. Map over `data` and watch `job_status`.
- **410** if the session has expired/closed since upload.

### 6.3 Questions — user view

**List answers** — `GET /api/v1/questions?session_id=<id>&status=<opt>&page=<n>&per_page=<n>`

- **`session_id` is required** for users (missing → 400).
- The session must be **open and unexpired** (→ **410** otherwise).
- Only the caller's own session is visible (→ **404** otherwise).

```json
{
  "data": [
    {
      "id": "uuid", "number": 1,
      "question": "Укажите, какие из данных формул соответствуют кислотам:",
      "multiple_correct": true,
      "choices": ["Fe(OH)₂","Cs₂O","HBr","Na₂CO₃","H₂SO₄"],
      "answers": [{"id":"C","value":"HBr"},{"id":"E","value":"H₂SO₄"}],
      "status": "moderation", "confidence": 0.85
    }
  ],
  "page": 1, "per_page": 20
}
```

User response shape notes:
- **No `explanation`, `tags`, `image_id`, `choice_labeling`.** The user never
  sees AI reasoning.
- `answers` is an array of `{id, value}` objects. The `id` is a **derived
  display label**: `A`, `B`, … `Z`, `AA`, `AB`… (letter labeling) or `1`, `2`,
  `3`… (number labeling). It is derived from the value's index in `choices`. If
  a value is not found in `choices`, `id` is an empty string. **Do not assume
  ids are single letters.**
- `confidence` is a float in `[0, 1]`.

**Get one answer** — `GET /api/v1/questions/:id` → same shape as a list item.
- **404** if the question is not linked to the caller's session. (No explicit
  session-expiry check on the single GET, but to discover a question id the
  user must go through the expiry-gated list.)

### 6.4 Questions — expert view

**Moderation queue** — `GET /api/v1/questions?status=<moderation|error>&tag=<opt>&page=<n>&per_page=<n>`

- `status` defaults to `"moderation"`; allowed `"moderation"` or `"error"`
  (anything else → 400). Filter by `error` to see placeholders awaiting manual
  entry for unreadable images.
- `tag` is an optional exact-match filter.

```json
{
  "data": [
    {
      "id": "uuid", "number": 1, "question": "...", "multiple_correct": true,
      "choices": ["..."], "answers": ["HBr","H₂SO₄"],
      "choice_labeling": "letter", "confidence": 0.85,
      "explanation": "... [VERIFICATION FLAG] ...",
      "tags": ["ai-generated","chemistry"], "status": "moderation",
      "image_id": "uuid", "has_verification_report": true,
      "verified_at": null, "verified_by": null
    }
  ],
  "page": 1, "per_page": 20
}
```

Expert shape notes:
- `answers` is a **plain string array** (raw values, no derived ids).
- `explanation`, `tags`, `image_id`, `choice_labeling`, `has_verification_report`,
  `verified_at` (nullable), `verified_by` (nullable) are all present.
- `verified_at` / `verified_by` are `null` until an expert verifies.

**Verify / edit** — `PATCH /api/v1/questions/:id` (expert only; `user` → **403**)

```json
{
  "status": "verified",          // REQUIRED, must be exactly "verified"
  "answers": ["HBr","H₂SO₄"],    // optional, overwrites
  "choices": ["..."],            // optional, overwrites
  "explanation": "Because…",     // optional
  "tags": ["chemistry"],         // optional
  "confidence": 0.95             // optional; omitted → defaults to 1.0
}
```

- **200** → the full updated `ExpertQuestionResponse`. (If the post-update
  re-fetch fails, the API returns a minimal fallback `{"id":"...","status":"verified"}`.)
- **400** if `status` is absent or not `"verified"`. **404** question not found.
- The PATCH **overwrites** the supplied fields; omitted optional fields keep
  their existing values.

### 6.5 Expert image access

**Source image** — `GET /api/v1/images/:id`

- Returns the **raw image bytes** with `Content-Type` = the stored MIME.
- **404** if the image is missing **or its bytes have been cleaned** (see below).
- In a browser, fetch as a blob and render via `URL.createObjectURL` (the
  request needs the `Authorization` header, so you cannot use a plain `<img
  src>` URL):

```js
const res = await fetch(`${API}/api/v1/images/${iid}`, {
  headers: { Authorization: `Bearer ${token}` },
});
const blob = await res.blob();
imgEl.src = URL.createObjectURL(blob);  // revoke when done
```

**Verification report** — `GET /api/v1/images/:id/verification-report`

- `Content-Type: application/json`. The body is the raw DeepSeek report JSON.
- **200** with body `null` (the literal JSON null) when the image exists but has
  no report.
- **404** if the image is missing.

**Image cleanup:** when an expert verifies the **last** `moderation`/`error`
question linked to an image, that image's bytes are set to `NULL` in the same
transaction (the row and metadata are kept). After that, `GET /images/:id`
returns **404**. So in the moderation UI, **fetch and display the source image
before the user verifies the last open question** for that image.

### 6.6 Manual question creation — expert only

**Create** — `POST /api/v1/questions` (expert only; `user` → **403**)

```json
{
  "question": "Укажите, какие из данных формул соответствуют кислотам:",
  "choices": ["Fe(OH)₂","Cs₂O","HBr","Na₂CO₃","H₂SO₄"],
  "answers": ["HBr","H₂SO₄"],
  "multiple_correct": true,
  "choice_labeling": "letter",
  "explanation": "Кислоты —…",
  "tags": ["chemistry"],
  "confidence": 0.99
}
```

- `question` (required), `choices` (required, ≥ 2), `answers` (required, ≥ 1).
- `confidence` defaults to **0.99** when omitted (not 1.0 like PATCH).
- `choice_labeling` defaults to `"letter"`.
- **201** → full `ExpertQuestionResponse`. The question enters the store at
  `status: "verified"` with `verified_at` = now, `verified_by` = caller. It is
  **not** linked to a session or image (`number: 0`, `image_id: null`).
- The server **appends** `manual-entry` to `tags`; `ai-generated` is never
  applied.
- **409** `duplicate` if an identical question exists (exact-hash match). The
  response includes `question_id` so the frontend can redirect to a PATCH form:
  ```json
  {"error": {"code": "duplicate", "message": "question already exists", "question_id": "uuid"}}
  ```
- **400** if required fields are missing or values are invalid.
- If the embedder is configured, an embedding is generated best-effort (failures
  are logged, never block creation).

> **Use case:** seed canonical questions for subjects with no source image, or
> pre-populate a question bank before the exam season. Manually created questions
> are immediately `verified` and participate in future dedup.

---

## 7. Expert moderation flow

```
GET /questions?status=moderation  (the queue)
        │
        ▼  for each item:
GET /images/:image_id             (source image — render as blob)
GET /images/:image_id/verification-report   (DeepSeek reasoning, may be null)
        │
        ▼  decide:
PATCH /questions/:id  {status:"verified", answers, explanation, ...}
        │
        ▼  if this was the last open question for that image:
   image bytes are cleaned → subsequent GET /images/:image_id → 404
```

UI recommendation: group the queue by `image_id`, show the source image and
verification report inline, and disable the "verify" action only when all
corrections are entered. Filter `status=error` separately for unreadable-image
placeholders that need manual entry.

**Manual entry (no image):** experts can also create canonical questions directly
via `POST /questions` (see [§6.6](#66-manual-question-creation--expert-only)).
These bypass the pipeline entirely and enter the store already `verified`. Use
this to seed a question bank or add questions for which no source photo exists.

---

## 8. Frontend integration patterns — "how to use correctly"

### Auth client with proactive refresh

```js
const API = "/api/v1";   // CORS is enabled; see §2

let token = null, role = null, expiresAt = 0;
let refreshTimer = null;

async function login(email, password) {
  const r = await fetch(`${API}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!r.ok) throw await r.json();
  const body = await r.json();          // { token, role }
  storeToken(body.token, body.role);
  scheduleRefresh();
  return body;
}

function storeToken(jwt, r) {
  token = jwt; role = r;
  expiresAt = (JSON.parse(atob(jwt.split(".")[1])).exp) * 1000;
}

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  const delay = Math.max(expiresAt - Date.now() - 5 * 60 * 1000, 30_000);
  refreshTimer = setTimeout(refresh, delay);
}

async function refresh() {
  const r = await fetch(`${API}/auth/refresh`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (r.status === 401) return forceRelogin();   // token already dead
  if (!r.ok) throw await r.json();
  const body = await r.json();
  storeToken(body.token, body.role);
  scheduleRefresh();
}

function forceRelogin() { token = null; role = null; /* redirect to /login */ }
```

### A fetch wrapper with error normalization and 401 handling

```js
async function api(path, { method = "GET", body, json, signal } = {}) {
  const headers = { Authorization: `Bearer ${token}` };
  if (json !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(`${API}${path}`, {
    method, headers, signal,
    body: json !== undefined ? JSON.stringify(json) : body,  // body = FormData for upload
  });
  if (r.status === 401) { forceRelogin(); throw new Error("unauthorized"); }
  if (r.status === 204) return null;
  if (r.ok) return r.headers.get("content-type")?.includes("application/json")
    ? r.json() : r;
  const err = await r.json().catch(() => ({}));
  throw new ApiError(err.error?.code ?? "unknown", err.error?.message ?? r.statusText, r.status);
}

class ApiError extends Error {
  constructor(code, message, status) { super(message); this.code = code; this.status = status; }
}
```

### Upload + poll loop

```js
async function uploadAndAwait(sid, file, { onStatus } = {}) {
  const form = new FormData(); form.append("image", file);
  const { image_id } = await api(`/sessions/${sid}/images`, { body: form });

  while (true) {
    await sleep(3000);
    const { data } = await api(`/sessions/${sid}/images`);
    const img = data.find((d) => d.id === image_id);
    onStatus?.(img.job_status);
    if (img.job_status === "done" || img.job_status === "failed") return img;
    if (img.job_status === "unknown") continue;   // degraded read; keep polling
  }
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
```

### Error handling

<a name="error-handling"></a>

Every error response is `{"error": {"code": "...", "message": "..."}}`. Map the
`code` (or HTTP status) to a user-facing action:

| HTTP | `code` | Meaning | Frontend action |
|---|---|---|---|
| 400 | `validation` | Bad input (missing field, bad MIME, etc.) | Show field/validation error |
| 401 | `unauthorized` | Missing/expired/invalid token | Clear creds, redirect to login |
| 403 | `forbidden` | Wrong role (e.g. user calling PATCH) | Hide/disable that feature for non-experts |
| 404 | `not_found` | Resource missing, not owned, or image bytes cleaned | Show "not found" / stop polling |
| 409 | `duplicate` | Email already registered **or** question already exists | Register: "Account exists". POST /questions: read `question_id`, offer to edit existing |
| 410 | `session_expired` | Session closed or past `expires_at` | Disable upload; show "session expired" |
| 500 | `internal` | Unexpected server error | Generic error; suggest retry |
| 503 | `ai_unavailable` | AI service down (request-path only) | Retry; note pipeline failures surface as `job_status:"failed"`, not 503 |

Note: because extraction is async, **AI failures reach the frontend as
`job_status: "failed"`, not as an HTTP error on upload.** The upload itself
always returns `202` if the image is accepted.

### Pagination without `total`

There is no `total` field in list responses. Implement either "load more" (fetch
the next `page` until `data.length < per_page` or `data.length === 0`) or
infinite scroll on the same condition. Do not try to compute a page count.

### Role-based UI

- Use the `role` from the login/refresh response to choose routes and features.
- Do **not** show expert endpoints (`PATCH /questions`, `GET /images/...`) to
  `user`-role accounts — they will get `403`. Build two surfaces (user app vs
  expert console) gated on `role`.
- For `GET /questions` and `GET /questions/:id`, the same URL serves both roles
  with different shapes. Decode based on stored `role`, not on which fields are
  present.

---

## 9. TypeScript type reference

```ts
export type Role = "user" | "expert";
export type SessionStatus = "open" | "closed" | "expired";
export type JobStatus = "pending" | "processing" | "done" | "failed";
export type QuestionStatus = "moderation" | "verified" | "error";
export type ChoiceLabeling = "letter" | "number";

// --- Auth ---
export interface RegisterRequest { email: string; password: string; }       // password >= 8
export interface UserResponse { id: string; email: string; role: Role; }
export interface LoginRequest { email: string; password: string; }
export interface AuthResponse { token: string; role: Role; }                 // login + refresh

// --- Sessions ---
export interface CreateSessionRequest { duration_seconds: number; buffer_seconds?: number; }
export interface SessionResponse { id: string; expires_at: string; status: SessionStatus; }
export interface SessionDetailResponse extends SessionResponse {
  duration_seconds: number; buffer_seconds: number;
  started_at: string; image_count: number;
}
export interface SessionListResponse { data: SessionResponse[]; page: number; per_page: number; }

// --- Images ---
export interface ImageUploadResponse { image_id: string; job_id: string; }
export interface ImageResponse {
  id: string; mime: string; width: number; height: number;
  job_status: JobStatus | "unknown"; created_at: string;
}
export interface ImageListResponse { data: ImageResponse[]; }                // not paginated

// --- Questions: user view ---
export interface AnswerRef { id: string; value: string; }                    // id derived from index
export interface UserQuestionResponse {
  id: string; number: number; question: string; multiple_correct: boolean;
  choices: string[]; answers: AnswerRef[]; status: QuestionStatus; confidence: number;
}

// --- Questions: expert view ---
export interface ExpertQuestionResponse {
  id: string; number: number; question: string; multiple_correct: boolean;
  choices: string[]; answers: string[]; choice_labeling: ChoiceLabeling;
  confidence: number; explanation: string; tags: string[]; status: QuestionStatus;
  image_id: string; has_verification_report: boolean;
  verified_at: string | null; verified_by: string | null;
}
export interface QuestionListResponse<T = UserQuestionResponse | ExpertQuestionResponse> {
  data: T[]; page: number; per_page: number;
}

// --- Expert POST (manual creation) ---
export interface CreateQuestionRequest {
  question: string;                     // required
  choices: string[];                    // required, >= 2
  answers: string[];                    // required, >= 1
  multiple_correct?: boolean;           // default false
  choice_labeling?: ChoiceLabeling;     // default "letter"
  explanation?: string;
  tags?: string[];                      // server appends "manual-entry"
  confidence?: number;                  // [0,1], default 0.99
}

// --- Expert PATCH ---
export interface QuestionPatchRequest {
  status: "verified";                  // required
  answers?: string[]; choices?: string[]; explanation?: string;
  tags?: string[]; confidence?: number; // omitted confidence -> server default 1.0
}

// --- Errors ---
export interface ApiErrorBody { error: { code: string; message: string }; }
```

---

## 10. Gotchas & common mistakes

1. **CORS is enabled by default** (`*` origins, no credentials). A cross-origin
   browser app can call the API directly — preflight `OPTIONS` gets `204`. Set
   `COEUS_CORS_ALLOWED_ORIGINS` to specific origins in production. See [§2](#2-conventions).
2. **No refresh token.** `/auth/refresh` needs a *valid* token; an expired token
   cannot be refreshed. Refresh proactively, and treat any `401` as "log in
   again". See [§3.3](#33-refresh--the-model-you-must-understand).
3. **Upload is async.** `202` does **not** mean answers are ready. You must poll
   `job_status`. See [§4](#4-core-user-flow-end-to-end).
4. **`expires_at` is authoritative, not `status`.** A session can read `open`
   after its wall-clock expiry. The server enforces expiry correctly; your
   countdown must use `expires_at`.
5. **Answer reads expire with the session.** `GET /questions?session_id=...`
   returns `410` after the session window. If the user needs a persistent
   record, snapshot the answers client-side before expiry.
6. **No `total` in pagination.** Use "load more" / empty-page detection.
7. **`GET /questions` shape depends on role.** Same URL, two shapes. Branch on
   stored role.
8. **Answer `id`s are not always single letters.** They can be `AA`, `AB`, …
   (>26 choices) or numeric (`1`, `2`, …). Do not hardcode `[A–E]`.
9. **Expert images vanish after review.** Once the last open question for an
   image is verified, `GET /images/:id` → `404`. Display the image before the
   user verifies the last question.
10. **Multipart upload: do not set `Content-Type` manually.** Let the browser
    set the boundary, or the upload fails.
11. **Image MIME is sniffed, not trusted.** Sending a `.png` with `image/jpeg`
    content-type is fine; the server reads the bytes. But the bytes must
    actually be JPEG/PNG/WebP and ≤ 10 MiB.
12. **`PATCH` overwrites supplied fields; omitted fields are preserved.** If you
    send `answers`, it replaces the whole array — not appends.
13. **Verification report can be a literal `null` body** (HTTP 200, JSON `null`)
    when the image has no report. Distinguish this from `404` (image missing).
14. **No logout endpoint.** Logout is client-side only; the token stays valid
    until its `exp` (1 h, or longer if proactively refreshed).
