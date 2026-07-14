# User Management & Admin RBAC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `admin` superuser role with full user management (list/update/delete/reset-password), per-request stateless JWT invalidation via `active`/`token_version` columns, a variadic `RoleGuard`, and a question-delete endpoint.

**Architecture:** A new migration widens the role `CHECK`, adds `active`/`token_version` to `users`, and flips `questions.verified_by` to `ON DELETE SET NULL`. JWT claims carry `active`+`ver`; `AuthMiddleware` revalidates them against the live DB row each request (stateless per-user invalidation). Transactional repo methods (`SELECT ... FOR UPDATE`) enforce self-protection and last-active-admin invariants. No service layer — handlers call repos directly, following existing precedent (`QuestionRepo.UpdateByExpert`).

**Tech Stack:** Go 1.26.3, Gin, pgx/v5, pgvector, PostgreSQL 16 (Testcontainers), golang-jwt/v5, bcrypt, crypto/rand.

## Global Constraints

- **Go 1.26.3+** (see `go.mod`).
- **`CGO_ENABLED=1` + libvips required for a full `go build ./...`** (the `enhancer` package imports `govips`). Unit tests of a *single* non-cgo package (`go test -short ./internal/auth/`, etc.) do **not** need libvips — only `./...`-scoped builds/tests pull in the cgo package. If libvips is missing, install it (`brew install vips pkg-config` on macOS) or use `docker build -t coeus .`.
- **Integration tests need Docker running** and run via `go test ./internal/storage/postgres/ ./internal/pipeline/ -timeout 180s`. They self-skip under `-short`. DB-backed tests use `setupTestDB(t)` in `internal/storage/postgres/testhelpers_test.go`.
- **Migrations auto-run on boot** via `postgres.RunMigrations` (embedded `migrations/*.sql`, applied in sorted order). Adding `0006_*.sql` is sufficient — there is **no separate migrate step**.
- **Error envelope everywhere:** `{"error":{"code":"...","message":"..."}}` via `handlers/common.go` `errorResponse()` and `domain.HTTPStatus()`. List responses: `{"data":[...],"page":N,"per_page":N}`.
- **Timestamp projection is fixed:** all user queries use `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` and `storage.User.CreatedAt` is a `string`. Do not change this.
- **`*.md` and `docs/` are gitignored** — plan/spec files won't show in `git status`; that is expected. Use `git add -f` only if explicitly asked to track them.
- **Capture, don't create:** this plan implements the approved spec verbatim. Do not redesign. The authoritative spec is `docs/superpowers/specs/2026-07-15-user-management-and-admin-rbac-design.md`.
- **No new config values, no new external dependencies, no front-end work, no soft-delete, no JWT blacklist.**

## File Structure

**New files:**
- `internal/storage/postgres/migrations/0006_user_management.sql` — schema changes (role CHECK, `active`, `token_version`, `verified_by` FK, index).
- `internal/storage/postgres/migration_0006_test.go` — integration test: migration applies, columns/defaults present, `admin` role accepted, `verified_by` SET NULL works.
- `internal/httpapi/handlers/users.go` — `UserHandler` (`List`/`Update`/`Delete`/`ResetPassword`) + `toUserResponse` helper.
- `internal/httpapi/handlers/users_test.go` — `UserHandler` unit tests with a controllable fake `UserRepo`.
- `internal/storage/postgres/server_auth_test.go` — integration tests for route authorization (admin/expert/user) booting a real `httpapi.Server` against `setupTestDB`.

**Modified files (layer order):**
- `internal/domain/errors.go` (+`errors_test.go`) — new sentinels + HTTPStatus cases.
- `internal/storage/ports.go` — `storage.User` fields; `UserFilter`/`UserUpdate` types; new `UserRepo` + `QuestionRepo` methods.
- `internal/storage/postgres/user_repo.go` — update existing queries; add `List`/`Update`/`Delete`/`ResetPassword`.
- `internal/storage/postgres/user_repo_test.go` — extend existing tests; add repo-method integration tests.
- `internal/auth/password.go` (+`password_test.go`) — `GeneratePassword`.
- `internal/auth/jwt.go` (+`jwt_test.go`) — `Claims` fields + `Issue` signature.
- `internal/httpapi/middleware.go` (+`middleware_test.go`) — `AuthMiddleware` per-request revalidation + `user` context stash; pure `tokenValid`; variadic `RoleGuard`.
- `internal/httpapi/handlers/auth.go` (+`auth_test.go`) — `Login`/`Refresh` use new `Issue`; `Profile` method; fake `Active:true`.
- `internal/httpapi/dto/responses.go` (+`requests.go`) — `UserResponse`, `UserListResponse`, `ResetPasswordResponse`, `UpdateUserRequest`.
- `internal/httpapi/handlers/questions.go` (+`questions_test.go`) — `Delete` method; fake `QuestionRepo.Delete` stub.
- `internal/httpapi/server.go` — `AuthMiddleware` call sites gain `userRepo`; register 6 new routes; widen 4 expert routes.

---

## Task 1: Migration `0006_user_management.sql`

**Files:**
- Create: `internal/storage/postgres/migrations/0006_user_management.sql`
- Create: `internal/storage/postgres/migration_0006_test.go`

**Interfaces:**
- Consumes: `setupTestDB(t)` (Testcontainers), `NewUserRepo`, `NewQuestionRepo`, existing `UserRepo.Create`/`FindByID`, `QuestionRepo.Create`/`FindByID`.
- Produces: the `active`/`token_version` columns and the `admin` role + `verified_by ON DELETE SET NULL`, on which every later task depends.

- [ ] **Step 1: Create the migration file**

Write `internal/storage/postgres/migrations/0006_user_management.sql` (verbatim from spec §Data Model):

```sql
-- 0006_user_management.sql
-- Admin RBAC: widen role CHECK, add active + token_version for stateless JWT
-- invalidation, and make verified_by ON DELETE SET NULL so deleting a user who
-- verified questions preserves those questions (null attribution).
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'expert', 'admin'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS active boolean NOT NULL DEFAULT true,
                           ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_users_role_active ON users(role, active);

-- verified_by: NO ACTION -> ON DELETE SET NULL (preserve question, null attribution)
ALTER TABLE questions DROP CONSTRAINT IF EXISTS questions_verified_by_fkey;
ALTER TABLE questions ADD CONSTRAINT questions_verified_by_fkey
    FOREIGN KEY (verified_by) REFERENCES users(id) ON DELETE SET NULL;
```

- [ ] **Step 2: Write the failing integration test**

Create `internal/storage/postgres/migration_0006_test.go`:

```go
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// TestMigration0006_ColumnsAndDefaults asserts the new columns exist with the
// spec defaults (active=true, token_version=0) on a freshly migrated DB.
func TestMigration0006_ColumnsAndDefaults(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	u, err := repo.Create(ctx, "mig@example.com", "hash", "user")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if !u.Active {
		t.Errorf("Active = false, want true (DEFAULT)")
	}
	if u.TokenVersion != 0 {
		t.Errorf("TokenVersion = %d, want 0", u.TokenVersion)
	}
}

// TestMigration0006_AdminRoleAccepted asserts the widened CHECK allows 'admin'.
func TestMigration0006_AdminRoleAccepted(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	if _, err := repo.Create(ctx, "admin@example.com", "hash", "admin"); err != nil {
		t.Fatalf("create admin user: %v", err)
	}
}

// TestMigration0006_VerifiedByOnDeleteSetNull asserts that deleting a user who
// verified a question leaves the question intact with verified_by IS NULL.
func TestMigration0006_VerifiedByOnDeleteSetNull(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	users := NewUserRepo(pool)
	questions := NewQuestionRepo(pool)

	verifier, err := users.Create(ctx, "verifier@example.com", "hash", "expert")
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	qID, err := questions.Create(ctx, &domain.Question{
		Text:      "Q", TextHash: "vby-hash", TextNorm: "vby",
		Status: domain.QuestionStatusVerified, Choices: []string{"a"},
		ChoiceLabeling: "letter", VerifiedAt: &now, VerifiedBy: &verifier.ID,
	})
	if err != nil {
		t.Fatalf("create verified question: %v", err)
	}

	// Delete the verifier directly (UserRepo.Delete lands in Task 8).
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, verifier.ID); err != nil {
		t.Fatalf("delete verifier: %v", err)
	}

	// Question must survive, with verified_by now NULL.
	q, err := questions.FindByID(ctx, qID)
	if err != nil {
		t.Fatalf("question should survive user deletion, got: %v", err)
	}
	if q.VerifiedBy != nil {
		t.Errorf("VerifiedBy = %v, want nil (ON DELETE SET NULL)", *q.VerifiedBy)
	}
}
```

> Note: these tests reference `u.Active`/`u.TokenVersion` on `storage.User`, which Task 3 adds to the struct. To keep Task 1 independently green, the assertions on `u.Active`/`u.TokenVersion` are written here but **will not compile until Task 3 lands**. If executing tasks strictly in order, run the migration portion via `psql`/container first (Step 3), then the struct assertions compile after Task 3. Alternatively, defer the `Columns` test's field assertions to Task 3 and keep only `AdminRoleAccepted` + `VerifiedByOnDeleteSetNull` here (those compile today since they use repo methods that exist). **Recommended:** keep all three here for cohesion, and treat the compile gate as "passes after Task 3."

- [ ] **Step 3: Verify the migration applies cleanly**

