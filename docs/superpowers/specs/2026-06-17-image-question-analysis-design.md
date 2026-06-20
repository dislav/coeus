# Image Question Analysis Service — Design Specification

| Field | Value |
|-------|-------|
| **Title** | Image Question Analysis Service |
| **Date** | 2026-06-17 |
| **Status** | Approved |
| **Author** | Engineering Team |
| **Version** | 1.0 |

---

## 1. Overview

The Image Question Analysis Service is a Go-based backend system that accepts images of quiz or test questions, extracts structured question-and-answer data using a vision-capable AI model, verifies the extracted answers with a second text-only AI model, and routes the results through an expert moderation workflow. The service is designed as the minimal viable product (MVP) for an AI-assisted study and test-prep platform.

The core user journey is:

1. An authenticated user creates and starts a timed session.
2. The user uploads one or more images during the active session.
3. Each image is enhanced, analyzed by a vision model, and converted into structured question records.
4. Extracted answers are verified by a second model.
5. Experts review and approve questions, at which point the source image data is removed.

---

## 2. Requirements

### 2.1 Functional Requirements

| ID | Requirement |
|----|-------------|
| FR-1 | Authenticated users with `has_access=true` may create sessions, start sessions, upload images, and view questions for their own sessions. |
| FR-2 | Sessions are created in a `created` state and must be explicitly started; once started, a countdown timer enforces expiry. |
| FR-3 | Users may upload images in common formats (JPEG, PNG, WebP) via multipart upload during an active session. |
| FR-4 | Uploaded images are enhanced (contrast adjustment) before being sent to the vision model. |
| FR-5 | The vision model extracts questions, answer choices, correct answers, confidence scores, and explanations as structured JSON. |
| FR-6 | Extracted questions are normalized and matched against existing verified questions to avoid duplication. |
| FR-7 | New questions are sent to a verifier model for a second-pass answer check. |
| FR-8 | Questions are stored with status `processing`, then transition to `moderation`, `verified`, or `error`. |
| FR-9 | Experts (`role='expert'`) may list questions pending moderation, view source images, and update question status. |
| FR-10 | When all questions derived from an image are marked `verified`, the original and enhanced image bytea data is deleted from the database. |
| FR-11 | Session expiry is enforced automatically; expired sessions reject further uploads. |

### 2.2 Non-Functional Requirements

| ID | Requirement |
|----|-------------|
| NFR-1 | **Language & Runtime:** Go 1.25+ with the Gin web framework. |
| NFR-2 | **Database:** PostgreSQL, accessed via PGX v5 pool. |
| NFR-3 | **Background Jobs:** River v0.39.0 for durable, transactional job processing. |
| NFR-4 | **Rate Limiting:** Per-user RPM limiting via `ulule/limiter`. |
| NFR-5 | **Observability:** Structured JSON logging, Prometheus metrics, and health/readiness endpoints. |
| NFR-6 | **Testing:** Layered tests with real Postgres for repositories, mocks for services, and httptest for handlers. |
| NFR-7 | **Security:** Session-based auth, expert role checks, and sensitive image cleanup after verification. |
| NFR-8 | **Configurability:** 12-factor environment-based configuration. |

---

## 3. Data Model

### 3.1 Entity-Relationship Summary

- A `user` may have many `sessions`.
- A `session` may have many `images` and many `questions`.
- An `image` belongs to one `session` and may produce many `questions`.
- A `question` belongs to one `image` and one `session`.

> **Answer format design:** The `answers` column stores only the correct answer **values** as a JSON array of strings — e.g., `["автомобильный рынок"]` or `["верно все"]`. This is the canonical, shuffle-independent form. User-facing API responses assemble the display format dynamically by matching each answer value against the `choices` array to find the corresponding choice label (ID), then returning the combined string: `"а) автомобильный рынок"`. The AI model returns `{id, value}` objects during extraction, but only the `value` is persisted; the `id` is used for verification and then discarded.

### 3.2 SQL Schema

```sql
CREATE TYPE user_role AS ENUM ('user', 'expert');

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    has_access    BOOLEAN NOT NULL DEFAULT false,
    role          user_role NOT NULL DEFAULT 'user',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE session_status AS ENUM ('created', 'active', 'expired', 'closed');

CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    duration    INTERVAL NOT NULL,
    buffer      INTERVAL NOT NULL DEFAULT '5 minutes',
    started_at  TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,            -- started_at + duration (stops uploads)
    closed_at   TIMESTAMPTZ,            -- started_at + duration + buffer (hard close)
    status      session_status NOT NULL DEFAULT 'created',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE images (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    image_data      BYTEA,              -- nullable; NULL after cleanup
    enhanced_data   BYTEA,              -- nullable; NULL after cleanup
    mime_type       TEXT NOT NULL,
    file_name       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'processing',
    cleaned_at      TIMESTAMPTZ,        -- set when bytes are deleted
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT images_data_not_null_at_insert CHECK (
        image_data IS NOT NULL AND enhanced_data IS NOT NULL
    )
);
CREATE INDEX idx_images_session ON images(session_id);

CREATE TYPE question_status AS ENUM ('processing', 'moderation', 'verified', 'error');

CREATE TABLE questions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    image_id            UUID NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    session_id          UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    number              INT NOT NULL,
    question_text       TEXT NOT NULL,
    normalized_text     TEXT NOT NULL,
    choices             JSONB NOT NULL DEFAULT '[]',
    answers             JSONB NOT NULL DEFAULT '[]',
    multiple_correct    BOOLEAN NOT NULL DEFAULT false,
    confidence          REAL NOT NULL DEFAULT 0.0,
    explanation         TEXT,
    status              question_status NOT NULL DEFAULT 'processing',
    matched_question_id UUID REFERENCES questions(id),  -- set when dedup matched
    ai_analysis         JSONB,
    verification_report JSONB,
    tags                TEXT[] NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_question_number UNIQUE(image_id, number)
);
CREATE INDEX idx_questions_image ON questions(image_id);
CREATE INDEX idx_questions_session ON questions(session_id);
CREATE INDEX idx_questions_normalized ON questions(normalized_text);
CREATE INDEX idx_questions_status ON questions(status);

-- Auto-update updated_at on row modification
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_questions_updated_at
    BEFORE UPDATE ON questions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
```

