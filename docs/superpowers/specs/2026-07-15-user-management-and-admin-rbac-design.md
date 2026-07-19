# User Management & Admin RBAC — Design Specification

- **Date:** 2026-07-15
- **Status:** Approved
- **Service:** Coeus (Go, Gin, PostgreSQL + pgvector, single binary)
- **Migration:** `0006_user_management.sql`

## Overview

Coeus today recognizes two roles — `user` and `expert` — enforced as a plain string by a DB `CHECK` constraint. Authorization is a single-role `RoleGuard` that does exact string equality. There is no `admin` role, no way to administer users at runtime, no ability to invalidate an issued JWT without rotating the global signing secret, and no endpoint to delete questions.

This spec adds a third role (`admin`) that acts as a **superuser** over the existing expert capabilities **plus** full user management: list, update, delete, and reset-password. It introduces `active` and `token_version` columns on `users`, extends JWT claims to carry both, and makes `AuthMiddleware` validate them per request against the live DB row — yielding stateless, per-user token invalidation on deactivation, role change, and password reset. `RoleGuard` becomes variadic so admin routes can be guarded alongside expert routes. Six new endpoints are added; four existing expert routes are widened to accept admin as well.

The design is intentionally minimal: no refresh-token rotation/blacklist, no audit changes (the existing `verified_by` attribution is sufficient), no soft-delete, no front-end work, no new config or external dependencies.

## Scope

### In scope
- New `admin` role (DB CHECK constraint widened).
- New `active` and `token_version` columns on `users`.
- JWT claims extension (`active`, `ver`) and **stateless** token invalidation via per-request `FindByID` comparison.
- Variadic `RoleGuard(allowedRoles ...string)` (backward compatible).
- Six new endpoints: `GET /profile`, `GET /users`, `PUT /users/:id`, `DELETE /users/:id`, `POST /users/:id/reset-password`, `DELETE /questions/:id`.
- Password generation utility (`auth.GeneratePassword`).
- Last-admin and self-protection invariants (transactional, `SELECT FOR UPDATE`).
- Fix the `questions.verified_by` FK to `ON DELETE SET NULL` so verified questions survive user deletion.

### Out of scope
- Moderation audit changes (`verified_by` is sufficient as-is).
- Refresh-token rotation or a JWT blacklist.
- Email delivery of generated passwords.
- Soft-delete of questions or users.
- Front-end changes.
- New configuration values or external dependencies.

## Background (current-state facts)

- Roles are plain strings enforced by a DB `CHECK` constraint; today `user` and `expert`.
- `internal/httpapi/middleware.go` provides `AuthMiddleware` (sets gin context `claims`, `user_id`, `role`) and `RoleGuard(requiredRole string)` (exact string equality).
- JWT `Claims` (`internal/auth/jwt.go`): `{UserID "sub", Role, jwt.RegisteredClaims}`, HS256, 3h access TTL.
- `internal/auth/password.go`: bcrypt `HashPassword`/`VerifyPassword` (`DefaultCost=10`). No password generator exists.
- `storage.User` struct (`internal/storage/ports.go`): `{ID, Email, PasswordHash, Role, CreatedAt}`. `UserRepo` interface has only `Create`, `FindByEmail`, `FindByID`.
- `questions` table: `verified_by uuid REFERENCES users(id)` with **no** `ON DELETE` action (defaults to NO ACTION). FKs `session_questions`→`questions` and `question_tags`→`questions` are `ON DELETE CASCADE`.
- Migrations live in `internal/storage/postgres/migrations/NNNN_*.sql`, embedded, auto-run on boot. Latest is `0005`.
- Error envelope everywhere: `{"error":{"code":"...","message":"..."}}` via `handlers/common.go` `errorResponse()` and `domain.HTTPStatus()`.
- List endpoints use offset pagination: `{"data":[...],"page":N,"per_page":N}`.
- Handlers call repositories directly (**no service layer**). `QuestionRepo.UpdateByExpert` is the existing precedent for a multi-statement transactional repo method.
- Testing: `-short` flag gates unit tests; Testcontainers + pgvector integration tests need Docker and use `setupTestDB(t)`.

## Data Model Changes

### Migration `0006_user_management.sql`

```sql
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'expert', 'admin'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS active boolean NOT NULL DEFAULT true,
                          ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_users_role_active ON users(role, active);
-- fix verified_by FK to allow user deletion (preserve question, null attribution)
ALTER TABLE questions DROP CONSTRAINT IF EXISTS questions_verified_by_fkey;
ALTER TABLE questions ADD CONSTRAINT questions_verified_by_fkey
    FOREIGN KEY (verified_by) REFERENCES users(id) ON DELETE SET NULL;
```

