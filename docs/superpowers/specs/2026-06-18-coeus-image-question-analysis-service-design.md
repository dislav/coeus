# Coeus — Image Question Analysis Service

**Status:** Design approved, pending implementation plan
**Date:** 2026-06-18
**Tech stack:** Go + Gin, Postgres + PGX, pgvector

---

## 1. Overview

Coeus is a backend service that helps users analyze images of quizzes, tests, and exams. A user starts a timed session, uploads photographs of question pages, and receives answers with a status. Behind the scenes, a vision model extracts questions and answers from the enhanced image, a text model verifies them, and the results flow through a deduplication + human-moderation lifecycle. Experts review uncertain or errored answers and promote them to verified, at which point the original image bytes are freed.

Two committed skills define the AI contracts and are consumed verbatim as system prompts:

- `skills/extract-questions-from-image/SKILL.md` — vision extraction (Kimi K2.7). Output: `{ questions: [{ number, question, multiple_correct, choices[], answers[{id,value}], confidence, explanation, tags[] }] }` plus an optional `error` block (`unreadable_image` | `partial_extraction` | `no_questions_found`). The `tags[]` field per question is a planned extension to the skill (subject tags emitted by Kimi; the service injects `ai-generated` on top).
- `skills/verify-extracted-questions/SKILL.md` — text-only verification (DeepSeek V4 Pro). Input: the extracted JSON. Output: the same `{ questions: [...] }` with possible per-question `confidence`/`explanation` adjustments, plus a top-level `_verification` summary. The verifier never rewrites `answers`; disagreements become `[VERIFICATION FLAG]` blocks appended to `explanation`.

### Goals

- Async image → answers pipeline within a user's timed session.
- Canonical, deduplicated question store: the same exam question photographed by many users is one row, verified once.
- Two answer-lifecycle statuses (`moderation`, `verified`) plus one unreadable-image status (`error`) forwarded to specialists.
- Expert moderation surface with full context (image + verification report + AI explanation).
- Shuffle-safe storage: answers persist as values only; display ids are derived at read time.
- Single-binary deployment: one Go process + one Postgres. No Redis, no S3, no external queue.

### Non-goals (v1)

- Multi-host horizontal scaling (queue + bytea are swappable later, but not designed for it now).
- WebSocket/SSE push (polling only; push is a localized add later).
- OpenTelemetry tracing (request-id + slog correlation is the v1 floor).
- Fuzzing.
- An expert invite/registration flow (expert accounts seeded out-of-band).
- Re-displaying images to users after they've been cleaned (bytes are gone).

---

## 2. Key Decisions (from brainstorming)

| # | Decision | Choice | Rationale |
|---|---|---|---|
| 1 | Upload/result delivery | **Async + polling** | Survives long AI calls without holding HTTP connections; matches the status lifecycle naturally |
| 2 | Existing-question matching | **Hybrid: exact hash then pgvector semantic** | Exact handles re-uploads for free; vector catches OCR drift and paraphrases |
| 3 | Tags source | **Kimi emits subject tags in extraction output** | Avoids an extra model call; `tags` field to be added to the extraction skill |
| 4a | Tag placement | **Per question** | Mixed-subject pages are common |
| 4b | `ai-generated` tag | **Service-injected** | Provenance is a system invariant, not model output |
| 5a | Worker mechanism | **Postgres-as-queue (LISTEN/NOTIFY + `FOR UPDATE SKIP LOCKED`)** | Durable, survives restarts, no new infra |
| 5b | Image storage | **Postgres `bytea`** | Single source of truth, trivial backups, fits exam-photo sizes |
| 6a | Auth | **JWT with `user`/`expert` roles** | Real identity + enforcement without opaque-token DB lookup |
| 6b | Moderation URL shape | **Unified `/api/v1/questions`** | Role gates behavior, not URLs |
| 6c | Expert edit | **Overwrite in place, no history table** | Auditability deferred; v1 keeps it simple |
| — | Code structure | **Standard Go layout + narrow interface ports** | Idiomatic, testable, no hexagonal ceremony |

Additional refinements from review:

