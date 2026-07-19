# Front-End Integration Spec: User Management & Admin RBAC

- **Date:** 2026-07-15
- **Source branch:** `feat/user-management-admin-rbac`
- **Backend spec:** `docs/superpowers/specs/2026-07-15-user-management-and-admin-rbac-design.md`
- **Status:** Backend implemented, reviewed, merge-ready
- **Audience:** Front-end application owners

This document specifies the API contract the front-end must integrate against, **scoped to changes that require front-end work**. Endpoints whose behavior did not change are omitted. New endpoints are described in full.

---

## 1. Scope summary

| Category | Count | Front-end action |
|---|---|---|
| New endpoints | 6 | Build new integration/UI |
| Existing endpoints with changed behavior | 2 areas | Modify existing code |
| Existing endpoints unchanged | (all others) | None — omitted from this doc |

The two areas of changed behavior are high-impact and described first (§3), because they affect *every* authenticated request and *several* existing screens.

---

## 2. Shared contracts

### 2.1 Roles

```
"user" | "expert" | "admin"
```

`admin` is a **superuser**: it has every `expert` power *plus* user-management power. For any UI gate that currently checks `role === "expert"`, the rule becomes `role === "expert" || role === "admin"`. See §3.2.

### 2.2 Error envelope (unchanged)

Every 4xx/5xx returns the same shape:

```json
{ "error": { "code": "string", "message": "string" } }
```

New `code` values introduced by this work (all map to HTTP **409 Conflict**):

| `code` | Meaning | Where it surfaces |
|---|---|---|
| `self_forbidden` | An admin tried to change their own `role`/`active`, or delete themselves | `PUT/DELETE /users/:id` |
| `last_admin` | Operation would leave zero active admins | `PUT/DELETE /users/:id` |
| `question_in_use` | Question cannot be deleted (verified + linked to sessions) | `DELETE /questions/:id` |

The `message` for `question_in_use` is dynamic, e.g. `"question is linked to 3 session(s)"` — display it verbatim or substitute your own copy keyed on `code`.

### 2.3 Auth header

All authenticated requests: `Authorization: Bearer <jwt>`. Access token TTL is **3h**. There is no separate refresh-token storage — `POST /auth/refresh` issues a fresh 3h access token and itself requires a valid bearer (see §3.1).

---

## 3. Changes to EXISTING behavior (requires front-end modifications)

### 3.1 A valid token can now be rejected mid-session (stateless revocation)

**What changed.** The auth middleware now re-checks the user's live DB row on **every** authenticated request. A token that is not expired and has a valid signature can still be rejected with `401` if the account's state has changed since the token was issued.

A request returns `401` (body `{"error":{"code":"unauthorized","message":"authentication required"}}`) when any of these happen *after* the current token was issued:

- The user was **deactivated** (`active = false`)
- The user's **role** or **active** flag was changed (bumps `token_version`)
- The user was **deleted**
- The user tried to log in while **deactivated** — login itself now returns `401` (previously it succeeded)

**Front-end impact.** You cannot assume `401` means only "token expired." Any authenticated call may return `401` because an admin revoked the session. There is **no refresh-from-401 recovery path**: `POST /auth/refresh` is itself behind the same middleware and will also return `401` in these cases.

**Required changes:**

1. Add a **global HTTP 401 interceptor** (response middleware on your API client) that, on any `401` from any endpoint:
   - Clears the stored token / local session
   - Redirects to the login screen
   - Aborts any in-flight retries against `/auth/refresh`
2. Do **not** attempt an automatic refresh-then-retry on `401`. That loop can no longer succeed once revocation has occurred.

> Note: this replaces any "try refresh, then retry" logic you may have today. That logic was valid when `401` only meant expiry; it is now incorrect.

### 3.2 Role-based UI gating must admit `admin` on widened routes

Five existing routes changed their allowed roles from `expert`-only to `expert` **and** `admin`. The server now permits admin; the front-end must stop hiding these actions from admins.