The migration auto-applies inside every Testcontainers test via `RunMigrations`. To verify it applies on its own without depending on later struct changes, boot a throwaway container and apply migrations:

```bash
go test ./internal/storage/postgres/ -run TestMigration0006_VerifiedByOnDeleteSetNull -timeout 180s -v
```
Expected: PASS (this test uses only existing repo methods + a raw `DELETE`, so it compiles and proves the FK behavior). Requires Docker.

- [ ] **Step 4: Vet**

```bash
go vet ./internal/storage/postgres/
```
Expected: no issues (targeted package — no cgo/libvips needed).

- [ ] **Step 5: Commit**

```bash
git add internal/storage/postgres/migrations/0006_user_management.sql internal/storage/postgres/migration_0006_test.go
git commit -m "feat(db): add 0006 migration for admin role, active/token_version, verified_by SET NULL"
```

---

## Task 2: Domain errors (`self_forbidden`, `last_admin`) + HTTPStatus cases

**Files:**
- Modify: `internal/domain/errors.go`
- Modify: `internal/domain/errors_test.go`

**Interfaces:**
- Consumes: existing `domain.NewError`, `domain.Error`, `domain.HTTPStatus`.
- Produces: `domain.ErrSelfForbidden`, `domain.ErrLastAdmin` sentinels; HTTP 409 mapping for codes `self_forbidden`, `last_admin`, `question_in_use` (the last is constructed dynamically via `NewError` in Task 10, but its HTTP mapping is added here).

- [ ] **Step 1: Write the failing test**

Append to `internal/domain/errors_test.go`:

```go
func TestHTTPStatus_NewConflictCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"self_forbidden sentinel", ErrSelfForbidden, 409},
		{"last_admin sentinel", ErrLastAdmin, 409},
		{"question_in_use dynamic", NewError("question_in_use", "linked to 3 session(s)"), 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HTTPStatus(tc.err); got != tc.want {
				t.Errorf("HTTPStatus(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestNewErrorSentinelMessages(t *testing.T) {
	if ErrSelfForbidden.Code != "self_forbidden" {
		t.Errorf("ErrSelfForbidden.Code = %q", ErrSelfForbidden.Code)
	}
	if ErrLastAdmin.Code != "last_admin" {
		t.Errorf("ErrLastAdmin.Code = %q", ErrLastAdmin.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -short ./internal/domain/ -run TestHTTPStatus_NewConflictCodes -v
```
Expected: FAIL / compile error (`ErrSelfForbidden` undefined, and no `409` mapping for `question_in_use`).

- [ ] **Step 3: Add the sentinels**

In `internal/domain/errors.go`, add to the `var (...)` block (after `ErrAIUnavailable`):

```go
	ErrSelfForbidden = NewError("self_forbidden", "admin cannot change their own role or active state")
	ErrLastAdmin     = NewError("last_admin", "operation would remove the last active admin")
```

- [ ] **Step 4: Add the HTTPStatus cases**

In `domain.HTTPStatus`'s `switch e.Code`, add (next to the existing `case "duplicate":`):

```go
	case "self_forbidden", "last_admin", "question_in_use":
		return http.StatusConflict
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test -short ./internal/domain/ -v
```
Expected: PASS (all domain tests, including the existing ones).

- [ ] **Step 6: Vet + commit**

```bash
go vet ./internal/domain/
git add internal/domain/errors.go internal/domain/errors_test.go
git commit -m "feat(domain): add self_forbidden/last_admin errors and 409 mapping for question_in_use"
```

---

## Task 3: Extend `storage.User` + update existing UserRepo queries

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/user_repo.go`
- Modify: `internal/storage/postgres/user_repo_test.go`

**Interfaces:**
- Consumes: Task 1 (columns `active`, `token_version` exist).
- Produces: `storage.User.Active` (`bool`) and `storage.User.TokenVersion` (`int64`), populated by `Create`/`FindByEmail`/`FindByID`. `Login` and `AuthMiddleware` (Task 5) depend on these being non-zero/real.

> **Spec warning (§Data Model):** the three existing queries MUST add the two new columns. Omitting them silently yields `active=false`, breaking token validation.

- [ ] **Step 1: Write the failing test**

In `internal/storage/postgres/user_repo_test.go`, extend `TestUserRepo_CreateAndFindByEmail` assertions and add a FindByID assertion. Append:

```go
func TestUserRepo_NewColumnsPopulated(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, "cols@example.com", "hash", "user")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Active {
		t.Errorf("created.Active = false, want true")
	}
	if created.TokenVersion != 0 {
		t.Errorf("created.TokenVersion = %d, want 0", created.TokenVersion)
	}

	byEmail, err := repo.FindByEmail(ctx, "cols@example.com")
	if err != nil {
		t.Fatalf("find by email: %v", err)
	}
	if !byEmail.Active || byEmail.TokenVersion != 0 {
		t.Errorf("by email: Active=%v TokenVersion=%d", byEmail.Active, byEmail.TokenVersion)
	}

	byID, err := repo.FindByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if !byID.Active || byID.TokenVersion != 0 {
		t.Errorf("by id: Active=%v TokenVersion=%d", byID.Active, byID.TokenVersion)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_NewColumnsPopulated -timeout 180s -v
```
Expected: FAIL / compile error (`storage.User` has no field `Active`/`TokenVersion`).

- [ ] **Step 3: Add fields to the struct**

In `internal/storage/ports.go`, replace the `User` struct:

```go
// User is the storage-level user record (includes password hash for auth).
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

- [ ] **Step 4: Update the three queries + scans**

In `internal/storage/postgres/user_repo.go`:

`Create` — `RETURNING` list + scan:
```go
	row := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, role, active, token_version,
		          to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`, email, passwordHash, role)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
```

`FindByEmail` — `SELECT` + scan:
```go
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE email = $1
	`, email)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
```