- **Three-value `questions.status`**: `moderation`, `verified`, `error`. The first two are the answer lifecycle; `error` is the unreadable-image state forwarded to specialists.
- **Kimi extraction retries**: up to 3 attempts within one job on `unreadable_image`/`no_questions_found`; on final failure a single `error`-status placeholder question is created and linked to the image for manual processing. `partial_extraction` does not retry — proceed with what was read.
- **Queue-level retries (distinct from extraction retries)**: a reaper reclaims stale `processing` rows; after `max_queue_attempts` a job goes to `failed`.
- **DeepSeek verification is best-effort**: on failure or timeout, questions are still saved as `moderation` with Kimi's extraction as-is; no `_verification` report is persisted.
- **Value-only answers in DB**: `questions.answers` is a JSON array of value strings (e.g. `["HBr","H₂SO₄"]`), not `[{id,value}]`. Display ids (`"A) Paris"`, `"1. Amsterdam"`) are derived at read time from the choice index + `questions.choice_labeling`.
- **User responses exclude `explanation`** (expert-only, carries verification flags).
- **Image byte cleanup**: after an expert verifies the last `moderation`/`error` question linked to an image, `images.original` and `images.enhanced` are set to `NULL` in the same transaction. The `images` row and all metadata remain.

---

## 3. Data Model (Postgres)

### 3.1 Extensions

- `pgvector` — for `questions.embedding` and cosine-gist indexing.

### 3.2 Tables

**`users`**
```sql
id            uuid primary key default gen_random_uuid()
email         text not null unique
password_hash text not null
role          text not null check (role in ('user','expert'))
created_at    timestamptz not null default now()
```

**`sessions`** — a user's timed test window.
```sql
id               uuid primary key default gen_random_uuid()
user_id          uuid not null references users(id) on delete cascade
duration_seconds int not null check (duration_seconds > 0)
buffer_seconds   int not null check (buffer_seconds >= 0)
started_at       timestamptz not null default now()
expires_at       timestamptz not null  -- = started_at + duration_seconds + buffer_seconds
status           text not null default 'open' check (status in ('open','closed','expired'))
-- index on (user_id, created_at desc)
```

**`images`** — one per upload.
```sql
id                  uuid primary key default gen_random_uuid()
session_id          uuid not null references sessions(id) on delete cascade
original            bytea null          -- null after post-review cleanup
enhanced            bytea null          -- null after post-review cleanup
mime                text not null
width               int null
height              int null
verification_report jsonb null          -- DeepSeek's _verification on success; null on failure/skip
extraction_error    jsonb null          -- Kimi's `error` block on partial_extraction (expert awareness)
created_at          timestamptz not null default now()
-- index on (session_id, created_at)
```

**`questions`** — the canonical, deduplicated knowledge base.
```sql
id                uuid primary key default gen_random_uuid()
number            int not null
question          text not null          -- '' for error placeholders
question_normalized text not null        -- lowercased, whitespace-collapsed, punctuation-stripped
question_hash     text not null unique   -- sha256 of question_normalized (exact-match arm)
multiple_correct  boolean not null default false
choices           jsonb not null         -- array of strings, label prefixes stripped, image order
answers           jsonb not null         -- array of VALUE STRINGS ONLY, e.g. ["HBr","H₂SO₄"]
choice_labeling   text not null default 'letter' check (choice_labeling in ('letter','number'))
confidence        numeric(3,2) not null default 0
explanation       text not null default ''
embedding         vector(1536) null      -- null only for error placeholders
status            text not null default 'moderation'
                  check (status in ('moderation','verified','error'))
verified_at       timestamptz null
verified_by       uuid null references users(id)
created_at        timestamptz not null default now()
updated_at        timestamptz not null default now()
-- ivfflat/hnsw index on embedding for cosine (pgvector)
-- index on (status) for the moderation queue
```