- Existing users default to `active=true`, `token_version=0`. **No backfill.**
- The `questions.verified_by` FK changes from NO ACTION to `ON DELETE SET NULL`, so deleting a user who verified questions leaves those questions intact with `verified_by IS NULL`.

### Struct change — `storage.User`

`storage.User` (in `internal/storage/ports.go`) gains two fields:

```go
type User struct {
    ID           string
    Email        string
    PasswordHash string
    Role         string
    Active       bool
    TokenVersion int64
    CreatedAt    string
}
```

- `CreatedAt` stays `string` — matching the existing `storage.User` type and the `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` projection used by all current user queries. No query changes its timestamp formatting.
- **Existing queries that must add the two new columns:** `UserRepo.Create` (`RETURNING ...` list), `FindByEmail` (`SELECT ...`), and `FindByID` (`SELECT ...`) must each include `active` and `token_version`, since `Login` and `AuthMiddleware` read them from the loaded row. Omitting them would silently yield zero-value defaults (`active=false`, `token_version=0`) and break token validation.

## Auth & Token Invalidation

### Claims

`Claims` (`internal/auth/jwt.go`) becomes:

```go
type Claims struct {
    UserID       string `json:"sub"`
    Role         string `json:"role"`
    Active       bool   `json:"active"`
    TokenVersion int64  `json:"ver"`
    jwt.RegisteredClaims
}
```

### Issue

`JWTManager.Issue(userID, role string, active bool, tokenVersion int64)` threads the new values from the user record at issue time. **Login** reads `Active`/`TokenVersion` from the `FindByEmail` result it already loads; **Refresh** reads them from the `*storage.User` stashed in gin context by `AuthMiddleware` (no extra query). The values are never defaulted.

### Per-request validation — `AuthMiddleware`

`AuthMiddleware` gains a `storage.UserRepo` parameter (it must load the user row each request):

```go
func AuthMiddleware(jwtMgr *auth.JWTManager, users storage.UserRepo) gin.HandlerFunc
```

It performs a `FindByID` per request (on the `claims.sub` user id) and **rejects with 401 `unauthorized`** when:

- the user no longer exists (token points at a deleted account), OR
- `claims.Active != user.Active`, OR
- `claims.TokenVersion != user.TokenVersion`.

When all checks pass, the request proceeds with the gin context carrying `user_id` and `role` as today, **plus the freshly-loaded `*storage.User` under the gin context key `"user"`** — so `GET /profile` and `POST /auth/refresh` read it from context without a second query.

### Invalidating events (bump `token_version`)

Any of the following bumps `users.token_version`, invalidating all previously issued tokens for that user on their next request:

1. **Password reset** (`POST /users/:id/reset-password`).
2. **Active change** — deactivation bumps. (Reactivation does not *need* to bump on its own, but because deactivation already bumped the version, a stale token from before deactivation cannot be resurrected after reactivation.)
3. **Role change**.

A pure email change does **not** bump `token_version`.

## Authorization Model

### Variadic `RoleGuard`

`RoleGuard` (`internal/httpapi/middleware.go`) becomes variadic and performs a map-membership check. It is backward compatible: existing single-argument call sites continue to work.

```go
func RoleGuard(allowedRoles ...string) gin.HandlerFunc // map membership check; backward compatible
```

`admin` is a **SUPERUSER**: everything an `expert` can do **plus** user management. The four existing expert routes change from `RoleGuard("expert")` to `RoleGuard("expert","admin")` — backward-compatible one-line edits.

### Endpoint → role table

| Endpoint | Allowed | Guard |
|---|---|---|
| `GET /api/v1/profile` | any authenticated | none (`AuthMiddleware` only) |
| `GET /questions`, `GET /questions/:id` | any authenticated | none |
| session / image-upload | any authenticated + ownership | unchanged |
| `POST /questions`, `PUT /questions/:id` | expert, admin | `RoleGuard("expert","admin")` [was `RoleGuard("expert")`] |
| `GET /images/:id`, `GET /images/:id/verification-report` | expert, admin | `RoleGuard("expert","admin")` [was `RoleGuard("expert")`] |
| `DELETE /questions/:id` | expert, admin | `RoleGuard("expert","admin")` |
| `GET /users` | admin | `RoleGuard("admin")` |
| `PUT /users/:id` | admin | `RoleGuard("admin")` |
| `DELETE /users/:id` | admin | `RoleGuard("admin")` |
| `POST /users/:id/reset-password` | admin | `RoleGuard("admin")` |