### 3.3 Domain Types (Go)

```go
package domain

type User struct {
    ID           uuid.UUID
    Email        string
    PasswordHash string
    HasAccess    bool
    Role         UserRole
    CreatedAt    time.Time
}

type Session struct {
    ID        uuid.UUID
    UserID    uuid.UUID
    Duration  time.Duration
    Buffer    time.Duration
    StartedAt *time.Time
    ExpiresAt *time.Time
    ClosedAt  *time.Time
    Status    SessionStatus
    CreatedAt time.Time
}

type Image struct {
    ID           uuid.UUID
    SessionID    uuid.UUID
    ImageData    *[]byte  // nil after cleanup
    EnhancedData *[]byte  // nil after cleanup
    MimeType     string
    FileName     string
    Status       string
    CleanedAt    *time.Time
    CreatedAt    time.Time
}

type Question struct {
    ID                 uuid.UUID
    ImageID            uuid.UUID
    SessionID          uuid.UUID
    Number             int
    QuestionText       string
    NormalizedText     string
    Choices            []Choice
    Answers            []string  // stored as value texts: ["автомобильный рынок"]
    MultipleCorrect    bool
    Confidence         float32
    Explanation        *string
    Status             QuestionStatus
    MatchedQuestionID  *uuid.UUID  // set when dedup matched
    AIAnalysis         json.RawMessage
    VerificationReport json.RawMessage
    Tags               []string
    CreatedAt          time.Time
    UpdatedAt          time.Time
}

type Choice struct {
    Label string `json:"label"`
    Text  string `json:"text"`
}
```

### 3.4 Enums

```go
package enums

type UserRole string

const (
    UserRoleUser   UserRole = "user"
    UserRoleExpert UserRole = "expert"
)

type SessionStatus string

const (
    SessionStatusCreated SessionStatus = "created"
    SessionStatusActive  SessionStatus = "active"
    SessionStatusExpired SessionStatus = "expired"
    SessionStatusClosed  SessionStatus = "closed"
)

type QuestionStatus string

const (
    QuestionStatusProcessing QuestionStatus = "processing"
    QuestionStatusModeration QuestionStatus = "moderation"
    QuestionStatusVerified   QuestionStatus = "verified"
    QuestionStatusError      QuestionStatus = "error"
)
```

### 3.5 Status Transitions

**Session status state machine:**

```
created ──► active ──► expired ──► closed
               │
               └──────────► closed (user terminates early)
```

| Transition | Trigger | Allowed? |
|-----------|---------|----------|
| `created` → `active` | User calls `POST /start` | Yes |
| `active` → `expired` | Clock reaches `expires_at` | Yes (automatic) |
| `expired` → `closed` | Clock reaches `closed_at` | Yes (automatic) |
| `active` → `closed` | User terminates session | Future |
| Any → `created` | — | No (immutable once started) |
| `closed` → any | — | No (terminal state) |

**Question status state machine:**

```
processing ──► moderation ──► verified
     │              │
     └──► error ────┘
```

| Transition | Trigger | Who |
|-----------|---------|-----|
| `processing` → `moderation` | DeepSeek verification complete (or skipped on match) | System |
| `processing` → `error` | Kimi extraction exhausted all retries | System |
| `moderation` → `verified` | Expert approves question | Expert |
| `moderation` → `error` | Expert rejects question | Expert |
| `error` → `moderation` | Expert re-submits for reprocessing | Expert |
| `verified` → any | — | No (terminal) |

> Question `verified` is a terminal state. Once verified, a question cannot be moved back.

---

## 4. API Design

### 4.1 Endpoint Summary

| Method | Path | Auth | Expert | Description |
|--------|------|------|--------|-------------|
| POST | `/api/auth/login` | no | no | Authenticate and create session |
| POST | `/api/auth/logout` | yes | no | Destroy current session |
| POST | `/api/sessions` | yes | no | Create a new session |
| POST | `/api/sessions/:id/start` | yes | no | Start the session clock |
| GET | `/api/sessions/:id` | yes | no | Get session info and remaining time |
| POST | `/api/sessions/:id/images` | yes | no | Upload an image |
| GET | `/api/sessions/:id/questions` | yes | no | List questions for the user's session (all statuses) |
| GET | `/api/questions` | yes | no | List user's own questions, filterable by `?status=` |
| GET | `/api/questions` | yes | yes | Experts: list *all* questions (with `?status=` filter) |
| GET | `/api/questions/:id` | yes | no | Get a single question with answers (user: own only) |
| PATCH | `/api/questions/:id` | yes | yes | Update a question's status (expert only) |
| GET | `/api/questions/:id/image` | yes | yes | Retrieve the original image (410 Gone after cleanup) |
| GET | `/health` | no | no | Health check (liveness) |
| GET | `/ready` | no | no | Readiness check (DB + River) |
| GET | `/debug/river` | no | no | River inspector (internal use) |
| GET | `/metrics` | no | no | Prometheus metrics |