**`session_questions`** — uniform link between a session's image and a canonical question (covers both "matched existing" and "newly created", including error placeholders).
```sql
id                    uuid primary key default gen_random_uuid()
session_id            uuid not null references sessions(id) on delete cascade
image_id              uuid not null references images(id) on delete cascade
question_id           uuid not null references questions(id) on delete cascade
extracted_number      int not null
extracted_confidence  numeric(3,2) not null
created_at            timestamptz not null default now()
-- unique(session_id, image_id, question_id)
-- index on (image_id) for the cleanup check
-- index on (session_id) for GET /questions?session_id=
```

**`tags`**
```sql
id   uuid primary key default gen_random_uuid()
name text not null unique
-- seeded with 'ai-generated', 'needs-manual'
```

**`question_tags`**
```sql
question_id uuid not null references questions(id) on delete cascade
tag_id      uuid not null references tags(id) on delete cascade
primary key (question_id, tag_id)
```

**`jobs`** — the Postgres-backed queue.
```sql
id          uuid primary key default gen_random_uuid()
image_id    uuid not null references images(id) on delete cascade
session_id  uuid not null references sessions(id) on delete cascade
status      text not null default 'pending'
            check (status in ('pending','processing','done','failed'))
attempts    int not null default 0
last_error  text null
queued_at   timestamptz not null default now()
started_at  timestamptz null
finished_at timestamptz null
-- index on (status, queued_at) for the claim query
```

### 3.3 Status semantics

| `questions.status` | Meaning | Set by | Cleared by |
|---|---|---|---|
| `moderation` | AI-extracted answer awaiting expert review | Pipeline (new or after best-effort verify) | Expert `PATCH` to `verified` |
| `verified` | Expert-confirmed answer; future matches return this | Expert `PATCH` | — (terminal) |
| `error` | Kimi could not read the image after 3 attempts; placeholder awaiting manual entry | Pipeline (extraction exhausted) | Expert `PATCH` (fills in fields, sets `verified`) |

| `jobs.status` | Meaning |
|---|---|
| `pending` | Enqueued, waiting for a worker |
| `processing` | Claimed by a worker |
| `done` | Pipeline finished (may include error-question placeholders) |
| `failed` | Queue-level retries exhausted; operator-visible |

### 3.4 The deduplication lookup (step 4)

For each extracted question:
1. Compute `question_hash = sha256(normalize(question))`.
2. **Exact match**: `SELECT id FROM questions WHERE question_hash = $1`. On hit → reuse the existing canonical row; link via `session_questions`; skip to step 8.
3. **Semantic fallback** (no exact hit): embed the question text; `SELECT id, embedding <=> $1 AS dist FROM questions WHERE embedding IS NOT NULL ORDER BY dist LIMIT 1`. If `dist <= (1 - semantic_threshold)` (i.e. cosine similarity ≥ `0.92` by default) → reuse; link; skip to step 8.
4. **No hit** → create a new `questions` row at `status='moderation'`.

### 3.5 Image byte cleanup

Triggered synchronously inside the expert `PATCH /api/v1/questions/:id` transaction, after the status flip:

```sql
-- After updating the patched question:
SELECT count(*) FROM session_questions sq
JOIN questions q ON q.id = sq.question_id
WHERE sq.image_id = $1 AND q.status IN ('moderation','error');
-- If 0:
UPDATE images SET original = NULL, enhanced = NULL WHERE id = $1;
```

The `images` row, metadata (`mime`, `width`, `height`, `verification_report`, `extraction_error`, `created_at`), and all `questions`/`session_questions` rows are retained. Idempotent: a second PATCH touching a sibling question re-runs the count (still 0) and the `UPDATE … = NULL` is a harmless no-op.

---

## 4. HTTP API

All endpoints under `/api/v1`, JSON in/out. Uniform error shape: `{"error":{"code":"...","message":"..."}}`.

### 4.1 Auth (`/api/v1/auth`)

| Method | Path | Auth | Body / Result |
|---|---|---|---|
| `POST` | `/auth/register` | open | `{email,password}` → `201 {id,email,role}` (role defaults to `user`) |
| `POST` | `/auth/login` | open | `{email,password}` → `200 {token,expires_at,role}` (JWT) |
| `POST` | `/auth/refresh` | bearer | `200 {token,expires_at}` |