`FindByID` — `SELECT` + scan:
```go
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE id = $1
	`, id)

	var u storage.User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt)
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo -timeout 180s -v
```
Expected: PASS (all `TestUserRepo_*`).

- [ ] **Step 6: Vet + commit**

```bash
go vet ./internal/storage/...
git add internal/storage/ports.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go
git commit -m "feat(storage): carry active/token_version through UserRepo Create/FindByEmail/FindByID"
```

---

## Task 4: `auth.GeneratePassword`

**Files:**
- Modify: `internal/auth/password.go`
- Modify: `internal/auth/password_test.go`

**Interfaces:**
- Consumes: `crypto/rand`, `math/big`.
- Produces: `auth.GeneratePassword() (string, error)` — 20 chars from `[A-Za-z0-9!@#$%^&*]`, bias-free. Used by `UserRepo.ResetPassword` (Task 9).

- [ ] **Step 1: Write the failing test**

Append to `internal/auth/password_test.go`:

```go
import "strings"

const wantAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"

func TestGeneratePassword_LengthAndCharset(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	if len(pw) != 20 {
		t.Errorf("len = %d, want 20", len(pw))
	}
	for i, r := range pw {
		if !strings.ContainsRune(wantAlphabet, r) {
			t.Errorf("char at %d (%q) not in alphabet", i, r)
		}
	}
}

func TestGeneratePassword_NoPanicAndUniform(t *testing.T) {
	seen := make(map[rune]bool)
	for i := 0; i < 5000; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		for _, r := range pw {
			seen[r] = true
		}
	}
	// Uniformity sanity: over a large sample, every alphabet symbol appears.
	for _, r := range wantAlphabet {
		if !seen[r] {
			t.Errorf("alphabet symbol %q never appeared in 5000 samples", r)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -short ./internal/auth/ -run TestGeneratePassword -v
```
Expected: FAIL / compile error (`GeneratePassword` undefined).

- [ ] **Step 3: Implement**

Replace `internal/auth/password.go`:

```go
package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

const (
	generatedPasswordLength = 20
	// [A-Za-z0-9!@#$%^&*] — 70 symbols, ~122 bits of entropy at length 20.
	passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*"
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GeneratePassword returns a bias-free 20-character password drawn uniformly
// from passwordAlphabet using crypto/rand (via math/big modulo).
func GeneratePassword() (string, error) {
	n := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, generatedPasswordLength)
	for i := range out {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[idx.Int64()]
	}
	return string(out), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -short ./internal/auth/ -v
```
Expected: PASS (all auth tests, including pre-existing).

- [ ] **Step 5: Vet + commit**

```bash
go vet ./internal/auth/
git add internal/auth/password.go internal/auth/password_test.go
git commit -m "feat(auth): add bias-free GeneratePassword (20 chars, crypto/rand)"
```

---

## Task 5: JWT claims + per-request validation + variadic RoleGuard + wire auth call sites

This is the coherent "stateless token invalidation" slice: extending `Claims`/`Issue`, making `AuthMiddleware` revalidate against the live row each request and stash it, making `RoleGuard` variadic, and updating every affected call site so the build stays green.

**Files:**
- Modify: `internal/auth/jwt.go`, `internal/auth/jwt_test.go`
- Modify: `internal/httpapi/middleware.go`, `internal/httpapi/middleware_test.go`
- Modify: `internal/httpapi/handlers/auth.go`, `internal/httpapi/handlers/auth_test.go`
- Modify: `internal/httpapi/server.go`

**Interfaces:**
- Consumes: Task 3 (`storage.User.Active`/`TokenVersion`).
- Produces:
  - `auth.Claims` with `Active bool` + `TokenVersion int64` (`json:"active"`, `json:"ver"`).
  - `auth.JWTManager.Issue(userID, role string, active bool, tokenVersion int64) (string, error)`.
  - `httpapi.AuthMiddleware(jwtMgr *auth.JWTManager, users storage.UserRepo) gin.HandlerFunc` — per-request `FindByID`, validation, stashes `*storage.User` under gin key `"user"`.
  - `httpapi.RoleGuard(allowedRoles ...string) gin.HandlerFunc` (variadic, backward compatible).
  - `AuthHandler.Refresh` reads the stashed user (no extra query).

- [ ] **Step 1: Extend Claims + Issue signature**

In `internal/auth/jwt.go`:

```go
type Claims struct {
	UserID       string `json:"sub"`
	Role         string `json:"role"`
	Active       bool   `json:"active"`
	TokenVersion int64  `json:"ver"`
	jwt.RegisteredClaims
}
```

```go
func (m *JWTManager) Issue(userID, role string, active bool, tokenVersion int64) (string, error) {
	claims := Claims{
		UserID:       userID,
		Role:         role,
		Active:       active,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(m.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}
```

- [ ] **Step 2: Update jwt_test.go**

Every existing `mgr.Issue(...)` call gains two trailing args. Replace each `mgr.Issue("user-123", "user")` with `mgr.Issue("user-123", "user", true, 0)` and `mgr.Issue("expert-1", "expert")` with `mgr.Issue("expert-1", "expert", true, 0)`. Then add:

```go
func TestIssueAndVerifyCarriesActiveAndVersion(t *testing.T) {
	mgr := NewJWTManager(config.JWTConfig{Secret: "s", AccessTTL: time.Hour})
	token, err := mgr.Issue("u1", "user", true, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !claims.Active {
		t.Error("claims.Active = false, want true")
	}
	if claims.TokenVersion != 7 {
		t.Errorf("claims.TokenVersion = %d, want 7", claims.TokenVersion)
	}
}
```

```bash
go test -short ./internal/auth/ -v
```
Expected: PASS.

- [ ] **Step 3: Rewrite AuthMiddleware + RoleGuard + pure helper**

In `internal/httpapi/middleware.go`, replace `AuthMiddleware` and `RoleGuard` and add `tokenValid`:

```go
func AuthMiddleware(jwtMgr *auth.JWTManager, users storage.UserRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := jwtMgr.Verify(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}

		// Stateless per-request revalidation against the live row.
		user, err := users.FindByID(c.Request.Context(), claims.UserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		if !tokenValid(claims, user) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}

		c.Set("claims", claims)
		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)
		c.Set("user", user)
		c.Next()
	}
}

// tokenValid reports whether the JWT claims still match the live user row.
// Pure (no DB) so it is unit-testable in isolation.
func tokenValid(claims *auth.Claims, user *storage.User) bool {
	return claims.Active == user.Active && claims.TokenVersion == user.TokenVersion
}

func RoleGuard(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, apiError(domain.ErrForbidden))
			return
		}
		for _, allowed := range allowedRoles {
			if role.(string) == allowed {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, apiError(domain.ErrForbidden))
	}
}
```

- [ ] **Step 4: Add middleware tests (tokenValid + variadic RoleGuard + fake UserRepo)**

Append to `internal/httpapi/middleware_test.go`:

```go
import (
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// fakeUserRepo implements just storage.UserRepo for middleware tests.
type fakeUserRepo struct {
	user *storage.User
	err  error
}

func (f *fakeUserRepo) Create(context.Context, string, string, string) (*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindByEmail(context.Context, string) (*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindByID(_ context.Context, _ string) (*storage.User, error) {
	return f.user, f.err
}

func TestTokenValid(t *testing.T) {
	cases := []struct {
		name             string
		claimsActive     bool
		claimsVersion    int64
		userActive       bool
		userVersion      int64
		want             bool
	}{
		{"match", true, 3, true, 3, true},
		{"active mismatch", true, 3, false, 3, false},
		{"version mismatch", true, 3, true, 4, false},
		{"both mismatch", false, 0, true, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := &auth.Claims{Active: tc.claimsActive, TokenVersion: tc.claimsVersion}
			user := &storage.User{Active: tc.userActive, TokenVersion: tc.userVersion}
			if got := tokenValid(claims, user); got != tc.want {
				t.Errorf("tokenValid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoleGuard_Variadic(t *testing.T) {
	mk := func(role string, guard ...string) int {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Next() })
		r.GET("/x", RoleGuard(guard...), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		return w.Code
	}
	if c := mk("admin", "expert", "admin"); c != 200 {
		t.Errorf("admin via (expert,admin): got %d want 200", c)
	}
	if c := mk("expert", "expert"); c != 200 {
		t.Errorf("expert via (expert): got %d want 200", c)
	}
	if c := mk("user", "expert", "admin"); c != 403 {
		t.Errorf("user via (expert,admin): got %d want 403", c)
	}
	if c := mk("user"); c != 403 { // empty allowed list
		t.Errorf("no allowed roles: got %d want 403", c)
	}
}

func TestRoleGuard_MissingRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// No "role" set in context.
	r.GET("/x", RoleGuard("admin"), func(c *gin.Context) { t.Error("must not run") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	if w.Code != 403 {
		t.Errorf("missing role: got %d want 403", w.Code)
	}
}
```

> Note: `fakeUserRepo` here implements the **current** 3-method `UserRepo`. Task 6 will add the four new method stubs to it (and to `mockUserRepo`) so it keeps satisfying the interface as it grows.

- [ ] **Step 5: Update auth.go Login + Refresh**

In `internal/httpapi/handlers/auth.go`:

`Login` — change the `Issue` call:
```go
	token, err := h.jwtMgr.Issue(user.ID, user.Role, user.Active, user.TokenVersion)
```

`Refresh` — read the stashed user instead of raw context strings:
```go
func (h *AuthHandler) Refresh(c *gin.Context) {
	v, _ := c.Get("user")
	user := v.(*storage.User)

	token, err := h.jwtMgr.Issue(user.ID, user.Role, user.Active, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusOK, authResponse{Token: token, Role: user.Role})
}
```

- [ ] **Step 6: Fix mockUserRepo Active default**

In `internal/httpapi/handlers/auth_test.go`, `mockUserRepo.Create` returns a user with zero `Active` (false), which would issue invalid tokens. Set it true:

```go
func (m *mockUserRepo) Create(_ context.Context, email, hash, role string) (*storage.User, error) {
	if _, ok := m.users[email]; ok {
		return nil, fmt.Errorf("create: %w", domain.ErrDuplicate)
	}
	u := &storage.User{ID: uuid.NewString(), Email: email, PasswordHash: hash, Role: role, Active: true}
	m.users[email] = u
	return u, nil
}
```

- [ ] **Step 7: Update server.go AuthMiddleware call sites**

In `internal/httpapi/server.go` `registerRoutes`, both call sites gain `s.userRepo`:

```go
		authGroup.POST("/refresh", AuthMiddleware(s.jwtMgr, s.userRepo), authHandler.Refresh)
```
```go
	apiGroup.Use(AuthMiddleware(s.jwtMgr, s.userRepo))
```

- [ ] **Step 8: Run the full affected unit-test surface**

```bash
go test -short ./internal/auth/ ./internal/httpapi/ ./internal/httpapi/handlers/ -v
```
Expected: PASS. (`httpapi` middleware + server tests, `handlers` auth tests, `auth` jwt/password tests.) These are non-cgo packages — no libvips needed.

- [ ] **Step 9: Vet + commit**

```bash
go vet ./internal/auth/ ./internal/httpapi/ ./internal/httpapi/handlers/
git add internal/auth/jwt.go internal/auth/jwt_test.go \
        internal/httpapi/middleware.go internal/httpapi/middleware_test.go \
        internal/httpapi/handlers/auth.go internal/httpapi/handlers/auth_test.go \
        internal/httpapi/server.go
git commit -m "feat(auth): stateless per-request token validation, variadic RoleGuard, stash user in context"
```

---

## Task 6: `UserRepo.List` + `UserFilter`/`UserUpdate` types + fake stubs

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/user_repo.go`
- Modify: `internal/storage/postgres/user_repo_test.go`
- Modify: `internal/httpapi/handlers/auth_test.go` (add 4 stubs to `mockUserRepo`)
- Modify: `internal/httpapi/middleware_test.go` (add 4 stubs to `fakeUserRepo`)

**Interfaces:**
- Consumes: `escapeLike` (in `question_repo.go`, same package).
- Produces:
  - `storage.UserFilter{Role *string; Active *bool; Query *string}`.
  - `storage.UserUpdate{Email string; Role string; Active bool}` (used by Task 7).
  - `UserRepo.List(ctx, filter UserFilter, limit, offset int) ([]*User, error)`.
  - Four no-op stubs on the two test fakes so they keep satisfying the growing interface (Tasks 7–9 then need no fake edits).

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/postgres/user_repo_test.go`:

```go
func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestUserRepo_List(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "a@example.com", "h", "user")
	repo.Create(ctx, "b@example.com", "h", "expert")
	repo.Create(ctx, "c@example.com", "h", "admin")
	repo.Create(ctx, "admin2@example.com", "h", "admin")

	// All, ordered by created_at DESC.
	all, err := repo.List(ctx, storage.UserFilter{}, 100, 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4", len(all))
	}

	// Filter by role.
	admins, _ := repo.List(ctx, storage.UserFilter{Role: strPtr("admin")}, 100, 0)
	if len(admins) != 2 {
		t.Errorf("admins = %d, want 2", len(admins))
	}

	// Filter by active.
	inactive, _ := repo.List(ctx, storage.UserFilter{Active: boolPtr(false)}, 100, 0)
	if len(inactive) != 0 {
		t.Errorf("inactive = %d, want 0", len(inactive))
	}

	// Query substring (ILIKE).
	got, _ := repo.List(ctx, storage.UserFilter{Query: strPtr("ADMIN2")}, 100, 0)
	if len(got) != 1 || got[0].Email != "admin2@example.com" {
		t.Errorf("query ADMIN2: got %+v", got)
	}

	// Pagination.
	page1, _ := repo.List(ctx, storage.UserFilter{}, 2, 0)
	page2, _ := repo.List(ctx, storage.UserFilter{}, 2, 2)
	if len(page1) != 2 || len(page2) != 2 {
		t.Errorf("pagination: page1=%d page2=%d", len(page1), len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Error("pagination returned same row on both pages")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_List -timeout 180s -v
```
Expected: FAIL / compile error (`UserFilter`/`List` undefined).

- [ ] **Step 3: Add the types + interface method**

In `internal/storage/ports.go`, add the types (near the `User` struct) and extend `UserRepo`:

```go
// UserFilter holds the optional List filters. Each pointer is optional (nil => no filter).
type UserFilter struct {
	Role   *string
	Active *bool
	Query  *string
}

// UserUpdate is the full-replacement payload for PUT /users/:id (non-pointer fields).
type UserUpdate struct {
	Email  string
	Role   string
	Active bool
}
```

```go
type UserRepo interface {
	Create(ctx context.Context, email, passwordHash, role string) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	FindByID(ctx context.Context, id string) (*User, error)
	List(ctx context.Context, filter UserFilter, limit, offset int) ([]*User, error)
}
```

- [ ] **Step 4: Implement List**

In `internal/storage/postgres/user_repo.go`, add the `strings` import and the method:

```go
import (
	// existing...
	"strings"
)
```

```go
func (r *UserRepo) List(ctx context.Context, filter storage.UserFilter, limit, offset int) ([]*storage.User, error) {
	query := `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users`
	args := []interface{}{}
	idx := 1
	var where []string
	if filter.Role != nil {
		where = append(where, fmt.Sprintf("role = $%d", idx))
		args = append(args, *filter.Role)
		idx++
	}
	if filter.Active != nil {
		where = append(where, fmt.Sprintf("active = $%d", idx))
		args = append(args, *filter.Active)
		idx++
	}
	if filter.Query != nil && *filter.Query != "" {
		where = append(where, fmt.Sprintf("email ILIKE $%d", idx))
		args = append(args, "%"+escapeLike(*filter.Query)+"%")
		idx++
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var results []*storage.User
	for rows.Next() {
		var u storage.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &u.TokenVersion, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		results = append(results, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return results, nil
}
```

- [ ] **Step 5: Add 4 stubs to both test fakes (so the interface can grow in Tasks 7–9 without re-editing fakes)**

In `internal/httpapi/handlers/auth_test.go`, add to `mockUserRepo`:

```go
func (m *mockUserRepo) List(context.Context, storage.UserFilter, int, int) ([]*storage.User, error) {
	return nil, nil
}
func (m *mockUserRepo) Update(context.Context, string, storage.UserUpdate, string) (*storage.User, error) {
	return nil, nil
}
func (m *mockUserRepo) Delete(context.Context, string, string) error { return nil }
func (m *mockUserRepo) ResetPassword(context.Context, string) (string, error) {
	return "stub", nil
}
```
(Add `"github.com/vlgrigoriev/coeus/internal/storage"` to imports if not present — it already is.)

In `internal/httpapi/middleware_test.go`, add the same four methods to `fakeUserRepo`:

```go
func (f *fakeUserRepo) List(context.Context, storage.UserFilter, int, int) ([]*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) Update(context.Context, string, storage.UserUpdate, string) (*storage.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) Delete(context.Context, string, string) error { return nil }
func (f *fakeUserRepo) ResetPassword(context.Context, string) (string, error) {
	return "stub", nil
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_List -timeout 180s -v
go test -short ./internal/httpapi/ ./internal/httpapi/handlers/
```
Expected: PASS (repo List; fakes still compile).

- [ ] **Step 7: Vet + commit**

```bash
go vet ./internal/storage/... ./internal/httpapi/...
git add internal/storage/ports.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go \
        internal/httpapi/handlers/auth_test.go internal/httpapi/middleware_test.go
git commit -m "feat(storage): UserRepo.List with UserFilter/UserUpdate types"
```

---

## Task 7: `UserRepo.Update` (transactional guards + conditional token bump)

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/user_repo.go`
- Modify: `internal/storage/postgres/user_repo_test.go`

**Interfaces:**
- Consumes: `storage.UserUpdate` (Task 6); `domain.ErrSelfForbidden`/`ErrLastAdmin`/`ErrDuplicate`/`ErrNotFound` (Task 2).
- Produces: `UserRepo.Update(ctx, id string, upd UserUpdate, callerID string) (*User, error)` — full replacement with `SELECT ... FOR UPDATE`, value-aware self-protection, last-active-admin guard, email-uniqueness check, and `token_version` bump only on a real `role`/`active` change.

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/postgres/user_repo_test.go`:

```go
func TestUserRepo_Update_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "up@example.com", "h", "user")
	// Another admin performs the update.
	caller, _ := repo.Create(ctx, "caller@example.com", "h", "admin")

	updated, err := repo.Update(ctx, target.ID, storage.UserUpdate{
		Email: "changed@example.com", Role: "expert", Active: true,
	}, caller.ID)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Email != "changed@example.com" || updated.Role != "expert" {
		t.Errorf("updated = %+v", updated)
	}
	// role changed user->expert => token_version bumped from 0 to 1.
	if updated.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1 (role changed)", updated.TokenVersion)
	}
}

func TestUserRepo_Update_NoBumpOnSameRoleActive(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "nb@example.com", "h", "expert")
	// First bump it to 1 via a role change.
	repo.Update(ctx, target.ID, storage.UserUpdate{Email: "nb@example.com", Role: "user", Active: true}, "someone-else")
	// Now send the SAME role/active back (only email differs) => no bump.
	updated, err := repo.Update(ctx, target.ID, storage.UserUpdate{Email: "nb2@example.com", Role: "user", Active: true}, "someone-else")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1 (no bump on pure email change)", updated.TokenVersion)
	}
	if updated.Email != "nb2@example.com" {
		t.Errorf("email not updated: %q", updated.Email)
	}
}

func TestUserRepo_Update_SelfForbidden(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "self@example.com", "h", "admin")
	_, err := repo.Update(ctx, self.ID, storage.UserUpdate{
		Email: "self@example.com", Role: "user", Active: true,
	}, self.ID)
	if !errors.Is(err, domain.ErrSelfForbidden) {
		t.Errorf("err = %v, want ErrSelfForbidden", err)
	}
}

func TestUserRepo_Update_SelfEmailAllowed(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "selfmail@example.com", "h", "admin")
	// Editing own email only (role/active unchanged) is allowed.
	updated, err := repo.Update(ctx, self.ID, storage.UserUpdate{
		Email: "selfmail2@example.com", Role: "admin", Active: true,
	}, self.ID)
	if err != nil {
		t.Fatalf("self email edit should be allowed: %v", err)
	}
	if updated.TokenVersion != 0 {
		t.Errorf("TokenVersion = %d, want 0 (no bump)", updated.TokenVersion)
	}
}

func TestUserRepo_Update_LastAdmin(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	only, _ := repo.Create(ctx, "only-admin@example.com", "h", "admin")
	caller, _ := repo.Create(ctx, "caller2@example.com", "h", "admin")
	_, err := repo.Update(ctx, only.ID, storage.UserUpdate{
		Email: "only-admin@example.com", Role: "user", Active: true,
	}, caller.ID)
	if !errors.Is(err, domain.ErrLastAdmin) {
		t.Errorf("err = %v, want ErrLastAdmin", err)
	}
}

func TestUserRepo_Update_DuplicateEmail(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	repo.Create(ctx, "taken@example.com", "h", "user")
	target, _ := repo.Create(ctx, "orig@example.com", "h", "user")
	caller, _ := repo.Create(ctx, "caller3@example.com", "h", "admin")
	_, err := repo.Update(ctx, target.ID, storage.UserUpdate{
		Email: "taken@example.com", Role: "user", Active: true,
	}, caller.ID)
	if !errors.Is(err, domain.ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
}

func TestUserRepo_Update_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.Update(ctx, "00000000-0000-0000-0000-000000000000", storage.UserUpdate{
		Email: "x@example.com", Role: "user", Active: true,
	}, "caller")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_Update -timeout 180s -v
```
Expected: FAIL / compile error (`UserRepo.Update` undefined).

- [ ] **Step 3: Add the interface method**

In `internal/storage/ports.go`, add to `UserRepo`:

```go
	Update(ctx context.Context, id string, upd UserUpdate, callerID string) (*User, error)
```

- [ ] **Step 4: Implement Update**

In `internal/storage/postgres/user_repo.go`:

```go
func (r *UserRepo) Update(ctx context.Context, id string, upd storage.UserUpdate, callerID string) (*storage.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update user: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the target row and read its current state before guard counts.
	var cur storage.User
	err = tx.QueryRow(ctx, `
		SELECT id, email, password_hash, role, active, token_version,
		       to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM users WHERE id = $1 FOR UPDATE
	`, id).Scan(&cur.ID, &cur.Email, &cur.PasswordHash, &cur.Role, &cur.Active, &cur.TokenVersion, &cur.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("update user: %w", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("update user select: %w", err)
	}

	roleChanged := cur.Role != upd.Role
	activeChanged := cur.Active != upd.Active
	emailChanged := cur.Email != upd.Email

	// Self-protection: caller cannot alter their own role or active state.
	if id == callerID && (roleChanged || activeChanged) {
		return nil, fmt.Errorf("update user: %w", domain.ErrSelfForbidden)
	}

	// Last-active-admin guard.
	if cur.Role == "admin" && cur.Active && (upd.Role != "admin" || !upd.Active) {
		var activeAdmins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND active = true`).Scan(&activeAdmins); err != nil {
			return nil, fmt.Errorf("count admins: %w", err)
		}
		if activeAdmins <= 1 {
			return nil, fmt.Errorf("update user: %w", domain.ErrLastAdmin)
		}
	}

	// Email uniqueness (case-sensitive, excludes self).
	if emailChanged {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email = $1 AND id <> $2)`, upd.Email, id).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check email unique: %w", err)
		}
		if exists {
			return nil, fmt.Errorf("update user: %w", domain.ErrDuplicate)
		}
	}

	newVersion := cur.TokenVersion
	if roleChanged || activeChanged {
		newVersion++
	}

	var updated storage.User
	err = tx.QueryRow(ctx, `
		UPDATE users SET email = $1, role = $2, active = $3, token_version = $4
		WHERE id = $5
		RETURNING id, email, password_hash, role, active, token_version,
		          to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	`, upd.Email, upd.Role, upd.Active, newVersion, id).Scan(
		&updated.ID, &updated.Email, &updated.PasswordHash, &updated.Role, &updated.Active, &updated.TokenVersion, &updated.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("update user: %w", domain.ErrDuplicate)
		}
		return nil, fmt.Errorf("update user exec: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update user: %w", err)
	}
	return &updated, nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_Update -timeout 180s -v
```
Expected: PASS (all 7 cases).