### 4.2 Request/Response Examples

#### Login

```http
POST /api/auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "secret"
}
```

```http
200 OK
Set-Cookie: session=...
Content-Type: application/json

{
  "user_id": "550e8400-e29b-41d4-a716-446655440000",
  "email": "user@example.com",
  "has_access": true,
  "role": "user"
}
```

> **MVP note:** User accounts are pre-seeded by administrators. There is no self-registration endpoint. Password comparison is done against a `password_hash` column (add to `users` table — see schema).

#### Logout

```http
POST /api/auth/logout
```

```http
200 OK
```

#### Create Session

```http
POST /api/sessions
Content-Type: application/json

{
  "duration_minutes": 60,
  "buffer_minutes": 5
}
```

```http
201 Created
Content-Type: application/json

{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "created",
  "duration": "1h0m0s",
  "buffer": "5m0s",
  "created_at": "2026-06-17T10:00:00Z"
}
```

#### Start Session

```http
POST /api/sessions/550e8400-e29b-41d4-a716-446655440000/start
```

```http
200 OK
Content-Type: application/json

{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "active",
  "started_at": "2026-06-17T10:05:00Z",
  "expires_at": "2026-06-17T11:05:00Z"
}
```

#### Upload Image

```http
POST /api/sessions/550e8400-e29b-41d4-a716-446655440000/images
Content-Type: multipart/form-data

image: <binary>
```

```http
202 Accepted
Content-Type: application/json

{
  "id": "660e8400-e29b-41d4-a716-446655440001",
  "status": "processing",
  "file_name": "quiz_page_1.jpg",
  "mime_type": "image/jpeg"
}
```

#### List Session Questions (User View)

```http
GET /api/sessions/550e8400-e29b-41d4-a716-446655440000/questions
```

```http
200 OK
Content-Type: application/json

[
  {
    "id": "770e8400-e29b-41d4-a716-446655440002",
    "number": 1,
    "question_text": "What is the capital of France?",
    "choices": [
      { "label": "A", "text": "Paris" },
      { "label": "B", "text": "London" }
    ],
    "answers": ["A) Paris"],
    "multiple_correct": false,
    "confidence": 0.97,
    "explanation": "Paris is the capital of France.",
    "status": "verified",
    "tags": [],
    "updated_at": "2026-06-17T10:15:00Z"
  }
]
```

#### List Questions (Expert Filter)

```http
GET /api/questions?status=moderation
```

```http
200 OK
Content-Type: application/json

[
  {
    "id": "770e8400-e29b-41d4-a716-446655440002",
    "session_id": "550e8400-e29b-41d4-a716-446655440000",
    "image_id": "660e8400-e29b-41d4-a716-446655440001",
    "number": 1,
    "question_text": "What is the capital of France?",
    "choices": [
      { "label": "A", "text": "Paris" },
      { "label": "B", "text": "London" },
      { "label": "C", "text": "Berlin" },
      { "label": "D", "text": "Madrid" }
    ],
    "answers": ["A) Paris"],
    "status": "moderation",
    "confidence": 0.97
  }
]
```

#### Update Question Status (Expert)

```http
PATCH /api/questions/770e8400-e29b-41d4-a716-446655440002
Content-Type: application/json

{
  "status": "verified"
}
```

```http
200 OK
Content-Type: application/json

{
  "id": "770e8400-e29b-41d4-a716-446655440002",
  "status": "verified",
  "updated_at": "2026-06-17T10:30:00Z"
}
```

### 4.3 DTO Definitions (Go)

```go
package dto

// Auth
type LoginRequest struct {
    Email    string `json:"email" binding:"required,email"`
    Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
    UserID    uuid.UUID `json:"user_id"`
    Email     string    `json:"email"`
    HasAccess bool      `json:"has_access"`
    Role      enums.UserRole `json:"role"`
}

// Sessions
type CreateSessionRequest struct {
    DurationMinutes int `json:"duration_minutes" binding:"required,min=5,max=480"`
    BufferMinutes   int `json:"buffer_minutes" binding:"min=0,max=60"`
}

type CreateSessionResponse struct {
    ID        uuid.UUID          `json:"id"`
    Status    enums.SessionStatus `json:"status"`
    Duration  string             `json:"duration"`
    Buffer    string             `json:"buffer"`
    CreatedAt time.Time          `json:"created_at"`
}

type StartSessionResponse struct {
    ID        uuid.UUID          `json:"id"`
    Status    enums.SessionStatus `json:"status"`
    StartedAt time.Time          `json:"started_at"`
    ExpiresAt time.Time          `json:"expires_at"`
}

// Images
type UploadImageResponse struct {
    ID       uuid.UUID `json:"id"`
    Status   string    `json:"status"`
    FileName string    `json:"file_name"`
    MimeType string    `json:"mime_type"`
}

// Questions
// QuestionResponse — user-facing (answers formatted as "ID) value")
type QuestionResponse struct {
    ID              uuid.UUID            `json:"id"`
    SessionID       uuid.UUID            `json:"session_id,omitempty"`
    ImageID         uuid.UUID            `json:"image_id,omitempty"`
    Number          int                  `json:"number"`
    QuestionText    string               `json:"question_text"`
    Choices         []domain.Choice      `json:"choices"`
    Answers         []string             `json:"answers"`        // "A) Paris", "Г) верно все"
    MultipleCorrect bool                 `json:"multiple_correct"`
    Confidence      float32              `json:"confidence"`
    Explanation     *string              `json:"explanation,omitempty"`
    Status          enums.QuestionStatus `json:"status"`
    Tags            []string             `json:"tags"`
    Verification    json.RawMessage      `json:"verification,omitempty"`
    UpdatedAt       time.Time            `json:"updated_at"`
}

type UpdateQuestionStatusRequest struct {
    Status enums.QuestionStatus `json:"status" binding:"required,oneof=verified error moderation"`
}

type QuestionFilterRequest struct {
    Status string `form:"status"` // moderation, verified, error
}
```