Expert accounts are created with `role='expert'` out-of-band (v1: config-flagged admin seed). No public expert registration.

### 4.2 Sessions (`/api/v1/sessions`) — `user` role

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/sessions` | Start a timed session. Body `{duration_seconds,buffer_seconds}`. → `201 {id,expires_at,status:'open'}` |
| `GET` | `/sessions` | List the caller's sessions (paginated) |
| `GET` | `/sessions/:id` | One session, only if owned by caller. Includes image/question counts |
| `POST` | `/sessions/:id/close` | Manually close early. → `204` |

**`SessionWindow` middleware** guards upload + question-read paths: for `:session_id` in scope, checks `expires_at > now()` and `status='open'`; else `410 Gone` with `{"error":{"code":"session_expired"}}`.

### 4.3 Images / Uploads (`/api/v1/sessions/:id/images`) — `user` role, within session window

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/sessions/:id/images` | `multipart/form-data`, field `image`. Validates mime (`image/jpeg`/`image/png`/`image/webp`) + size cap. Inserts `images` row (raw bytes), inserts `jobs` row (`pending`), commits, `NOTIFY jobs_new`. → `202 {image_id,job_id}`. **Does not block on the pipeline.** |
| `GET` | `/sessions/:id/images` | List the session's images with their `jobs.status` (client polling surface) |

### 4.4 Questions (`/api/v1/questions`) — both roles, behavior splits by role

| Method | Path | Role | Behavior |
|---|---|---|---|
| `GET` | `/questions` | `user` | The user's session answers. Query: `session_id` (required), `status` (optional), `page`/`per_page`. **User-facing shape** (see 4.5): `number,question,choices[],answers:[{id,value}],status,confidence`. **No `explanation`.** |
| `GET` | `/questions` | `expert` | Moderation queue. Query: `status=moderation|error` (default `moderation`), `tag` (optional), `page`/`per_page`. **Expert-facing shape**: full fields incl. `explanation`, `tags[]`, `image_id`, `has_verification_report`. |
| `GET` | `/questions/:id` | `user` | One question, only if linked to the caller's session. User-facing shape. |
| `GET` | `/questions/:id` | `expert` | One question, any. Expert-facing shape + `image_id`. |
| `PATCH` | `/questions/:id` | `expert` only (`user` → `403`) | Body: `{status:'verified', answers?:[value,…], choices?:[…], explanation?, tags?:[…], confidence?}`. Overwrites in place. After commit, runs the image-byte cleanup check. |

### 4.5 Expert image access (`/api/v1/images`)

| Method | Path | Role | Behavior |
|---|---|---|---|
| `GET` | `/images/:id` | `expert` only | Serves `images.original` bytes with `Content-Type: images.mime`. `404` if bytes already cleaned. |
| `GET` | `/images/:id/verification-report` | `expert` only | Returns `images.verification_report` JSON, or `null` / `404` if none. |

### 4.6 Response shapes

**User-facing question** (no `explanation`, answers carry display ids):
```json
{
  "id": "uuid",
  "number": 1,
  "question": "Укажите, какие из данных формул соответствуют кислотам:",
  "multiple_correct": true,
  "choices": ["Fe(OH)₂","Cs₂O","HBr","Na₂CO₃","H₂SO₄"],
  "answers": [{"id":"C","value":"HBr"},{"id":"E","value":"H₂SO₄"}],
  "status": "moderation",
  "confidence": 0.85
}
```

`answers[*].id` is derived at read time: find the value's index in `choices`, then map index→id using `choice_labeling` (`0→A`, `0→1` for `number`). First-index wins on duplicate choice texts (known v1 edge case).

**Expert-facing question** (full):
```json
{
  "id": "uuid",
  "number": 1,
  "question": "...",
  "multiple_correct": true,
  "choices": ["..."],
  "answers": ["HBr","H₂SO₄"],          // value-only, as stored
  "choice_labeling": "letter",
  "confidence": 0.85,
  "explanation": "... [VERIFICATION FLAG] ...",
  "tags": ["ai-generated","chemistry"],
  "status": "moderation",
  "image_id": "uuid",
  "has_verification_report": true,
  "verified_at": null,
  "verified_by": null
}
```