- [ ] **Step 6: Vet + commit**

```bash
go vet ./internal/storage/...
git add internal/storage/ports.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go
git commit -m "feat(storage): UserRepo.Update with self/last-admin guards and conditional token bump"
```

---

## Task 8: `UserRepo.Delete` (transactional guards)

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/user_repo.go`
- Modify: `internal/storage/postgres/user_repo_test.go`

**Interfaces:**
- Consumes: `domain.ErrSelfForbidden`/`ErrLastAdmin`/`ErrNotFound`; Task 1's `verified_by ON DELETE SET NULL`.
- Produces: `UserRepo.Delete(ctx, id, callerID string) error` — hard delete with `SELECT ... FOR UPDATE`, self-protection, last-admin guard. Cascades `session_questions`→`images`→`jobs`; `questions.verified_by` set NULL by migration.

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/postgres/user_repo_test.go`:

```go
func TestUserRepo_Delete_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "del@example.com", "h", "user")
	caller, _ := repo.Create(ctx, "del-caller@example.com", "h", "admin")

	if err := repo.Delete(ctx, target.ID, caller.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.FindByID(ctx, target.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("after delete, FindByID err = %v, want ErrNotFound", err)
	}
}

func TestUserRepo_Delete_SelfForbidden(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	self, _ := repo.Create(ctx, "del-self@example.com", "h", "admin")
	err := repo.Delete(ctx, self.ID, self.ID)
	if !errors.Is(err, domain.ErrSelfForbidden) {
		t.Errorf("err = %v, want ErrSelfForbidden", err)
	}
}

func TestUserRepo_Delete_LastAdmin(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	only, _ := repo.Create(ctx, "del-only@example.com", "h", "admin")
	caller, _ := repo.Create(ctx, "del-caller2@example.com", "h", "admin")
	err := repo.Delete(ctx, only.ID, caller.ID)
	if !errors.Is(err, domain.ErrLastAdmin) {
		t.Errorf("err = %v, want ErrLastAdmin", err)
	}
}

func TestUserRepo_Delete_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	err := repo.Delete(ctx, "00000000-0000-0000-0000-000000000000", "caller")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_Delete -timeout 180s -v
```
Expected: FAIL / compile error.