### 4.4 Error Response Shape

All HTTP errors return a consistent JSON body:

```json
{
  "error": "session_expired",
  "message": "The session has expired. Please create a new session.",
  "details": {}
}
```

Common error codes:

| Code | HTTP | Meaning |
|------|------|---------|
| `unauthorized` | 401 | Missing or invalid authentication |
| `forbidden` | 403 | Insufficient privileges or session expired |
| `session_expired` | 403 | Session past expiry time |
| `not_found` | 404 | Resource does not exist |
| `bad_request` | 400 | Invalid input |
| `rate_limited` | 429 | Too many requests |
| `internal_error` | 500 | Unexpected server error |

---

## 5. Architecture

### 5.1 Package Structure

```
coeus/
├── cmd/
│   ├── api/main.go              # HTTP API server entrypoint
│   ├── worker/main.go           # River worker process entrypoint
│   └── migrate/main.go          # Database migration runner
├── internal/
│   ├── config/config.go         # Environment configuration
│   ├── db/db.go                 # PGX pool and migration helpers
│   ├── domain/
│   │   ├── user.go              # User entity
│   │   ├── session.go           # Session entity
│   │   ├── image.go             # Image entity
│   │   ├── question.go          # Question entity
│   │   └── enums/enums.go       # Status enums
│   ├── repository/
│   │   ├── user.go              # UserRepository
│   │   ├── session.go           # SessionRepository
│   │   ├── image.go             # ImageRepository
│   │   └── question.go          # QuestionRepository
│   ├── service/
│   │   ├── session.go           # SessionService
│   │   ├── image.go             # ImageService
│   │   ├── question.go          # QuestionService
│   │   ├── moderation.go        # ModerationService
│   │   ├── ai/client.go         # Generic AI client interface
│   │   ├── ai/kimi.go           # Kimi K2.6 integration
│   │   ├── ai/deepseek.go       # DeepSeek V4 Pro integration
│   │   ├── ai/enhance.go        # Image enhancement helpers
│   │   └── matcher/normalizer.go # Question deduplication
│   ├── handler/
│   │   ├── session.go           # Session handlers
│   │   ├── image.go             # Image handlers
│   │   ├── question.go          # Question handlers
│   │   └── dto/dto.go           # Request/response DTOs
│   ├── middleware/
│   │   ├── auth.go              # Authentication middleware
│   │   ├── expert.go            # Expert authorization middleware
│   │   ├── session.go           # Session validation middleware
│   │   └── ratelimit.go         # Rate limiting middleware
│   ├── queue/
│   │   ├── client.go            # River client setup
│   │   └── jobs/
│   │       ├── extract.go       # ExtractQuestionsJob
│   │       ├── verify.go        # VerifyQuestionsJob
│   │       └── scheduler.go     # Periodic job scheduler
│   └── prompts/prompts.go       # Embedded AI prompts
├── skills/
│   ├── extract-questions-from-image/SKILL.md
│   └── verify-extracted-questions/SKILL.md
├── migrations/                  # golang-migrate SQL files
├── go.mod
├── go.sum
└── Makefile
```

### 5.2 Component Responsibilities

| Component | Responsibility |
|-----------|----------------|
| `cmd/api` | Boots the Gin server, wires repositories, services, handlers, and middleware. |
| `cmd/worker` | Boots the River worker pool and registers job handlers. |
| `cmd/migrate` | Runs `golang-migrate` up/down commands. |
| `repository` | Encapsulates all SQL access; returns domain models, accepts `pgx.Tx` for transactional tests. |
| `service` | Contains business logic, orchestrates repositories and AI clients, and enforces invariants. |
| `handler` | HTTP adapters: bind JSON/multipart input, call services, render responses. |
| `middleware` | Cross-cutting concerns: auth, expert checks, rate limiting, session state validation. |
| `queue` | River client configuration and job definitions. |
| `prompts` | Embeds `skills/` prompt files into the binary with `//go:embed`. |

### 5.3 Service Interfaces

```go
package service

type SessionService interface {
    Create(ctx context.Context, userID uuid.UUID, duration, buffer time.Duration) (*domain.Session, error)
    Start(ctx context.Context, id uuid.UUID) (*domain.Session, error)
    Get(ctx context.Context, id uuid.UUID) (*domain.Session, error)
}

type ImageService interface {
    Upload(ctx context.Context, sessionID uuid.UUID, file io.Reader, filename, mimeType string) (*domain.Image, error)
}

type QuestionService interface {
    ListBySession(ctx context.Context, sessionID uuid.UUID) ([]domain.Question, error)
    List(ctx context.Context, filter QuestionFilter) ([]domain.Question, error)
    Get(ctx context.Context, id uuid.UUID) (*domain.Question, error)
}

type ModerationService interface {
    UpdateStatus(ctx context.Context, id uuid.UUID, status enums.QuestionStatus) (*domain.Question, error)
    GetImage(ctx context.Context, questionID uuid.UUID) ([]byte, string, error)
}
```