## Endpoint Specifications

All responses use the standard error envelope `{"error":{"code":"...","message":"..."}}` via `errorResponse()` and `domain.HTTPStatus()`. List responses use the standard `{"data":[...],"page":N,"per_page":N}` shape.

### Shared `UserResponse`

Returned by every user-facing endpoint. **Never** exposes `password_hash` or `token_version`.

```json
{
  "id": "uuid",
  "email": "user@example.com",
  "role": "user|expert|admin",
  "active": true,
  "created_at": "2026-07-15T12:00:00Z"
}
```

- `created_at` is ISO 8601 UTC.
- `role` is one of `user`, `expert`, `admin`.

---

### GET /api/v1/profile

- **Auth:** any authenticated user (no `RoleGuard`).
- **Request:** no body, no query params. Caller identity taken from the JWT (`user_id` in gin context).
- **Response:** `200 OK` → the calling user's `UserResponse`, keyed on `user_id`.
- **Errors:**
  - `404 not_found` — if the user id is missing (e.g. account deleted but token still presented — though per-request `FindByID` in `AuthMiddleware` would normally reject first with `401 unauthorized`).
- **Behavior:** reads the row for `user_id` from the JWT and returns it. No side effects.

---

### GET /api/v1/users

- **Auth:** `admin` — `RoleGuard("admin")`.
- **Query params:**
  - `page` (int, default `1`, min `1`) — page number.
  - `per_page` (int, default `20`, **cap `100`**) — page size; values above the cap are clamped to `100`.
  - `role` (`user` | `expert` | `admin`, optional) — filter by exact role.
  - `active` (`true` | `false`, optional) — filter by active flag.
  - `q` (string, optional) — email substring match (`ILIKE %q%`).
- **Order:** `created_at DESC`.
- **Response:** `200 OK`
  ```json
  {
    "data": [UserResponse, ...],
    "page": 1,
    "per_page": 20
  }
  ```
  - **No `total` field** — matches the existing `QuestionListResponse` / `SessionListResponse` precedent.
- **Behavior:** delegates to `UserRepo.List` with a `UserFilter`. Filters are individually optional and combine with AND semantics.

---

### PUT /api/v1/users/:id