- [ ] **Step 3: Add the interface method**

In `internal/storage/ports.go`, add to `UserRepo`:

```go
	Delete(ctx context.Context, id, callerID string) error
```

- [ ] **Step 4: Implement Delete**

In `internal/storage/postgres/user_repo.go`:

```go
func (r *UserRepo) Delete(ctx context.Context, id, callerID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete user: %w", err)
	}
	defer tx.Rollback(ctx)

	var role string
	var active bool
	err = tx.QueryRow(ctx, `SELECT role, active FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role, &active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("delete user: %w", domain.ErrNotFound)
		}
		return fmt.Errorf("delete user select: %w", err)
	}

	if id == callerID {
		return fmt.Errorf("delete user: %w", domain.ErrSelfForbidden)
	}

	if role == "admin" && active {
		var activeAdmins int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND active = true`).Scan(&activeAdmins); err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if activeAdmins <= 1 {
			return fmt.Errorf("delete user: %w", domain.ErrLastAdmin)
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete user exec: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete user: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_Delete -timeout 180s -v
```
Expected: PASS.

- [ ] **Step 6: Vet + commit**

```bash
go vet ./internal/storage/...
git add internal/storage/ports.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go
git commit -m "feat(storage): UserRepo.Delete with self/last-admin guards"
```

---

## Task 9: `UserRepo.ResetPassword`

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/user_repo.go`
- Modify: `internal/storage/postgres/user_repo_test.go`

**Interfaces:**
- Consumes: `auth.GeneratePassword` + `auth.HashPassword` (Task 4); `FindByID` (404).
- Produces: `UserRepo.ResetPassword(ctx, id string) (plaintext string, err error)` — generates a 20-char password, bcrypt-hashes it, writes `password_hash`, bumps `token_version`, returns plaintext once. Does **not** touch `active`.

> **Import note:** `internal/storage/postgres/user_repo.go` will now import `github.com/vlgrigoriev/coeus/internal/auth`. This is acyclic (`auth` imports only `config`; it does not import `storage` or `postgres`).

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/postgres/user_repo_test.go`:

```go
import "github.com/vlgrigoriev/coeus/internal/auth"

func TestUserRepo_ResetPassword(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	target, _ := repo.Create(ctx, "rp@example.com", "old-hash", "user")

	plaintext, err := repo.ResetPassword(ctx, target.ID)
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if len(plaintext) != 20 {
		t.Errorf("len(plaintext) = %d, want 20", len(plaintext))
	}

	// token_version bumped from 0 to 1.
	after, _ := repo.FindByID(ctx, target.ID)
	if after.TokenVersion != 1 {
		t.Errorf("TokenVersion = %d, want 1", after.TokenVersion)
	}
	// active untouched.
	if !after.Active {
		t.Errorf("Active = false, want true (reset must not deactivate)")
	}
	// A NEW bcrypt hash replaced the old one and verifies the plaintext.
	if after.PasswordHash == "old-hash" {
		t.Error("password_hash was not replaced")
	}
	if !auth.VerifyPassword(after.PasswordHash, plaintext) {
		t.Error("new hash does not verify the generated plaintext")
	}
}

func TestUserRepo_ResetPassword_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool)
	ctx := context.Background()

	_, err := repo.ResetPassword(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo_ResetPassword -timeout 180s -v
```
Expected: FAIL / compile error.

- [ ] **Step 3: Add the interface method**

In `internal/storage/ports.go`, add to `UserRepo`:

```go
	ResetPassword(ctx context.Context, id string) (string, error)
```

- [ ] **Step 4: Implement ResetPassword**

In `internal/storage/postgres/user_repo.go`, add the import and method:

```go
import (
	// existing...
	"github.com/vlgrigoriev/coeus/internal/auth"
)
```

```go
func (r *UserRepo) ResetPassword(ctx context.Context, id string) (string, error) {
	// 404 first (spec: not_found if target absent).
	if _, err := r.FindByID(ctx, id); err != nil {
		return "", err
	}

	plaintext, err := auth.GeneratePassword()
	if err != nil {
		return "", fmt.Errorf("reset password generate: %w", err)
	}
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		return "", fmt.Errorf("reset password hash: %w", err)
	}

	if _, err := r.pool.Exec(ctx, `
		UPDATE users SET password_hash = $1, token_version = token_version + 1
		WHERE id = $2
	`, hash, id); err != nil {
		return "", fmt.Errorf("reset password exec: %w", err)
	}
	return plaintext, nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/storage/postgres/ -run TestUserRepo -timeout 180s -v
```
Expected: PASS (all `TestUserRepo_*`).

- [ ] **Step 6: Vet + commit**

```bash
go vet ./internal/storage/...
git add internal/storage/ports.go internal/storage/postgres/user_repo.go internal/storage/postgres/user_repo_test.go
git commit -m "feat(storage): UserRepo.ResetPassword generates password, bumps token_version"
```

---

## Task 10: `QuestionRepo.Delete` (question_in_use guard)

**Files:**
- Modify: `internal/storage/ports.go`
- Modify: `internal/storage/postgres/question_repo.go`
- Modify: `internal/storage/postgres/question_repo_test.go`
- Modify: `internal/httpapi/handlers/questions_test.go` (add `Delete` stub to `fakeQuestionRepo`)

**Interfaces:**
- Consumes: `domain.QuestionStatusError`; existing `QuestionRepo.FindByID`; existing `session_questions.question_id` cascade.
- Produces: `QuestionRepo.Delete(ctx, id string) error` — 404 if absent; `409 question_in_use` (message names the count) if referenced by `session_questions` and `status != 'error'`; else hard delete (cascades `session_questions` + `question_tags`).

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/postgres/question_repo_test.go`:

```go
func TestQuestionRepo_Delete(t *testing.T) {
	ctx := context.Background()
	pool := setupTestDB(t)
	questions := NewQuestionRepo(pool)

	// 404 when absent.
	err := questions.Delete(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("absent: err = %v, want ErrNotFound", err)
	}

	// Deletable when zero references.
	free, _ := questions.Create(ctx, &domain.Question{Text: "Free", TextHash: "del-free", TextNorm: "free", Status: domain.QuestionStatusVerified, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	if err := questions.Delete(ctx, free); err != nil {
		t.Fatalf("delete free: %v", err)
	}
	if _, err := questions.FindByID(ctx, free); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("free after delete: err = %v, want ErrNotFound", err)
	}

	// Deletable when referenced but status='error'.
	// (Build a referenced error question via image/session infra.)
	imgs := NewImageRepo(pool)
	sessions := NewSessionRepo(pool)
	users := NewUserRepo(pool)
	u, _ := users.Create(ctx, "qdel@example.com", "h", "user")
	sess, _ := sessions.Create(ctx, u.ID, 3600, 0)
	imgID, _ := imgs.Create(ctx, sess.ID, []byte("o"), "image/png", 1, 1)
	errQ, _ := questions.Create(ctx, &domain.Question{Text: "Err", TextHash: "del-err", TextNorm: "err", Status: domain.QuestionStatusError, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	questions.LinkToSession(ctx, sess.ID, imgID, errQ, 1, 0.9)
	if err := questions.Delete(ctx, errQ); err != nil {
		t.Fatalf("delete error question (referenced): %v", err)
	}

	// 409 question_in_use when referenced and not error.
	inUse, _ := questions.Create(ctx, &domain.Question{Text: "InUse", TextHash: "del-inuse", TextNorm: "inuse", Status: domain.QuestionStatusVerified, Choices: []string{"a"}, ChoiceLabeling: "letter"})
	questions.LinkToSession(ctx, sess.ID, imgID, inUse, 2, 0.9)
	err = questions.Delete(ctx, inUse)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "question_in_use" {
		t.Fatalf("in-use: err = %v, want code question_in_use", err)
	}
	if !strings.Contains(de.Message, "1 session") {
		t.Errorf("in-use message = %q, want it to name the count", de.Message)
	}
}
```

(Ensure `"errors"` and `"strings"` are imported in this test file.)

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/storage/postgres/ -run TestQuestionRepo_Delete -timeout 180s -v
```
Expected: FAIL / compile error.

- [ ] **Step 3: Add the interface method**

In `internal/storage/ports.go`, add to `QuestionRepo`:

```go
	Delete(ctx context.Context, id string) error
```

- [ ] **Step 4: Implement Delete**

In `internal/storage/postgres/question_repo.go`:

```go
func (r *QuestionRepo) Delete(ctx context.Context, id string) error {
	q, err := r.FindByID(ctx, id)
	if err != nil {
		return err // wraps domain.ErrNotFound
	}

	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM session_questions WHERE question_id = $1`, id).Scan(&n); err != nil {
		return fmt.Errorf("count session_questions: %w", err)
	}
	if n > 0 && q.Status != domain.QuestionStatusError {
		return domain.NewError("question_in_use", fmt.Sprintf("question is linked to %d session(s)", n))
	}

	if _, err := r.pool.Exec(ctx, `DELETE FROM questions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete question: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Add Delete stub to fakeQuestionRepo**

In `internal/httpapi/handlers/questions_test.go`, add to `fakeQuestionRepo`:

```go
func (f *fakeQuestionRepo) Delete(context.Context, string) error { return nil }
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/storage/postgres/ -run TestQuestionRepo_Delete -timeout 180s -v
go test -short ./internal/httpapi/handlers/
```
Expected: PASS (repo Delete; `fakeQuestionRepo` still satisfies the interface).

- [ ] **Step 7: Vet + commit**

```bash
go vet ./internal/storage/... ./internal/httpapi/handlers/
git add internal/storage/ports.go internal/storage/postgres/question_repo.go internal/storage/postgres/question_repo_test.go internal/httpapi/handlers/questions_test.go
git commit -m "feat(storage): QuestionRepo.Delete with question_in_use guard"
```

---

## Task 11: Handlers + DTOs (`UserHandler`, `Profile`, `QuestionHandler.Delete`)

**Files:**
- Modify: `internal/httpapi/dto/responses.go`, `internal/httpapi/dto/requests.go`
- Create: `internal/httpapi/handlers/users.go`
- Create: `internal/httpapi/handlers/users_test.go`
- Modify: `internal/httpapi/handlers/auth.go` (add `Profile`)
- Modify: `internal/httpapi/handlers/questions.go` (add `Delete`)

**Interfaces:**
- Consumes: Tasks 6–10 repo methods; Task 5's gin-context `"user"` stash; existing `parsePaging`/`errorResponse`/`toUserResponse`.
- Produces:
  - `dto.UserResponse`, `dto.UserListResponse`, `dto.ResetPasswordResponse`, `dto.UpdateUserRequest`.
  - `handlers.UserHandler{List, Update, Delete, ResetPassword}` + `toUserResponse`.
  - `AuthHandler.Profile` (`GET /api/v1/profile`).
  - `QuestionHandler.Delete` (`DELETE /questions/:id`).

- [ ] **Step 1: Add DTOs**

In `internal/httpapi/dto/responses.go`, append:

```go
// UserResponse is the shared user shape returned by /profile and /users endpoints.
// It NEVER exposes password_hash or token_version (spec §Shared UserResponse).
type UserResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

// UserListResponse wraps a paginated user list (no total field — matches
// QuestionListResponse / SessionListResponse precedent).
type UserListResponse struct {
	Data    []UserResponse `json:"data"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
}

// ResetPasswordResponse returns the generated plaintext exactly once.
type ResetPasswordResponse struct {
	Password string `json:"password"`
}
```

In `internal/httpapi/dto/requests.go`, append:

```go
// UpdateUserRequest is the body of PUT /api/v1/users/:id (admin-only, full-replace).
type UpdateUserRequest struct {
	Email  string `json:"email"  binding:"required,email"`
	Role   string `json:"role"   binding:"required,oneof=user expert admin"`
	Active bool   `json:"active"`
}
```

> `active` has no `required` tag: gin/validator cannot distinguish an absent `false` from a present `false` (false is the bool zero value). Clients must send an explicit `true`/`false`; the handler treats the received value as authoritative (PUT semantics).

- [ ] **Step 2: Create UserHandler + helper**

Create `internal/httpapi/handlers/users.go`:

```go
package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// UserHandler serves the admin-only /api/v1/users endpoints (spec §Endpoints).
type UserHandler struct {
	users storage.UserRepo
}

func NewUserHandler(users storage.UserRepo) *UserHandler {
	return &UserHandler{users: users}
}

func toUserResponse(u *storage.User) dto.UserResponse {
	return dto.UserResponse{
		ID:        u.ID,
		Email:     u.Email,
		Role:      u.Role,
		Active:    u.Active,
		CreatedAt: u.CreatedAt,
	}
}

// parseUserPaging parses page/per_page with spec-exact clamping: per_page above
// the cap is clamped to 100 (NOT reset to the default). This is deliberately
// distinct from questions.parsePaging (which resets out-of-range to the default)
// so that widening user paging does not alter the existing questions behavior.
func parseUserPaging(c *gin.Context) (page, perPage, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ = strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage, (page - 1) * perPage
}

// List — GET /api/v1/users (admin).
func (h *UserHandler) List(c *gin.Context) {
	page, perPage, offset := parseUserPaging(c)

	filter := storage.UserFilter{}
	if r := c.Query("role"); r != "" {
		filter.Role = &r
	}
	if a := c.Query("active"); a != "" {
		b := a == "true"
		filter.Active = &b
	}
	if q := c.Query("q"); q != "" {
		filter.Query = &q
	}

	users, err := h.users.List(c.Request.Context(), filter, perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	data := make([]dto.UserResponse, 0, len(users))
	for _, u := range users {
		data = append(data, toUserResponse(u))
	}
	c.JSON(http.StatusOK, dto.UserListResponse{Data: data, Page: page, PerPage: perPage})
}

// Update — PUT /api/v1/users/:id (admin). Full replacement.
func (h *UserHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	callerID := c.GetString("user_id")

	updated, err := h.users.Update(c.Request.Context(), id, storage.UserUpdate{
		Email: req.Email, Role: req.Role, Active: req.Active,
	}, callerID)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}
	c.JSON(http.StatusOK, toUserResponse(updated))
}

// Delete — DELETE /api/v1/users/:id (admin).
func (h *UserHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	callerID := c.GetString("user_id")
	if err := h.users.Delete(c.Request.Context(), id, callerID); err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// ResetPassword — POST /api/v1/users/:id/reset-password (admin).
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id := c.Param("id")
	plaintext, err := h.users.ResetPassword(c.Request.Context(), id)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}
	c.JSON(http.StatusOK, dto.ResetPasswordResponse{Password: plaintext})
}
```

- [ ] **Step 3: Add Profile to AuthHandler**

In `internal/httpapi/handlers/auth.go`, append:

```go
// Profile — GET /api/v1/profile. Reads the *storage.User stashed by AuthMiddleware.
func (h *AuthHandler) Profile(c *gin.Context) {
	v, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	user := v.(*storage.User)
	c.JSON(http.StatusOK, toUserResponse(user))
}
```

- [ ] **Step 4: Add Delete to QuestionHandler**

In `internal/httpapi/handlers/questions.go`, append:

```go
// Delete — DELETE /api/v1/questions/:id (expert, admin).
func (h *QuestionHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.questions.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		// question_in_use (and any other domain error) maps via HTTPStatus.
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}
	c.Status(http.StatusNoContent)
}
```

- [ ] **Step 5: Write UserHandler unit tests (controllable fake)**

Create `internal/httpapi/handlers/users_test.go`:

```go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// ctrlUserRepo is a controllable UserRepo for handler unit tests.
type ctrlUserRepo struct {
	list         func(ctx context.Context, f storage.UserFilter, limit, off int) ([]*storage.User, error)
	update       func(ctx context.Context, id string, upd storage.UserUpdate, caller string) (*storage.User, error)
	del          func(ctx context.Context, id, caller string) error
	reset        func(ctx context.Context, id string) (string, error)
}

func (c *ctrlUserRepo) Create(context.Context, string, string, string) (*storage.User, error) { return nil, nil }
func (c *ctrlUserRepo) FindByEmail(context.Context, string) (*storage.User, error)             { return nil, nil }
func (c *ctrlUserRepo) FindByID(context.Context, string) (*storage.User, error)                { return nil, nil }
func (c *ctrlUserRepo) List(ctx context.Context, f storage.UserFilter, l, o int) ([]*storage.User, error) {
	if c.list != nil {
		return c.list(ctx, f, l, o)
	}
	return nil, nil
}
func (c *ctrlUserRepo) Update(ctx context.Context, id string, upd storage.UserUpdate, caller string) (*storage.User, error) {
	return c.update(ctx, id, upd, caller)
}
func (c *ctrlUserRepo) Delete(ctx context.Context, id, caller string) error { return c.del(ctx, id, caller) }
func (c *ctrlUserRepo) ResetPassword(ctx context.Context, id string) (string, error) {
	return c.reset(ctx, id)
}

func setUserRouter(role, userID string, h *UserHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	return r
}

func TestUserHandler_List_MapsToResponse(t *testing.T) {
	repo := &ctrlUserRepo{list: func(context.Context, storage.UserFilter, int, int) ([]*storage.User, error) {
		return []*storage.User{{ID: "u1", Email: "a@x.com", Role: "user", Active: true, CreatedAt: "2026-07-15T00:00:00Z"}}, nil
	}}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.GET("/users", h.List)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/users?role=admin&active=true&q=foo&page=2&per_page=5", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp dto.UserListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].ID != "u1" || !resp.Data[0].Active {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
	if resp.Page != 2 || resp.PerPage != 5 {
		t.Errorf("paging = page %d per %d", resp.Page, resp.PerPage)
	}
}

func TestUserHandler_Update_Validation400(t *testing.T) {
	h := NewUserHandler(&ctrlUserRepo{update: func(context.Context, string, storage.UserUpdate, string) (*storage.User, error) {
		t.Error("update must not run on bad body")
		return nil, nil
	}})
	r := setUserRouter("admin", "caller", h)
	r.PUT("/users/:id", h.Update)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/users/u1", bytes.NewReader([]byte(`{"email":"bad","role":"x"}`))))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUserHandler_Update_SelfForbidden409(t *testing.T) {
	repo := &ctrlUserRepo{update: func(_ context.Context, _ string, _ storage.UserUpdate, _ string) (*storage.User, error) {
		return nil, domain.ErrSelfForbidden
	}}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.PUT("/users/:id", h.Update)

	body, _ := json.Marshal(map[string]any{"email": "a@x.com", "role": "user", "active": true})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/users/u1", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	var env map[string]map[string]string
	json.Unmarshal(w.Body.Bytes(), &env)
	if env["error"]["code"] != "self_forbidden" {
		t.Errorf("code = %v, want self_forbidden", env["error"]["code"])
	}
}

func TestUserHandler_Delete_204(t *testing.T) {
	repo := &ctrlUserRepo{del: func(context.Context, string, string) error { return nil }}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.DELETE("/users/:id", h.Delete)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/users/u1", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

func TestUserHandler_ResetPassword_ReturnsPlaintext(t *testing.T) {
	repo := &ctrlUserRepo{reset: func(context.Context, string) (string, error) { return "abc1234567890qrstUV", nil }}
	h := NewUserHandler(repo)
	r := setUserRouter("admin", "caller", h)
	r.POST("/users/:id/reset-password", h.ResetPassword)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/users/u1/reset-password", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["password"] != "abc1234567890qrstUV" {
		t.Errorf("password = %q", resp["password"])
	}
}
```

(`dto` is imported by the snippet above — no extra import needed.)

- [ ] **Step 6: Run tests**

```bash
go test -short ./internal/httpapi/... -v
```
Expected: PASS (handler unit tests + existing middleware/server tests).

- [ ] **Step 7: Vet + commit**

```bash
go vet ./internal/httpapi/...
git add internal/httpapi/dto/responses.go internal/httpapi/dto/requests.go \
        internal/httpapi/handlers/users.go internal/httpapi/handlers/users_test.go \
        internal/httpapi/handlers/auth.go internal/httpapi/handlers/questions.go
git commit -m "feat(httpapi): UserHandler, Profile, QuestionHandler.Delete + user DTOs"
```

---

## Task 12: Wiring (routes + widened guards) + route-authorization integration tests + final verification

**Files:**
- Modify: `internal/httpapi/server.go`
- Create: `internal/storage/postgres/server_auth_test.go`

**Interfaces:**
- Consumes: all prior tasks; `setupTestDB` (unexported in package `postgres`, so these integration tests live in package `postgres`).
- Produces: the final wired route table (6 new routes, 4 widened expert routes) and proof that role guards enforce the spec at the real wiring level.

> **No `wire.go` change required.** `httpapi.Server` already holds `s.userRepo` and constructs handlers inside `registerRoutes`; `UserHandler` follows the same pattern. The app's `Build` already passes `userRepo` to `NewServer`.

> **Test placement:** `setupTestDB` is unexported in package `postgres`, and `httpapi` does not import `postgres` (no import cycle). So the route-auth integration tests live in `internal/storage/postgres`, constructing a real `httpapi.Server` against the test pool.

- [ ] **Step 1: Update registerRoutes**

In `internal/httpapi/server.go` `registerRoutes`, the role strings are written inline (no new constants) so the widening is visible at each call site. Inside the `apiGroup` block (after `authHandler` is in scope and the `apiGroup.Use(AuthMiddleware(...))` line), add `/profile` and the `/users` admin group:

```go
		// Profile — any authenticated user (no RoleGuard).
		apiGroup.GET("/profile", authHandler.Profile)

		// User management — admin only (spec §Authorization).
		userHandler := handlers.NewUserHandler(s.userRepo)
		users := apiGroup.Group("/users", RoleGuard("admin"))
		{
			users.GET("", userHandler.List)
			users.PUT("/:id", userHandler.Update)
			users.DELETE("/:id", userHandler.Delete)
			users.POST("/:id/reset-password", userHandler.ResetPassword)
		}
```

Then widen the existing expert routes to accept admin, and add the question DELETE (replace the existing `questions` block and the `expertImages` group line):

```go
		questionHandler := handlers.NewQuestionHandler(s.questionRepo, s.sessionRepo, s.embedder)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.POST("", RoleGuard("expert", "admin"), questionHandler.Create)
			questions.PUT("/:id", RoleGuard("expert", "admin"), questionHandler.Update)
			questions.DELETE("/:id", RoleGuard("expert", "admin"), questionHandler.Delete)
		}

		// Expert image access — expert + admin (spec §Authorization).
		expertHandler := handlers.NewExpertHandler(s.imageRepo)
		expertImages := apiGroup.Group("/images", RoleGuard("expert", "admin"))
		{
			expertImages.GET("/:id", expertHandler.GetImage)
			expertImages.GET("/:id/verification-report", expertHandler.GetVerificationReport)
		}
```

- [ ] **Step 2: Verify the route table compiles + existing guard tests still pass**

```bash
go vet ./internal/httpapi/...
go test -short ./internal/httpapi/... -v
```
Expected: PASS. (`server_test.go`'s `RoleGuard("expert")` tests still pass — variadic with one arg is backward compatible.)

- [ ] **Step 3: Write route-authorization integration tests**

Create `internal/storage/postgres/server_auth_test.go`. It boots a real `httpapi.Server` against the Testcontainers pool and exercises the wired guards with real JWTs (so the per-request `FindByID` validation in `AuthMiddleware` is exercised end-to-end).

```go
package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// bootTestServer builds a real httpapi.Server against the test pool with a
// known JWT secret. embedder is nil (not exercised by these routes).
func bootTestServer(t *testing.T, pool *pgxpool.Pool) (*httpapi.Server, *auth.JWTManager, *UserRepo) {
	t.Helper()
	jwt := auth.NewJWTManager(config.JWTConfig{Secret: "route-auth-secret", AccessTTL: time.Hour})
	userRepo := NewUserRepo(pool)
	srv := httpapi.NewServer(
		userRepo,
		NewSessionRepo(pool),
		NewImageRepo(pool),
		NewQuestionRepo(pool),
		NewJobQueue(pool),
		jwt, pool,
		config.UploadConfig{},
		nil, // embedder unused
		config.CORSConfig{AllowedOrigins: []string{"*"}},
	)
	return srv, jwt, userRepo
}

// tokenFor creates a user of `role` and returns a valid Bearer token for it.
func tokenFor(t *testing.T, repo *UserRepo, jwt *auth.JWTManager, email, role string) string {
	t.Helper()
	u, err := repo.Create(context.Background(), email, "hash", role)
	if err != nil {
		t.Fatalf("create %s %s: %v", role, email, err)
	}
	tok, err := jwt.Issue(u.ID, u.Role, u.Active, u.TokenVersion)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return "Bearer " + tok
}

func do(t *testing.T, h http.Handler, method, target, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRouteAuth_ProfileWorksForAllRoles(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	ctx := context.Background()

	for _, role := range []string{"user", "expert", "admin"} {
		email := role + "-profile@example.com"
		u, _ := users.Create(ctx, email, "hash", role)
		tok, _ := jwt.Issue(u.ID, u.Role, u.Active, u.TokenVersion)
		w := do(t, h, "GET", "/api/v1/profile", "Bearer "+tok)
		if w.Code != http.StatusOK {
			t.Errorf("%s /profile: got %d want 200 (body=%s)", role, w.Code, w.Body.String())
		}
	}
}

func TestRouteAuth_AdminRoutesRejectNonAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()

	userTok := tokenFor(t, users, jwt, "ra-user@example.com", "user")
	expertTok := tokenFor(t, users, jwt, "ra-expert@example.com", "expert")

	cases := []struct {
		name, method, target, tok string
	}{
		{"user GET /users", "GET", "/api/v1/users", userTok},
		{"expert GET /users", "GET", "/api/v1/users", expertTok},
		{"user PUT /users/:id", "PUT", "/api/v1/users/00000000-0000-0000-0000-000000000000", userTok},
		{"user DELETE /users/:id", "DELETE", "/api/v1/users/00000000-0000-0000-0000-000000000000", userTok},
		{"user POST reset-password", "POST", "/api/v1/users/00000000-0000-0000-0000-000000000000/reset-password", userTok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.target, tc.tok)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: got %d want 403", tc.method, tc.target, w.Code)
			}
		})
	}
}

func TestRouteAuth_AdminRoutesAcceptAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	adminTok := tokenFor(t, users, jwt, "ra-admin@example.com", "admin")

	// GET /users must return 200 (not 403) for admin.
	w := do(t, h, "GET", "/api/v1/users", adminTok)
	if w.Code != http.StatusOK {
		t.Errorf("admin GET /users: got %d want 200 (body=%s)", w.Code, w.Body.String())
	}
}

func TestRouteAuth_QuestionDeleteRejectsUser(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	userTok := tokenFor(t, users, jwt, "qd-user@example.com", "user")

	w := do(t, h, "DELETE", "/api/v1/questions/00000000-0000-0000-0000-000000000000", userTok)
	if w.Code != http.StatusForbidden {
		t.Errorf("user DELETE /questions/:id: got %d want 403", w.Code)
	}
}

func TestRouteAuth_WidenedExpertRouteAcceptsAdmin(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()
	adminTok := tokenFor(t, users, jwt, "we-admin@example.com", "admin")

	// POST /questions is gated by RoleGuard("expert","admin"). Admin must NOT
	// get 403 (a 400/422 from a missing body is fine — it proves the guard let
	// the request through).
	w := do(t, h, "POST", "/api/v1/questions", adminTok)
	if w.Code == http.StatusForbidden {
		t.Fatalf("admin POST /questions: got 403, guard should have admitted admin (body=%s)", w.Body.String())
	}
}

func TestRouteAuth_ExpiredOrStaleTokenRejected(t *testing.T) {
	pool := setupTestDB(t)
	srv, jwt, users := bootTestServer(t, pool)
	h := srv.Handler()

	u, _ := users.Create(context.Background(), "stale@example.com", "hash", "user")
	// Issue with active=true, then deactivate via Update (bumps token_version).
	admin, _ := users.Create(context.Background(), "stale-admin@example.com", "hash", "admin")
	users.Update(context.Background(), u.ID, storage.UserUpdate{Email: "stale@example.com", Role: "user", Active: false}, admin.ID)
	// Token still claims active=true, version=0; live row is active=false, version=1.
	tok, _ := jwt.Issue(u.ID, u.Role, true, 0)

	w := do(t, h, "GET", "/api/v1/profile", "Bearer "+tok)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("stale token: got %d want 401 (stateless invalidation)", w.Code)
	}
}
```

(The snippet's import block is already complete — `pgxpool` for the `bootTestServer` pool param and `storage` for `storage.UserUpdate` in the stale-token test are both included.)

- [ ] **Step 4: Run the integration auth tests**
```bash
go test ./internal/storage/postgres/ -run TestRouteAuth -timeout 180s -v
```
Expected: PASS (requires Docker). These prove: `/profile` works for all roles; admin routes 403 for user/expert; admin routes 200 for admin; `DELETE /questions/:id` 403 for user; widened `POST /questions` admits admin; a deactivated user's stale token is rejected 401.

- [ ] **Step 5: Run the FULL repo-method integration suite**

```bash
go test ./internal/storage/postgres/ -timeout 180s
```
Expected: PASS (all user/question/migration/pipeline integration tests). Requires Docker.

- [ ] **Step 6: Run the full unit suite (needs libvips only for the cgo `enhancer` package)**

If libvips is installed:
```bash
go test -short ./...
```
If libvips is NOT installed, run the non-cgo surface explicitly:
```bash
go test -short ./internal/auth/ ./internal/domain/ ./internal/httpapi/... ./internal/storage/...
```
Expected: PASS.

- [ ] **Step 7: Verify a full build (libvips required)**

```bash
go build ./...
```
> This requires `CGO_ENABLED=1` + libvips headers (`brew install vips pkg-config`). If unavailable, use `docker build -t coeus .` instead and confirm it succeeds. The build must succeed with no errors.

- [ ] **Step 8: Vet the whole module**

```bash
go vet ./...
```
Expected: no issues (requires libvips for the same reason as build; otherwise vet the non-cgo packages listed in Step 6).

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/server.go internal/storage/postgres/server_auth_test.go
git commit -m "feat(httpapi): wire user/profile/question-delete routes, widen expert routes to admin"
```

---

## Spec Coverage Cross-Check

| Spec section | Task(s) |
|---|---|
| Migration `0006` (role CHECK, `active`, `token_version`, `verified_by` SET NULL, index) | 1 |
| `storage.User` struct (`Active`, `TokenVersion`) + existing query updates | 3 |
| JWT `Claims` (`active`, `ver`) + `Issue` signature | 5 |
| Per-request `AuthMiddleware` revalidation + `user` context stash | 5 |
| Variadic `RoleGuard` | 5 |
| Invalidating events (bump `token_version`): password reset | 9 |
| Invalidating events: active change / role change | 7 |
| Pure token-invalidation function (no DB) | 5 |
| Password generation (`auth.GeneratePassword`) | 4 |
| Domain errors (`self_forbidden`, `last_admin` sentinels; `question_in_use` dynamic) + HTTPStatus → 409 | 2, 10 |
| `UserRepo.List` + `UserFilter`/`UserUpdate` | 6 |
| `UserRepo.Update` (self/last-admin/duplicate, conditional bump, `FOR UPDATE`) | 7 |
| `UserRepo.Delete` (self/last-admin, `FOR UPDATE`) | 8 |
| `UserRepo.ResetPassword` | 9 |
| `QuestionRepo.Delete` (`question_in_use` guard) | 10 |
| Shared `UserResponse` (no password_hash/token_version) + list/reset DTOs | 11 |
| `GET /profile` | 11 (handler) + 12 (route) |
| `GET /users` | 6 (repo) + 11 (handler) + 12 (route) |
| `PUT /users/:id` | 7 + 11 + 12 |
| `DELETE /users/:id` | 8 + 11 + 12 |
| `POST /users/:id/reset-password` | 9 + 11 + 12 |
| `DELETE /questions/:id` | 10 + 11 + 12 |
| Four expert routes widened to `("expert","admin")` | 12 |
| Wiring (`server.go` call sites, route table) — no `wire.go` change | 12 |
| Testing Strategy (unit: GeneratePassword, JWT, RoleGuard, tokenValid) | 4, 5 |
| Testing Strategy (integration: repo methods, migration, handler auth, stale-token invalidation) | 1, 6–10, 12 |

All spec sections are covered. No task invents behavior outside the approved spec.