---

## 6. AI Prompts & Response Schemas

The AI prompts are authored and versioned in the `skills/` directory. They are embedded into the Go binary at compile time via `//go:embed`.

### 6.1 Extraction Prompt

**Source:** `skills/extract-questions-from-image/SKILL.md` (186 lines)

**Usage:** Sent to Kimi K2.6 along with a base64-encoded enhanced image URL. The prompt instructs the model to:
- Identify quiz/exam questions in the image
- Extract question text verbatim
- List answer choices (stripping label prefixes like `"A) "`)
- Identify correct answers (bare label IDs: `"A"`, not `"A)"`)
- Provide per-question confidence scores
- Handle multiple-correct questions
- Return errors for unreadable images or partial extraction

**Expected response format:**
```json
{
  "questions": [
    {
      "number": 1,
      "question": "What is the capital of France?",
      "multiple_correct": false,
      "choices": ["Paris", "London", "Berlin", "Madrid"],
      "answers": [
        { "id": "A", "value": "Paris" }
      ],
      "confidence": 0.97,
      "explanation": "The image clearly shows..."
    }
  ]
}
```

**Error response format:**
```json
{
  "error": {
    "code": "partial_extraction",
    "message": "Only 3 of 5 expected questions could be extracted",
    "details": "...",
    "questions_extracted": 3,
    "questions_expected": 5
  },
  "questions": [...]
}
```

**Error codes:** `unreadable_image`, `partial_extraction`, `no_questions_found`

### 6.2 Verification Prompt

**Source:** `skills/verify-extracted-questions/SKILL.md` (273 lines)

**Usage:** Sent to DeepSeek V4 Pro as a text-only prompt. The extracted question JSON is included inline. The prompt instructs the model to:
1. Validate structural completeness (required keys, ID-to-choice mapping)
2. Re-solve questions and flag answer disagreements (NEVER modify the `answers` array)
3. Re-evaluate confidence based on internal consistency
4. Detect garbled text / OCR artifacts

**Input format:**
```json
{
  "questions": [
    {
      "number": 1,
      "question": "What is the capital of France?",
      "multiple_correct": false,
      "choices": ["Paris", "London", "Berlin", "Madrid"],
      "answers": [{ "id": "A", "value": "Paris" }],
      "confidence": 0.97,
      "explanation": ""
    }
  ]
}
```

**Output format (adds `_verification` envelope):**
```json
{
  "questions": [...],
  "_verification": {
    "timestamp": "2026-06-17T10:05:00Z",
    "structural_fixes": [...],
    "answers_flagged": [
      {
        "question_number": 3,
        "extracted_answer": "B",
        "verifier_answer": "C",
        "reason": "The formula in question 3..."
      }
    ],
    "confidence_adjustments": [...],
    "garbled_text_detected": [...],
    "summary": "3 questions verified, 1 flagged for review"
  }
}
```

**Key constraint:** The verifier must NEVER modify `questions.*.question`, `*.choices`, or `*.answers` — only flag issues.

### 6.3 Prompt Operations in Code

The `internal/prompts/prompts.go` package loads and templates prompts:

```go
package prompts

import "embed"

//go:embed ../../../skills/extract-questions-from-image/*.md ../../../skills/verify-extracted-questions/*.md
var skillFiles embed.FS

func ExtractPrompt() string {
    data, _ := skillFiles.ReadFile("skills/extract-questions-from-image/SKILL.md")
    return string(data)
}

func VerifyPrompt(questionsJSON string) string {
    tpml, _ := skillFiles.ReadFile("skills/verify-extracted-questions/SKILL.md")
    return fmt.Sprintf("%s\n\n```json\n%s\n```", string(tpml), questionsJSON)
}
```

---

## 7. Data Flow

### 7.1 Sequence Diagram (Textual)

```
User -> API: POST /api/sessions
API -> SessionService: Create session
SessionService -> Repository: insert session
API -> User: session created

User -> API: POST /api/sessions/:id/start
API -> SessionService: Start session
SessionService -> Repository: update status=active, set expires_at
API -> User: session active

User -> API: POST /api/sessions/:id/images
API -> ImageService: Upload image
ImageService -> Enhancer: enhance image bytes
ImageService -> Repository: insert image (status=processing)
ImageService -> Queue: enqueue ExtractQuestionsJob
API -> User: image accepted

Worker -> Queue: ExtractQuestionsJob
Worker -> Kimi: extract-questions-from-image prompt
Kimi -> Worker: structured JSON (with {id, value} objects)
Worker -> Service: extract only answer values, discard IDs
Worker -> Matcher: normalize and deduplicate
Worker -> Repository: insert questions (status=processing, answers stored as values only)
Worker -> Queue: enqueue VerifyQuestionsJob per new question

Worker -> Queue: VerifyQuestionsJob
Worker -> DeepSeek: verify-extracted-questions prompt
DeepSeek -> Worker: verification report
Worker -> Repository: update question (status=moderation, verification_report)

Expert -> API: GET /api/questions?status=moderation
API -> QuestionService: list pending
API -> Expert: moderation queue

Expert -> API: PATCH /api/questions/:id {status: verified}
API -> ModerationService: update status
ModerationService -> Repository: update question
ModerationService -> Repository: if all image questions verified, delete image bytes
API -> Expert: updated question

User -> API: GET /api/sessions/:id/questions
API -> QuestionService: list questions for session (all statuses)
API -> User: questions with answers and statuses
```