### 4.7 Status codes

`201` (register/session create), `202` (upload accepted), `200` (other success), `204` (close session), `400` (validation), `401` (no/invalid token), `403` (wrong role), `404` (not found / bytes cleaned), `409` (conflict, e.g. duplicate email), `410` (session expired), `500` (server). The skill's internal `error` codes (`unreadable_image`, `partial_extraction`, `no_questions_found`) are never returned raw to users; they surface as `status:'error'` questions with a short message.

---

## 5. Pipeline + Worker

### 5.1 Queue mechanics (Postgres-as-queue)

**Worker lifecycle** (N workers, default 4, configurable):
- Each worker owns a dedicated PGX connection running `LISTEN jobs_new`.
- Claim loop: `SELECT id, image_id, session_id FROM jobs WHERE status='pending' ORDER BY queued_at LIMIT 1 FOR UPDATE SKIP LOCKED` inside a transaction; flip to `processing`, set `started_at=now()`, commit, run the pipeline.
- On empty queue: block on `NOTIFY`. The upload handler's `INSERT INTO jobs` fires `NOTIFY jobs_new`, waking a worker without a busy-loop.
- On completion: `UPDATE jobs SET status='done' OR 'failed', finished_at=now(), last_error=$err`.

**Reaper** (startup + every `reaper_interval`, default 60s):
- `UPDATE jobs SET status='pending', attempts=attempts+1 WHERE status='processing' AND started_at < now() - $stale_threshold` (default 10m).
- After `attempts >= max_queue_attempts` (default 3), a job is set to `failed` and not reclaimed. The `images` row remains for operator/expert inspection.

**Shutdown:** root `ctx` cancellation; workers finish the current job or abandon at the next cancellation point; in-flight rows are reclaimed on next startup.

### 5.2 Per-job pipeline

Inputs: `{job_id, image_id, session_id}`. Orchestration (the 10-step workflow, encoded):

```
Run(ctx, job):
  1. Load images.original bytes.
  2. Enhance: imageEnhancer.Enhance(original) -> enhanced bytes.
       Persist images.enhanced.
  3. Extract (Kimi, up to 3 attempts):
       for attempt in 1..extract_max_attempts:
         result, err = aiExtractor.Extract(ctx, enhanced)
         if ctx.Err(): return ctx.Err()              // don't retry on shutdown
         if err == nil and result.error.code not in {unreadable_image, no_questions_found}:
           break
       if all attempts failed (unreadable/no_questions):
         create ONE error-question placeholder:
           questions { status:'error', question:'', choices:'[]', answers:'[]',
                       confidence:0, choice_labeling:'letter', embedding:NULL }
           question_tags: ['ai-generated','needs-manual']
           session_questions { session_id, image_id, question_id, extracted_number:1, extracted_confidence:0 }
         commit; job -> 'done'; return
       if result.error.code == 'partial_extraction':
         proceed with result.questions[]; persist images.extraction_error = result.error (expert awareness)
  4. For each extracted question q:
       a. normalized = normalize(q.question); hash = sha256(normalized)
          embedding = aiEmbedder.Embed(ctx, q.question)
       b. Dedup (hybrid):
            hit = questionRepo.FindExact(hash)
            if not hit: hit = questionRepo.FindSemantic(embedding, threshold=0.92)
          if hit:
            link session_questions { session_id, image_id, hit.id, q.number, q.confidence }
            continue                                  // step 8 short-circuit
       c. New question:
            answers_values = [a.value for a in q.answers]            // value-only
            choice_labeling = infer(q.answers[*].id)                 // 'letter' | 'number'
            tags = ['ai-generated'] + q.tags                         // service injects ai-generated
            questions row { status:'moderation', ... , embedding }
            question_tags rows
            session_questions link
  5. Verify (DeepSeek, best-effort):
       new_questions = [q created this job]
       if new_questions is empty: skip verify           // all deduped; nothing to check
       else:
         verifyResult, err = aiVerifier.Verify(ctx, toSkillJSON(new_questions))
         if err != nil or verifyResult == nil:
           log; continue                                // non-critical; questions stay 'moderation' as extracted
         else:
           for each vq in verifyResult.questions:
             UPDATE questions SET confidence = vq.confidence,
                                  explanation = vq.explanation      // may carry [VERIFICATION FLAG]
             WHERE id = corresponding new question id
           UPDATE images SET verification_report = verifyResult._verification
  6. job -> 'done'
```