- **Auth:** `admin` — `RoleGuard("admin")`.
- **Semantics:** **full replacement** (PUT). All fields are required.
- **Request body:**
  ```json
  {
    "email": "new@example.com",
    "role": "expert",
    "active": true
  }
  ```
  - `email` — must be valid format **and** unique (excluding the target user itself; otherwise `409 duplicate`). Uniqueness is **case-sensitive**, matching the existing `users.email UNIQUE` constraint. (The `q` search filter's `ILIKE` is a separate display concern and does not affect uniqueness.)
  - `role` — one of `user`, `expert`, `admin`.
  - `active` — boolean.
- **Response:** `200 OK` → updated `UserResponse`.
- **Errors:**
  - `404 not_found` — target id does not exist.
  - `400 validation_error` — malformed body, invalid email, or `role` not in the allowed set.
  - `409 duplicate` — email already in use by a different user.
  - `409 self_forbidden` — target is the caller **and** the change would actually alter `role` or `active` (see self-protection below).
  - `409 last_admin` — the change would remove the last active admin.
- **Behavior (server-side diffing):**
  1. Load the current row for `:id`.
  2. **Diff** incoming vs. current to drive side effects:
     - **email change** → validate format and uniqueness (excluding self). Duplicate → `409 duplicate`.
     - **role OR active actually changes** → bump `token_version`. Sending the current values back unchanged → **no bump**. A pure email change → **no bump**.
  3. Run **self-protection** (see below).
  4. Run **last-active-admin** guard (see below).
  5. Persist within a transaction (`SELECT ... FOR UPDATE` on the target row before guard counts).
- **Self-protection (value-aware):** if `target_id == caller_id`, reject **only when `role` or `active` would actually change** → `409 self_forbidden`. Sending the caller's own current `role`/`active` back is a **no-op and allowed**; editing one's own email is **allowed**.
- **Last-active-admin:** if the change removes the final active admin (demotion to non-admin, or `active=false`), reject → `409 last_admin`.

---

### DELETE /api/v1/users/:id

- **Auth:** `admin` — `RoleGuard("admin")`.
- **Request:** no body.
- **Response:** `204 No Content` (empty body).
- **Errors:**
  - `404 not_found` — target id does not exist.
  - `409 self_forbidden` — target is the caller.
  - `409 last_admin` — deleting would remove the last active admin.
- **Behavior:** **hard delete**. Cascades `session_questions`→`images`→`jobs` via existing `ON DELETE CASCADE`; `questions.verified_by` is set to `NULL` via the migration's `ON DELETE SET NULL`. Self-protection and last-admin guards run **transactionally** (`SELECT ... FOR UPDATE` on the target row before guard counts).

---

### POST /api/v1/users/:id/reset-password

- **Auth:** `admin` — `RoleGuard("admin")`.
- **Request:** no body.
- **Response:** `200 OK`
  ```json
  { "password": "<20-char plaintext>" }
  ```
  - The plaintext is returned **exactly once**. Only the bcrypt hash is persisted.
- **Errors:**
  - `404 not_found` — target id does not exist.
- **Behavior:**
  1. Generate a 20-character password from `[A-Za-z0-9!@#$%^&*]` via `auth.GeneratePassword` (crypto/rand, bias-free).
  2. bcrypt-hash it (`HashPassword`).
  3. Write `password_hash` and **bump `token_version`**.
  4. **Does not touch `active`** (deactivated users stay deactivated).

---

### DELETE /api/v1/questions/:id

- **Auth:** `expert`, `admin` — `RoleGuard("expert","admin")`.
- **Request:** no body.
- **Response:** `204 No Content` (empty body).
- **Errors:**
  - `404 not_found` — question does not exist.
  - `409 question_in_use` — question is not deletable; the error `message` names the count of referencing `session_questions` rows.
- **Behavior:**
  1. Find the question by `:id`; `404` if absent.
  2. Deletable **iff** `status = 'error'` **OR** zero `session_questions` rows reference it; otherwise `409 question_in_use` (message names the count).
  3. Hard delete — cascades `session_questions` and `question_tags` via existing `ON DELETE CASCADE`.

## Password Generation

Add to `internal/auth/password.go`:

```go
func GeneratePassword() (string, error)
```

- **Length:** 20 characters (package-level `const`, no config — YAGNI).
- **Alphabet:** `[A-Za-z0-9!@#$%^&*]` — 70 symbols (package-level `const`).
- **Entropy:** ~122 bits.
- **RNG:** `crypto/rand`, **bias-free** selection — reject bytes `≥ 256 - (256 % 70)` before mapping to the alphabet, or use `math/big.Int` modulo.

## Domain Errors

New domain errors flow through the existing `errorResponse()` / `domain.HTTPStatus()` machinery.

| Error code | HTTP | Meaning |
|---|---|---|
| `self_forbidden` | `409 Conflict` | The admin attempted to change their own `role`/`active`, or delete themselves. |
| `last_admin` | `409 Conflict` | The operation would remove the last active admin. |
| `question_in_use` | `409 Conflict` | The question is referenced by `session_questions` and is not in `error` status. |

- `self_forbidden` and `last_admin` are **fixed-message sentinels** (`domain.ErrSelfForbidden`, `domain.ErrLastAdmin`), modelled on the existing `domain.ErrNotFound`.
- `question_in_use` is **constructed dynamically** with the referencing-session count in its message — `domain.NewError("question_in_use", fmt.Sprintf("question is linked to %d session(s)", n))` — it is not a fixed sentinel.
- Add all three codes (`self_forbidden`, `last_admin`, `question_in_use`) → `409 Conflict` to the `domain.HTTPStatus()` switch so handlers map them uniformly via `c.JSON(domain.HTTPStatus(err), errorResponse(err))`.

## New `UserRepo` Methods

Added to `UserRepo` (in `internal/storage/ports.go`) and implemented in `internal/storage/postgres`.

```go
List(ctx, filter UserFilter, limit, offset) ([]*User, error)
Update(ctx, id, upd UserUpdate, callerID) (*User, error)
Delete(ctx, id, callerID) error
ResetPassword(ctx, id) (plaintext string, err error)
```

### Types

- **`UserUpdate`** — plain (non-pointer) fields, since PUT is full-replacement: `Email`, `Role`, `Active`. The repo loads the current row and **diffs internally**.
- **`UserFilter`** — optional List filters: `Role *string`, `Active *bool`, `Query *string`.

### Transactional guarantees

- **`Update`** and **`Delete`** execute `SELECT ... FOR UPDATE` on the target row **before** running the self/last-admin guard counts. This defeats the concurrent-demote race (two admins cannot both succeed at removing the other's admin privilege).
- **`ResetPassword`** generates the password, bcrypt-hashes it, writes `password_hash`, and bumps `token_version`.
- **Caller identity is passed explicitly** from the handler (`callerID`) — the handler reads `user_id` from the gin context. **The repo never touches Gin.**

## New `QuestionRepo` Method

`QuestionRepo` (in `internal/storage/ports.go`) gains a single method:

```go
Delete(ctx context.Context, id string) error
```

- Loads the question (`404 not_found` if absent).
- Counts `session_questions` rows referencing it; if that count > 0 **and** `status != 'error'`, returns a dynamically-constructed `domain.Error` with code `question_in_use` and a message naming the count (`domain.NewError("question_in_use", fmt.Sprintf("question is linked to %d session(s)", n))`).
- Otherwise hard-deletes the question (cascades `session_questions` and `question_tags` via existing `ON DELETE CASCADE`).

This follows the same "transactional guard logic lives in the repo" convention as `UserRepo.Update`/`Delete` and the existing `QuestionRepo.UpdateByExpert`.

## Handler & Wiring Layout

- **`AuthHandler`** (existing) gains `Profile(c)` → `GET /api/v1/profile` (already holds `UserRepo` + `JWTManager`; reads the `*storage.User` stashed by `AuthMiddleware`). Existing `/auth/register` and `/auth/login` response shapes are unchanged — the new `UserResponse` is distinct to the profile/users endpoints.
- **`QuestionHandler`** (existing) gains `Delete(c)` → `DELETE /questions/:id` (already holds `QuestionRepo`).
- **New `handlers/users.go`** — `UserHandler` holding `UserRepo`, with methods: `List`, `Update` (PUT), `Delete`, `ResetPassword`.
- **`wire.go` / `server.go`:**
  - Construct `UserHandler` from the existing `UserRepo` (no new repo to instantiate — `UserRepo` already exists and just gains methods).
  - Pass `s.userRepo` into `AuthMiddleware(s.jwtMgr, s.userRepo)` at both of its call sites (the `/api/v1` auth group and the `/auth/refresh` route).
  - Register the six new routes with their `RoleGuard`s.
  - Widen the four existing expert routes from `RoleGuard("expert")` to `RoleGuard("expert","admin")`.
  - **No new config, no new external deps.** (The middleware gains a dependency on the already-constructed `UserRepo`; nothing new is built.)

## Testing Strategy

### Unit tests (`-short`, no Docker)

- **`auth.GeneratePassword`:** `length == 20`; every character is in the alphabet; no panic across many iterations; over a large sample every alphabet symbol appears at least once (a uniformity sanity check — avoids the theoretically-flaky "two consecutive calls differ" assertion, since `crypto/rand` can in principle emit identical strings).
- **`auth.JWTManager`:** issue + verify round-trip carries `Active` and `TokenVersion`; expired token is rejected.
- **`middleware.RoleGuard`:** variadic membership — single allow, multi allow, deny, empty context → `403`.
- **Pure token-invalidation comparison function:** `active` mismatch → invalid; `tokenVersion` mismatch → invalid; both match → valid. Tested in isolation **without a DB**.

### Integration tests (Testcontainers + `setupTestDB`)

- **`UserRepo.List`:** each filter combination (role, active, query) + pagination behavior.
- **`UserRepo.Update`:** happy path; each guard (`self_forbidden`, `last_admin`, `duplicate email`); `token_version` bumps **only** on a real `role`/`active` change (sending current values back → no bump; pure email change → no bump).
- **`UserRepo.Delete`:** happy path + guards (`self_forbidden`, `last_admin`).
- **`UserRepo.ResetPassword`:** returns plaintext; stores a new bcrypt hash; bumps `token_version`; the old bcrypt hash no longer verifies the old password.
- **`DELETE /questions/:id`:** `404` when absent; `204` on `status='error'` or zero links; `409 question_in_use` with the correct count in the message.
- **`verified_by ON DELETE SET NULL` migration:** insert a user who verified a question, delete the user, assert the question survives with `verified_by IS NULL`.
- **Handler auth:** each new admin route returns `403` for `user`/`expert` tokens; the `("expert","admin")` routes accept both roles; `/profile` works for all three roles.
- **Concurrent-demote race** is asserted only at the **sequential invariant level** (two admins cannot both succeed removing the other). True concurrency stress testing is **out of scope for MVP**.

## Non-Goals

- No refresh-token rotation or JWT blacklist — invalidation is stateless via the `active`/`token_version` claims checked per request.
- No moderation-audit changes — `verified_by` (now `ON DELETE SET NULL`) remains the sole attribution signal.
- No email delivery of generated passwords — the plaintext is returned exactly once in the HTTP response.
- No soft-delete of questions or users.
- No front-end changes.
- No new configuration values or external dependencies.
- No true concurrency stress testing of the demote race (sequential invariant only for MVP).