### 7.2 Image Processing Pipeline

1. **Validation:** Reject if MIME type is not `image/jpeg`, `image/png`, or `image/webp`. Reject if file size exceeds `MAX_UPLOAD_SIZE_MB` (default 10 MB). Reject if either dimension exceeds `MAX_IMAGE_DIM` (default 4096 pixels). Return HTTP 422 with `bad_request` error code and descriptive message.
2. **Decode:** Use Go stdlib `image.Decode` (with `image/jpeg`, `image/png`, and `golang.org/x/image/webp` registered) to decode the source bytes.
3. **Enhancement** (using `disintegration/imaging`):
   - Resize so the long edge fits `MAX_IMAGE_DIM` using `imaging.Fit` with `imaging.Lanczos`
   - Adjust contrast: `imaging.AdjustContrast(img, 15)` — positive value increases contrast
   - Adjust gamma: `imaging.AdjustGamma(img, 0.85)` — values < 1 brighten dark areas
   - Sharpen: `imaging.Sharpen(img, 0.5)` — enhances text edges
   - Failure is non-retryable; reject the upload on enhancement failure (e.g., invalid image data).

   ```go
   func Enhance(img image.Image, maxDim int) image.Image {
       img = imaging.Fit(img, maxDim, maxDim, imaging.Lanczos)
       img = imaging.AdjustContrast(img, 15)
       img = imaging.AdjustGamma(img, 0.85)
       img = imaging.Sharpen(img, 0.5)
       return img
   }
   ```
4. **Encode:** Re-encode the enhanced image as PNG (lossless, preserves text clarity) into a byte buffer.
5. **Storage:** Persist both original bytes and enhanced PNG bytes in `images.image_data` and `images.enhanced_data`. Enqueue `ExtractQuestionsJob` transactionally.
6. **Cleanup:** When the last question derived from an image is marked `verified`, set `image_data = NULL`, `enhanced_data = NULL`, and `cleaned_at = now()` in the same transaction. The columns are nullable to allow this. After cleanup, `GET /api/questions/:id/image` returns HTTP 410 Gone.

---

## 8. Job Processing

### 8.1 Job Definitions

```go
package jobs

const (
    ExtractQuestionsJobName = "extract_questions"
    VerifyQuestionsJobName  = "verify_questions"
    SessionExpiryJobName    = "session_expiry"
)

type ExtractQuestionsArgs struct {
    ImageID uuid.UUID `json:"image_id"`
}

func (ExtractQuestionsArgs) Kind() string { return ExtractQuestionsJobName }

type VerifyQuestionsArgs struct {
    QuestionID uuid.UUID `json:"question_id"`
}

func (VerifyQuestionsArgs) Kind() string { return VerifyQuestionsJobName }

type SessionExpiryArgs struct{}

func (SessionExpiryArgs) Kind() string { return SessionExpiryJobName }
```

### 8.2 Retry Strategy

| Job | Max Attempts | Backoff | Failure Behavior |
|-----|--------------|---------|------------------|
| `ExtractQuestionsJob` | 3 | Exponential: 5s → 15s → 45s | Final failure creates a single `questions` row with `status=error` and `ai_analysis` containing the error details for manual review. |
| `VerifyQuestionsJob` | 1 | N/A | Intentional: DeepSeek verification is non-critical. Any failure (network, timeout, rate limit) promotes the question to `status=moderation` with the error recorded in `verification_report`. Retries are not needed because Kimi's extraction alone is sufficient for moderation queue entry. |
| `SessionExpiryJob` | — | Periodic (every 60s) | Scans for sessions past `expires_at` and transitions them to `expired`. |

### 8.3 Worker Configuration

```go
package queue

func NewClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
    return river.NewClient(riverpgxv5.New(pool), &river.Config{
        Queues: map[string]river.QueueConfig{
            river.QueueDefault: {MaxWorkers: cfg.RiverMaxWorkers},
        },
        Workers: river.NewWorkers(
            &jobs.ExtractQuestionsWorker{},
            &jobs.VerifyQuestionsWorker{},
            &jobs.SessionExpiryWorker{},
        ),
        PeriodicJobs: []*river.PeriodicJob{
            river.NewPeriodicJob(
                river.PeriodicInterval(60 * time.Second),
                func() (river.JobArgs, *river.InsertOptions) {
                    return jobs.SessionExpiryArgs{}, nil
                },
                &river.PeriodicJobOpts{RunOnStart: true},
            ),
        },
    })
}
```

### 8.4 Transactional Enqueueing

All job inserts use `InsertTx` within the same transaction as the corresponding database write (image insert → extract job, question insert → verify job). This guarantees at-least-once execution without orphaned records.

---

## 9. Error Handling

### 9.1 Error Modes by Component