| Endpoint | Before | After |
|---|---|---|
| `POST /api/v1/questions` | expert | expert, admin |
| `PUT /api/v1/questions/:id` | expert | expert, admin |
| `DELETE /api/v1/questions/:id` | *(did not exist)* | expert, admin |
| `GET /api/v1/images/:id` | expert | expert, admin |
| `GET /api/v1/images/:id/verification-report` | expert | expert, admin |

**Required changes:** Anywhere the UI gates a feature on `role === "expert"`, change the predicate to include admin. Recommended helper:

```js
const canExpert = (role) => role === "expert" || role === "admin";
```

Affected UI (typical): Create/Edit Question buttons, Delete Question control (new — see §4.6), image viewer, verification-report viewer.

### 3.3 Token claims changed (if you decode the JWT client-side)

The JWT now carries two additional claims:

| Claim | Type | Meaning |
|---|---|---|
| `active` | bool | Whether the user was active at issue time |
| `ver` | int64 | Token version; changes when role/active changes |

Existing claims (`sub`, `role`, `exp`, `iat`) are unchanged, so decoding won't break. **However**, because these claims can now be stale (the whole point of §3.1), do not treat decoded `role`/`active` as authoritative for UI gating of privileged actions. Prefer `GET /profile` (§4.1) for the current state.

---

## 4. New endpoints

### 4.1 `GET /api/v1/profile` — current user

Available to **any authenticated** user. Use this as the source of truth for the logged-in user's current role/active state (claims in the JWT may be stale).

- **Auth:** `Bearer` (any role)
- **Response `200`:**

```json
{
  "id": "8f3c...uuid",
  "email": "alice@example.com",
  "role": "admin",
  "active": true,
  "created_at": "2026-07-12T09:14:22Z"
}
```

```go
type UserResponse struct {
    ID        string `json:"id"`
    Email     string `json:"email"`
    Role      string `json:"role"`
    Active    bool   `json:"active"`
    CreatedAt string `json:"created_at"`
}
```

> `token_version` and `password_hash` are never exposed.

---

### 4.2 `GET /api/v1/users` — list/filter users (admin)

- **Auth:** `Bearer` (admin only — others get `403 forbidden`)
- **Query parameters:**