**Invariants encoded:**
- `partial_extraction` proceeds (no retry); `unreadable_image`/`no_questions_found` retry up to 3 then error-placeholder.
- Dedup short-circuits verification: if every question was an existing hit, no new rows exist, verify is skipped.
- `ai-generated` is always injected regardless of Kimi output.
- Answers stored value-only; `choice_labeling` inferred once at creation and never recomputed.
- Verification only mutates `confidence` + `explanation` — never `answers` (per the verify skill's "do NOT change the answers array" rule). A DeepSeek disagreement becomes a `[VERIFICATION FLAG]` block in `explanation`, expert-facing.

### 5.3 AI client ports (narrow interfaces for testability)

```
ImageEnhancer  Enhance(ctx, raw []byte) (enhanced []byte, err error)
AIExtractor    Extract(ctx, image []byte) (ExtractResult, error)        // Kimi K2.7 vision
AIVerifier     Verify(ctx, questions []QuestionJSON) (VerifyResult, error)  // DeepSeek V4 Pro
AIEmbedder     Embed(ctx, text string) ([]float32, error)               // semantic dedup
```

- **`ImageEnhancer`** — contrast enhancement for text readability. v1 default: global contrast stretch + mild sharpen over Go `image` stdlib. Config-tunable; the least-specified piece, flagged for iteration.
- **`AIExtractor`** — wraps Kimi K2.7 chat-completions-with-vision. System prompt = `extract-questions-from-image` skill content (loaded via `embed.FS` at startup). Request: base64 image + skill system prompt. Response parsed into `ExtractResult{ Questions[], Error }`.
- **`AIVerifier`** — wraps DeepSeek V4 Pro chat (text-only). System prompt = `verify-extracted-questions` skill content. Request: extracted questions JSON. Response parsed into `VerifyResult{ _verification, Questions[] }`.
- **`AIEmbedder`** — OpenAI-compatible `text-embedding-3-small` by default (config point), 1536-dim float32. Used only for new (non-deduplicated) questions.

All four are injected into the pipeline at construction; tests use in-memory fakes. Each carries its own timeout (config) and respects the shared `ctx`.

### 5.4 Configuration surface

| Group | Key | Default |
|---|---|---|
| `server` | `addr`, `read_timeout`, `write_timeout`, `shutdown_timeout` | `:8080`, `15s`, `120s`, `30s` |
| `postgres` | `dsn`, `max_conns`, `min_conns` | env, 20, 4 |
| `jwt` | `secret`, `access_ttl`, `refresh_ttl` | env, `1h`, `7d` |
| `ai.kimi` | `base_url`, `api_key`, `model`, `timeout` | env, —, `kimi-k2.7`, `90s` |
| `ai.deepseek` | `base_url`, `api_key`, `model`, `timeout` | env, —, `deepseek-v4-pro`, `60s` |
| `ai.embedder` | `base_url`, `api_key`, `model`, `dim` | env, —, `text-embedding-3-small`, 1536 |
| `pipeline` | `extract_max_attempts`, `semantic_threshold`, `reaper_interval`, `stale_threshold`, `max_queue_attempts` | `3`, `0.92`, `60s`, `10m`, `3` |
| `workers` | `count` | `4` |
| `upload` | `max_bytes`, `allowed_mimes` | `10MiB`, `[image/jpeg,image/png,image/webp]` |

Loaded from `config.yaml` + env override. Secrets always via env.

---

## 6. Project Layout

```
cmd/
  coeus/
    main.go                 // wire deps, start Gin + workers, graceful shutdown
internal/
  config/                   // config.yaml + env override; typed Config struct
    config.go
    config.yaml             // committed defaults; secrets via env
  domain/                   // pure types, no I/O deps
    question.go             // Question, Answer, Tag, Status consts
    session.go              // Session, Image, Job
    errors.go               // typed domain errors (ErrNotFound, ErrSessionExpired, …)
  storage/                  // PGX implementations of the ports
    postgres/
      pool.go
      migrations/           // *.sql, embedded via embed.FS, applied on boot
      question_repo.go      // QuestionRepo: exact + semantic lookup, upsert, update
      session_repo.go
      image_repo.go         // bytea load/persist/cleanup
      job_queue.go          // JobQueue: enqueue, claim (FOR UPDATE SKIP LOCKED), reaper
      user_repo.go
  ai/                       // external AI client impls behind ports
    kimi/                   // AIExtractor impl (vision)
    deepseek/               // AIVerifier impl (text)
    embedder/               // AIEmbedder impl (OpenAI-compatible)
    enhance/                // ImageEnhancer impl (contrast/sharpen)
    skills.go               // loads skills/*.md as system prompts from embed.FS
  pipeline/                 // the orchestration (5.2)
    pipeline.go             // Run(ctx, job)
    ports.go                // the four interface ports + ExtractResult/VerifyResult
  httpapi/                  // Gin layer, thin
    server.go               // router wiring, middleware chain
    middleware.go           // Auth, RoleGuard, SessionWindow, RequestLog, Recover
    handlers/
      auth.go
      sessions.go
      images.go
      questions.go          // role-split GET /questions, GET /:id, PATCH /:id
      expert.go             // GET /images/:id, GET /images/:id/verification-report
    dto/
      requests.go
      responses.go          // user shape {id,value} answers, no explanation;
                            // expert shape full fields + image_id + report flag
  auth/                     // JWT issue/verify, password hash (bcrypt)
    jwt.go
    password.go
  app/                      // composition root: Config -> repos -> ai -> pipeline -> handlers
    wire.go
skills/                     // committed skill content, read at startup (embed.FS)
  extract-questions-from-image/SKILL.md
  verify-extracted-questions/SKILL.md
docs/samples/               // unchanged
go.mod                      // module github.com/<owner>/coeus
```

Each directory is one responsibility, small enough to hold in context and edit reliably. `domain` is pure (no I/O imports). `pipeline` depends only on the four ports + `domain` + `storage` interfaces — fully testable with fakes. The Gin layer never touches PGX or AI directly; it goes through `app`-wired services. Skills are embedded via `go:embed`, so a skill edit doesn't require a code change but does require a rebuild.

---

## 7. Observability

- **Structured logging:** `log/slog` (stdlib, Go 1.21+). JSON to stdout in prod, text in dev. Every handler and pipeline step logs with `job_id`/`session_id`/`image_id`/`question_id` correlation keys. AI calls log model, latency, attempt number, and outcome.
- **Request IDs:** middleware mints a UUID per request, sets `X-Request-Id` response header, and injects it into the slog context. AI-call logs carry the originating request id so a slow Kimi call is traceable back to the upload.
- **Health endpoints:**
  - `GET /healthz` — liveness: `200` if process up.
  - `GET /readyz` — readiness: `200` if Postgres reachable + at least one worker alive.
- **No Prometheus / OpenTelemetry in v1** — flagged for later. The request-id + slog correlation is enough for a single-binary service.
- **No silent drops:** image-byte cleanup, verification-best-effort skip, and partial-extraction proceed all emit explicit log lines.

---

## 8. Error Handling

- **Domain errors** (`internal/domain/errors.go`): typed — `ErrNotFound`, `ErrSessionExpired`, `ErrDuplicate`, `ErrValidation`, `ErrUnauthorized`, `ErrForbidden`, `ErrAIUnavailable`. Each carries context (`With("question_id", …)`). Handlers map them to HTTP status via a single `errors.As` switch. No `fmt.Errorf` in domain code; only at the storage/AI boundary where it is wrapped: `fmt.Errorf("kimi extract: %w", err)`.
- **AI error wrapping:** every AI port impl wraps its underlying HTTP/transport error with the call name (`kimi.Extract`, `deepseek.Verify`, `embedder.Embed`) so logs/readiness identify which model is down.
- **Panic safety:** Gin's `Recovery` middleware + a pipeline-level `recover` that converts a panic into a `failed` job (captured in `jobs.last_error`) so one bad image cannot kill a worker.
- **Client vs. ops error surface:** clients see `{"error":{"code","message"}}`; ops see the typed error + context via slog. Skill error codes are never returned raw to users.

---

## 9. Testing Strategy

- **Unit (no external deps):**
  - `pipeline` — the highest-value target. Fake all four ports; table-driven cases: full happy path; exact dedup hit (skips verify); semantic dedup hit; `unreadable_image` ×3 → error placeholder; `partial_extraction` → proceeds; DeepSeek failure → questions still saved as `moderation`; verification success applies confidence/explanation updates; `ai-generated` injected; answers value-only; `choice_labeling` inferred. Asserts resulting `questions`/`session_questions`/`images` rows via a fake repo.
  - `domain` — pure type + error-mapping logic.
  - `httpapi/dto` — user-vs-expert projection (answers become `{id,value}`, explanation stripped for users).
  - `auth` — JWT issue/verify/expire; bcrypt hash/verify.
- **Storage (Testcontainers Postgres + pgvector):** repos against a real Postgres. Specifically: exact match by hash; semantic match by cosine above threshold; `FOR UPDATE SKIP LOCKED` claim under concurrent workers (spawn N goroutines, assert each job claimed exactly once); reaper reclaims a stale row; image-byte cleanup on last-question-verified.
- **HTTP (`httptest` + fake services):** routing, role gating (`403` for `user` on `PATCH /questions/:id`), `SessionWindow` `410`, status-code mapping, DTO projection. AI/storage faked at the service seam.
- **AI client smoke (opt-in, `go test -tags=smoke`):** build-tag-gated test that actually calls Kimi/DeepSeek/embedder against a sample input, asserting parsed shape matches the skill contracts. Not in CI by default; needs keys. Uses `docs/samples` inputs.
- **Coverage target:** none hard-coded. Must-cover paths: pipeline orchestration, storage concurrency, DTO projection.

---

## 10. Graceful Shutdown & Lifecycle

`main.go`:
1. Run DB migrations on boot (embedded SQL, idempotent).
2. Start N worker goroutines (each with its own PGX connection for LISTEN).
3. Start the reaper ticker.
4. Start Gin.
5. On `SIGINT`/`SIGTERM`: cancel root `ctx` → Gin stops accepting (drains in-flight HTTP up to `read_timeout`) → workers finish current job or abandon at next cancellation point → PGX pool closes → exit. Force-exit if `shutdown_timeout` (default 30s) is exceeded.

---

## 11. Open / Deferred Items

- Update `skills/extract-questions-from-image/SKILL.md` to emit per-question `tags[]` (subject tags). The service code is written against this extended contract; until the skill is updated, the pipeline treats `tags` as possibly-empty and still injects `ai-generated`.
- Expert account provisioning (v1: config-flagged admin seed).
- Prometheus `/metrics` + OpenTelemetry tracing.
- WebSocket/SSE push (polling is v1).
- Fuzzing of AI JSON parsing.
- Duplicate-choice-text disambiguation for value→id display (first-index wins in v1).
- Tuning `semantic_threshold` against real re-photographed exam data.
- Replacing `bytea` with S3 / queue with Redis when scale demands it (localized swaps).

---

## 12. Glossary

- **Canonical question** — a row in `questions`; the deduplicated, expert-verified knowledge base entry for one exam question. Many `session_questions` links may point to it.
- **Error placeholder** — a `questions` row at `status='error'` created when Kimi cannot read an image after 3 attempts; an expert fills it in and promotes to `verified`.
- **Verification report** — DeepSeek's `_verification` object, stored on `images.verification_report`, expert-facing only.
- **Choice labeling** — per-question `'letter'` or `'number'`, inferred from Kimi's answer ids, used to render display ids (`A)`, `1.`) at read time without storing ids in the shuffle-safe `answers` array.