| Component | Error Condition | Handling |
|-----------|-----------------|----------|
| **Session** | Session expired or not started | Return HTTP 403 with code `session_expired`. |
| **Session** | Upload after expiry | Rejected by `session` middleware before reaching handler. |
| **Kimi Extraction** | River retry exhausted | Insert `questions` row with `status=error`; expert queue for manual processing. |
| **Kimi Extraction** | Unparseable JSON | Retryable; treated as transient failure. |
| **DeepSeek Verify** | Any failure | Log error, record in `verification_report`, promote to `moderation`. |
| **Duplicate Match** | Normalized text matches existing verified question | Create new question row for the session; set `matched_question_id` to the existing question's ID; set status to the existing question's status; skip DeepSeek verification. User receives the previously verified answer immediately. |
| **Image Cleanup** | Transaction failure during verified-status update | Roll back status change; cleanup is retried on next expert approval. |
| **Rate Limit** | RPM exceeded | HTTP 429 with code `rate_limited`. |

### 9.2 Error Helpers

```go
package handler

type APIError struct {
    Code    string                 `json:"error"`
    Message string                 `json:"message"`
    Details map[string]interface{} `json:"details"`
}

func RespondError(c *gin.Context, status int, code, message string, details map[string]interface{}) {
    c.JSON(status, APIError{
        Code:    code,
        Message: message,
        Details: details,
    })
}
```

---

## 10. Testing Strategy

### 10.1 Test Layers

| Layer | Tooling | Approach |
|-------|---------|----------|
| **Repository** | `testify/suite`, real Postgres | Spin up a test database per run; truncate tables between tests; exercise real SQL. |
| **Service** | `testify/mock` | Mock repositories and AI clients; test orchestration and business rules. |
| **Handler** | `httptest`, mocked services | Verify routing, binding, status codes, and JSON shapes. |
| **AI Client** | Mock HTTP transport | Intercept OpenAI-compatible HTTP requests; return fixture responses. |
| **Matcher/Normalizer** | Table-driven tests | Target 100% coverage; test punctuation stripping, whitespace collapse, lowercase. |
| **Integration** | Real DB + test containers | End-to-end upload → extraction → verification → moderation flow. |

### 10.2 Repository Test Pattern

```go
func (s *SessionRepositorySuite) SetupTest() {
    _, err := s.db.Exec(context.Background(),
        "TRUNCATE TABLE sessions, images, questions, users RESTART IDENTITY CASCADE")
    s.Require().NoError(err)
}
```

### 10.3 AI Client Fixture Pattern

```go
transport := &mockRoundTripper{response: []byte(openAIFixture)}
httpClient := &http.Client{Transport: transport}
client := openai.NewClientWithConfig(openai.DefaultConfig("test-key"))
client.HTTPClient = httpClient
```

---

## 11. Configuration

All configuration is loaded from environment variables using `caarlos0/env`.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | no | `8080` | HTTP server port |
| `SESSION_SECRET` | yes | — | Cookie/session signing secret |
| `DATABASE_URL` | yes | — | Postgres connection string |
| `KIMI_API_KEY` | yes | — | API key for Kimi K2.6 |
| `KIMI_BASE_URL` | no | `https://api.moonshot.ai/v1` | Kimi API base URL |
| `KIMI_MODEL` | no | `kimi-k2-6` | Kimi model name |
| `DEEPSEEK_API_KEY` | yes | — | API key for DeepSeek |
| `DEEPSEEK_BASE_URL` | no | `https://api.deepseek.com` | DeepSeek API base URL |
| `DEEPSEEK_MODEL` | no | `deepseek-v4-pro` | DeepSeek model name |
| `RIVER_MAX_WORKERS` | no | `10` | Max River workers |
| `RATE_LIMIT_RPM` | no | `60` | Requests per minute per user |
| `MAX_UPLOAD_SIZE_MB` | no | `10` | Maximum upload size in MB |
| `MAX_IMAGE_DIM` | no | `4096` | Maximum image width/height in pixels |
| `LOG_LEVEL` | no | `info` | slog level |
| `LOG_FORMAT` | no | `json` | `json` or `text` |

### 11.1 Configuration Struct

```go
package config

type Config struct {
    Port             int           `env:"PORT" envDefault:"8080"`
    SessionSecret    string        `env:"SESSION_SECRET"`
    DatabaseURL      string        `env:"DATABASE_URL"`
    KimiAPIKey       string        `env:"KIMI_API_KEY"`
    KimiBaseURL      string        `env:"KIMI_BASE_URL" envDefault:"https://api.moonshot.ai/v1"`
    KimiModel        string        `env:"KIMI_MODEL" envDefault:"kimi-k2-6"`
    DeepSeekAPIKey   string        `env:"DEEPSEEK_API_KEY"`
    DeepSeekBaseURL  string        `env:"DEEPSEEK_BASE_URL" envDefault:"https://api.deepseek.com"`
    DeepSeekModel    string        `env:"DEEPSEEK_MODEL" envDefault:"deepseek-v4-pro"`
    RiverMaxWorkers  int           `env:"RIVER_MAX_WORKERS" envDefault:"10"`
    RateLimitRPM     int           `env:"RATE_LIMIT_RPM" envDefault:"60"`
    MaxUploadSizeMB  int           `env:"MAX_UPLOAD_SIZE_MB" envDefault:"10"`
    MaxImageDim      int           `env:"MAX_IMAGE_DIM" envDefault:"4096"`
    LogLevel         string        `env:"LOG_LEVEL" envDefault:"info"`
    LogFormat        string        `env:"LOG_FORMAT" envDefault:"json"`
}
```