| Param | Type | Default | Notes |
|---|---|---|---|
| `page` | int | `1` | Clamped to ≥ 1 |
| `per_page` | int | `20` | Values > 100 are **clamped** to 100 |
| `role` | string | none | Exact match: `user`, `expert`, `admin` |
| `active` | string | none | `true` → active only; **any other non-empty value → inactive only** |
| `q` | string | none | Case-insensitive substring on email; LIKE wildcards (`%`, `_`, `\`) are escaped |

Results are ordered **`created_at DESC`** (newest first).

- **Response `200`:**

```json
{
  "data": [ { "id": "...", "email": "...", "role": "...", "active": true, "created_at": "..." } ],
  "page": 1,
  "per_page": 20
}
```

```go
type UserListResponse struct {
    Data    []UserResponse `json:"data"`
    Page    int            `json:"page"`
    PerPage int            `json:"per_page"`
}
```

> There is **no `total`** field. Implement pagination with a "next page" probe (request `page + 1`; if `data` is empty, you're past the end) or infinite scroll — a page-count indicator is not available.

---

### 4.3 `PUT /api/v1/users/:id` — update user (admin)

- **Auth:** `Bearer` (admin only)
- **This is a full replacement**, not a patch. Send all writable fields on every call.
- **Request body:**

```json
{
  "email": "alice@example.com",
  "role": "expert",
  "active": true
}
```

```go
type UpdateUserRequest struct {
    Email  string `json:"email"  binding:"required,email"`
    Role   string `json:"role"   binding:"required,oneof=user expert admin"`
    Active bool   `json:"active"`
}
```

| Field | Required | Validation |
|---|---|---|
| `email` | yes | valid email |
| `role` | yes | one of `user`, `expert`, `admin` |
| `active` | no — **but see warning** | boolean |

> ⚠️ **Footgun:** `active` has no `omitempty`. If you omit it, JSON sends `false`, which **deactivates** the user. Always send `active: true` explicitly when you intend to keep the user active. If your edit form has an "active" toggle, bind it directly; if it doesn't, default-send `true`.

- **Response `200`:** the updated `UserResponse` (same shape as §4.1).
- **Errors:**

| Status | `code` | When |
|---|---|---|
| 400 | `validation` | bad email / bad role value |
| 403 | `forbidden` | caller is not admin |
| 404 | `not_found` | no such user id |
| 409 | `self_forbidden` | caller is editing **themself** and changes `role` or `active` |
| 409 | `last_admin` | would leave zero active admins |

> Changing a user's `role` or `active` server-side bumps their `token_version`, which invalidates their current session on their next request (§3.1). This is intentional; no extra action is needed.

---

### 4.4 `DELETE /api/v1/users/:id` — delete user (admin)

- **Auth:** `Bearer` (admin only)
- **Response `204`:** no body.
- **Errors:**

| Status | `code` | When |
|---|---|---|
| 403 | `forbidden` | caller is not admin |
| 404 | `not_found` | no such user id |
| 409 | `self_forbidden` | caller is deleting **themself** |
| 409 | `last_admin` | would leave zero active admins |

> Deleting a user sets their authored questions' `verified_by` to `NULL` (questions are preserved). No front-end handling is required for that, but question detail views should tolerate a null `verified_by`.

---

### 4.5 `POST /api/v1/users/:id/reset-password` — reset password (admin)

- **Auth:** `Bearer` (admin only)
- **Request body:** none.
- **Response `200`:**

```json
{ "password": "xC7nQp9vR2mL4Zb1Kw8y" }
```

```go
type ResetPasswordResponse struct {
    Password string `json:"password"`
}
```

- The `password` is a generated **20-character** string.
- This is the **only time** the plaintext is returned. It is hashed server-side immediately; it cannot be retrieved again.
- **Required UX:** show the password once with a copy button, and warn the admin to relay it securely. Do not store it client-side beyond the immediate display.
- This action also bumps the affected user's `token_version`, invalidating their current sessions (§3.1).

---

### 4.6 `DELETE /api/v1/questions/:id` — delete question (expert, admin)

- **Auth:** `Bearer` (expert or admin)
- **Semantics:** deletable only when `status = "error"` **or** the question has zero links to sessions. Otherwise the server refuses.
- **Response `204`:** no body.
- **Errors:**

| Status | `code` | When |
|---|---|---|
| 403 | `forbidden` | caller is neither expert nor admin |
| 404 | `not_found` | no such question id |
| 409 | `question_in_use` | question is verified and linked to ≥1 session |

> The `question_in_use` `message` includes the link count (`"question is linked to N session(s)"`). Show it or map on `code`.

---

## 5. Endpoints NOT covered here (no front-end change required)

For completeness — these exist and are **unchanged** by this work; no modifications are needed:

- `POST /api/v1/auth/register` (still creates `role: "user"`, same 201 body)
- `POST /api/v1/auth/login` — behavior tightened (deactivated accounts now 401) but the **response shape is identical** to a wrong-password 401, so existing login error handling needs no code change. It will simply reject deactivated users.
- `POST /api/v1/auth/refresh` — subject to the §3.1 revocation rule; handle via the global 401 interceptor.
- `GET/POST /api/v1/sessions...`, `GET /api/v1/questions`, `GET /api/v1/questions/:id` — unchanged.

---

## 6. Front-end migration checklist

1. **Global 401 handler** — intercept any `401`, clear session, redirect to login. Remove any "refresh-and-retry-on-401" loop. (§3.1)
2. **Role predicate** — replace `role === "expert"` with a `canExpert()` helper that includes `admin`, for the five widened routes. (§3.2)
3. **Profile screen / app shell** — call `GET /profile` on app load to get authoritative role/active; don't rely on decoded JWT claims for privileged gating. (§3.3, §4.1)
4. **Admin: Users screen** — list with `role`/`active`/`q` filters + pagination (no total); edit via full-replace `PUT` (always send `active`); delete; reset password with one-time display. (§4.2–§4.5)
5. **Expert/Admin: Delete question** — new control with `question_in_use` (409) handling. (§4.6)
6. **Tolerate null `verified_by`** on question detail after a user is deleted. (§4.4)