---

## 12. Observability

### 12.1 Logging

- Use `log/slog` with JSON output in production.
- Include `trace_id`/`request_id` in all request-scoped logs.
- Log all AI calls with model name, token usage (when available), latency, and error.

```go
slog.InfoContext(ctx, "image uploaded",
    slog.String("image_id", imageID.String()),
    slog.String("session_id", sessionID.String()),
    slog.Int("size_bytes", len(data)),
)
```

### 12.2 Metrics

Prometheus metrics exposed at `/metrics`:

| Metric | Type | Labels |
|--------|------|--------|
| `questions_extracted_total` | Counter | `status=success\|error` |
| `questions_verified_total` | Counter | `status=success\|error` |
| `kimi_errors_total` | Counter | `phase=extract\|parse` |
| `deepseek_errors_total` | Counter | — |
| `images_uploaded_total` | Counter | `mime_type` |
| `active_sessions` | Gauge | `status` |
| `extraction_duration_seconds` | Histogram | `model` |

### 12.3 Health Checks

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Liveness; returns 200 if the process is running. |
| `GET /ready` | Readiness; verifies DB connectivity and River client health. |
| `GET /debug/river` | River built-in metrics and inspector (expert/internal access). |

---

## 13. Security

### 13.1 Authentication & Authorization

- **Session store:** Cookie-based via `gin-contrib/sessions/cookie`. The `SESSION_SECRET` environment variable provides the encryption key (must be at least 32 bytes).
- **User provisioning (MVP):** User accounts are manually created by administrators directly in the database or via a seed script. There is no self-registration endpoint. Each user has a `password_hash` (bcrypt), a `has_access` flag, and a `role` (`user` or `expert`).
- **Login:** `POST /api/auth/login` validates email/password against the users table, creates a session cookie.
- **Logout:** `POST /api/auth/logout` destroys the session.
- All endpoints except `/health`, `/ready`, `/debug/river`, `/metrics`, and auth endpoints require an authenticated session with `has_access=true`.
- Moderation endpoints (status updates, image retrieval) additionally require `role='expert'`.
- Middleware order: `auth` → `expert` (where required) → `ratelimit` → `session` validation.

### 13.2 Rate Limiting

- Per-user RPM limiting using `ulule/limiter` with an **in-memory store** (no Redis dependency for MVP).
- Requests are keyed by user ID (from session), falling back to client IP for unauthenticated requests.
- `RATE_LIMIT_RPM` defaults to 60 requests per minute per user.
- Returns HTTP 429 with code `rate_limited` when exceeded.
- Rate limit middleware is applied globally; rate-limit-reset headers are included in responses.

### 13.3 Data Cleanup

- Original and enhanced image bytes are deleted as soon as all questions derived from the image reach `verified` status.
- Deletion occurs in the same database transaction as the final status update to guarantee consistency.
- Question text, choices, and answers remain in the database for reuse and analytics.

### 13.4 Input Validation

- Image MIME type whitelist: `image/jpeg`, `image/png`, `image/webp`.
- Maximum upload size and image dimensions enforced before decoding.
- Image decoding uses `golang.org/x/image` for WebP support.

---

## 14. Future Work

The following items are intentionally out of scope for the MVP but are documented for future planning:

| Item | Description |
|------|-------------|
| **Payment-based access control** | Gate `has_access` on subscription or one-time payment status. |
| **Vector/semantic question matching** | Replace normalized text deduplication with embeddings and similarity search. |
| **AI-assigned tags during extraction** | Have Kimi assign subject/topic tags and store them in `questions.tags`. |
| **Separate moderation UI** | Build a dedicated frontend for experts instead of API-only moderation. |
| **Real-time job progress** | WebSocket or SSE updates for upload processing status. |
| **Multi-language support** | Detect and normalize questions in non-English languages. |

---

## 15. Appendix: Go Dependencies

### 14.1 go.mod (excerpt)

```go
module github.com/vlgrigoriev/coeus

go 1.25

require (
    github.com/caarlos0/env/v11 v11.0.0
    github.com/disintegration/imaging v1.6.2
    github.com/gin-contrib/sessions v1.1.0
    github.com/gin-gonic/gin v1.12.0
    github.com/golang-migrate/migrate/v4 v4.19.1
    github.com/google/uuid v1.6.0
    github.com/jackc/pgx/v5 v5.10.0
    github.com/prometheus/client_golang v1.19.0
    github.com/riverqueue/river v0.39.0
    github.com/riverqueue/river/riverdriver/riverpgxv5 v0.39.0
    github.com/sashabaranov/go-openai v1.41.2
    github.com/stretchr/testify v1.11.1
    github.com/ulule/limiter/v3 v3.11.2
    golang.org/x/image v0.42.0
)
```

### 14.2 Key Library Versions

| Library | Version | Purpose |
|---------|---------|---------|
| Go | 1.25+ | Runtime |
| Gin | v1.12.0 | HTTP framework |
| PGX | v5.10.0 | Postgres driver and pool |
| River | v0.39.0 | Background jobs |
| imaging | v1.6.2 | Image enhancement |
| go-openai | v1.41.2 | OpenAI-compatible AI clients |
| migrate | v4.19.1 | Schema migrations |
| sessions | v1.1.0 | Session management |
| limiter | v3.11.2 | Rate limiting |
| testify | v1.11.1 | Testing |
| x/image | v0.42.0 | WebP decoding |

---

*End of specification.*
