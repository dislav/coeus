package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// --- standalone repo-level tests (from Tasks 1-3) ---

func TestQuestionRepo_CreateAndFindExact(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Text: "What is the capital of France?",
		TextNorm: "what is the capital of france", TextHash: "abc123hash",
		Choices: []string{"Paris", "London", "Berlin"}, Answers: []string{"Paris"},
		ChoiceLabeling: "letter", Confidence: 0.95,
		Explanation: "Paris is the capital.", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated", "geography"},
	}
	id, err := repo.Create(ctx, q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty question ID")
	}

	found, err := repo.FindExact(ctx, "abc123hash")
	if err != nil {
		t.Fatalf("FindExact: %v", err)
	}
	if found.ID != id {
		t.Errorf("found ID = %q, want %q", found.ID, id)
	}
	if len(found.Answers) != 1 || found.Answers[0] != "Paris" {
		t.Errorf("answers = %v, want [Paris]", found.Answers)
	}
}

func TestQuestionRepo_FindExactNoMatch(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	found, err := repo.FindExact(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("FindExact err: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil, got %v", found)
	}
}

func TestQuestionRepo_FindSemanticAboveThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h1",
		Choices: []string{}, Answers: []string{}, ChoiceLabeling: "letter", Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	if _, err := repo.Create(ctx, q); err != nil {
		t.Fatalf("Create: %v", err)
	}

	search := make([]float32, 1536)
	search[0] = 1.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found == nil {
		t.Fatal("expected semantic match, got nil")
	}
}

func TestQuestionRepo_FindSemanticBelowThreshold(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	emb := make([]float32, 1536)
	emb[0] = 1.0
	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h1",
		Choices: []string{}, Answers: []string{}, ChoiceLabeling: "letter", Confidence: 0.9,
		Embedding: emb, Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	if _, err := repo.Create(ctx, q); err != nil {
		t.Fatalf("Create: %v", err)
	}

	search := make([]float32, 1536)
	search[1] = 1.0 // orthogonal — cosine similarity = 0.0
	found, err := repo.FindSemantic(ctx, search, 0.92)
	if err != nil {
		t.Fatalf("FindSemantic: %v", err)
	}
	if found != nil {
		t.Error("expected no match, got result")
	}
}

func TestQuestionRepo_UpdateFromVerification(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewQuestionRepo(pool)
	ctx := context.Background()

	q := &domain.Question{
		Number: 1, Text: "q", TextNorm: "q", TextHash: "h",
		Choices: []string{"a"}, Answers: []string{"a"}, ChoiceLabeling: "letter", Confidence: 0.90,
		Explanation: "original", Embedding: make([]float32, 1536),
		Status: domain.QuestionStatusModeration, Tags: []string{"ai-generated"},
	}
	id, err := repo.Create(ctx, q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.UpdateFromVerification(ctx, id, 0.75, "original [VERIFICATION FLAG]"); err != nil {
		t.Fatalf("UpdateFromVerification: %v", err)
	}

	found, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Confidence != 0.75 {
		t.Errorf("confidence = %v, want 0.75", found.Confidence)
	}
}

func TestQuestionRepo_CountUnresolvedForImage(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool)
	sessRepo := NewSessionRepo(pool)
	imgRepo := NewImageRepo(pool)
	qRepo := NewQuestionRepo(pool)
	ctx := context.Background()

	user, _ := userRepo.Create(ctx, "count@example.com", "hash", "user")
	sess, _ := sessRepo.Create(ctx, user.ID, 3600, 300)
	imgID, _ := imgRepo.Create(ctx, sess.ID, []byte("raw"), "image/jpeg", 800, 600)

	statuses := []string{domain.QuestionStatusModeration, domain.QuestionStatusModeration, domain.QuestionStatusVerified}
	for i, status := range statuses {
		q := &domain.Question{
			Number: i + 1, Text: "q", TextNorm: "q", TextHash: "hash" + string(rune('a'+i)),
			Choices: []string{}, Answers: []string{}, ChoiceLabeling: "letter", Confidence: 0.9,
			Embedding: make([]float32, 1536), Status: status, Tags: []string{"ai-generated"},
		}
		qID, err := qRepo.Create(ctx, q)
		if err != nil {
			t.Fatalf("Create question %d: %v", i, err)
		}
		if err := qRepo.LinkToSession(ctx, sess.ID, imgID, qID, i+1, 0.9); err != nil {
			t.Fatalf("LinkToSession question %d: %v", i, err)
		}
	}

	count, err := qRepo.CountUnresolvedForImage(ctx, imgID)
	if err != nil {
		t.Fatalf("CountUnresolvedForImage: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// --- helpers for the integration scenarios below ---

func eqSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// setEq reports whether two slices contain the same multiset of elements
// (order-insensitive, duplicates counted).
func setEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, v := range a {
		m[v]++
	}
	for _, v := range b {
		m[v]--
		if m[v] < 0 {
			return false
		}
	}
	return true
}

// dataFromResponse unmarshals the list-response body and returns the "data"
// slice, failing the test on a malformed body so failures point at the real
// parse error instead of a misleading count mismatch.
func dataFromResponse(t *testing.T, w *httptest.ResponseRecorder) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("malformed response body: %v: %s", err, w.Body.String())
	}
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("response missing data array: %s", w.Body.String())
	}
	return data
}

// --- HTTP test harness (no real JWT — inject auth context directly) ---

func newIntQuestionRouter(role, userID string, q storage.QuestionRepo, s storage.SessionRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.NewQuestionHandler(q, s, nil)
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	r.GET("/api/v1/questions", h.List)
	r.GET("/api/v1/questions/:id", h.Get)
	r.PUT("/api/v1/questions/:id", h.Update)
	return r
}

func intDoReq(t *testing.T, r http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- Fixture ---

type questionsFixture struct {
	userA    *storage.User
	userB    *storage.User
	expert   *storage.User
	sessionA *domain.Session
	sessionB *domain.Session
	qAModID  string // sessionA, status=moderation
	qAVerID  string // sessionA, status=verified
	qAErrID  string // sessionA, status=error
	qBID     string // sessionB, status=verified
}

func seedQuestionsFixture(t *testing.T, ctx context.Context, users *UserRepo, sessions *SessionRepo, questions *QuestionRepo, imgs *ImageRepo) *questionsFixture {
	t.Helper()

	f := &questionsFixture{}
	var err error

	f.userA, err = users.Create(ctx, "qa-int-userA@test.com", "hash-a", "user")
	if err != nil {
		t.Fatalf("create userA: %v", err)
	}
	f.userB, err = users.Create(ctx, "qa-int-userB@test.com", "hash-b", "user")
	if err != nil {
		t.Fatalf("create userB: %v", err)
	}
	f.expert, err = users.Create(ctx, "qa-int-expert@test.com", "hash-exp", "user")
	if err != nil {
		t.Fatalf("create expert: %v", err)
	}

	f.sessionA, err = sessions.Create(ctx, f.userA.ID, 3600, 0)
	if err != nil {
		t.Fatalf("create sessionA: %v", err)
	}
	f.sessionB, err = sessions.Create(ctx, f.userB.ID, 3600, 0)
	if err != nil {
		t.Fatalf("create sessionB: %v", err)
	}

	imgA, err := imgs.Create(ctx, f.sessionA.ID, []byte("img-a"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create imageA: %v", err)
	}
	imgB, err := imgs.Create(ctx, f.sessionB.ID, []byte("img-b"), "image/png", 10, 10)
	if err != nil {
		t.Fatalf("create imageB: %v", err)
	}

	// qAMod — moderation, no verified fields.
	f.qAModID, err = questions.Create(ctx, &domain.Question{
		Text: "Q-A-mod", TextHash: "qa-mod-hash-999", TextNorm: "qa-mod",
		Status: domain.QuestionStatusModeration,
		Choices: []string{"A", "B"}, Answers: []string{"A"},
		ChoiceLabeling: "letter",
	})
	if err != nil {
		t.Fatalf("create qAMod: %v", err)
	}
	if err := questions.LinkToSession(ctx, f.sessionA.ID, imgA, f.qAModID, 1, 0.9); err != nil {
		t.Fatalf("link qAMod: %v", err)
	}

	// qAVer — verified, with verified_at / verified_by.
	now := "2026-07-01T12:00:00Z"
	f.qAVerID, err = questions.Create(ctx, &domain.Question{
		Text: "Q-A-ver", TextHash: "qa-ver-hash-999", TextNorm: "qa-ver",
		Status: domain.QuestionStatusVerified,
		Choices: []string{"C", "D"}, Answers: []string{"C"},
		ChoiceLabeling: "letter",
		VerifiedAt: &now, VerifiedBy: &f.expert.ID,
	})
	if err != nil {
		t.Fatalf("create qAVer: %v", err)
	}
	if err := questions.LinkToSession(ctx, f.sessionA.ID, imgA, f.qAVerID, 2, 0.8); err != nil {
		t.Fatalf("link qAVer: %v", err)
	}

	// qAErr — error.
	f.qAErrID, err = questions.Create(ctx, &domain.Question{
		Text: "Q-A-err", TextHash: "qa-err-hash-999", TextNorm: "qa-err",
		Status: domain.QuestionStatusError,
		Choices: []string{"E", "F"}, Answers: []string{"E"},
		ChoiceLabeling: "letter",
	})
	if err != nil {
		t.Fatalf("create qAErr: %v", err)
	}
	if err := questions.LinkToSession(ctx, f.sessionA.ID, imgA, f.qAErrID, 3, 0.0); err != nil {
		t.Fatalf("link qAErr: %v", err)
	}

	// qB — verified in sessionB.
	f.qBID, err = questions.Create(ctx, &domain.Question{
		Text: "Q-B-mod", TextHash: "qb-hash-999", TextNorm: "qb",
		Status: domain.QuestionStatusVerified,
		Choices: []string{"G", "H"}, Answers: []string{"G"},
		ChoiceLabeling: "letter",
		VerifiedAt: &now, VerifiedBy: &f.expert.ID,
	})
	if err != nil {
		t.Fatalf("create qB: %v", err)
	}
	if err := questions.LinkToSession(ctx, f.sessionB.ID, imgB, f.qBID, 1, 0.95); err != nil {
		t.Fatalf("link qB: %v", err)
	}

	return f
}

// --- Top-level integration test for scenarios (a)–(i) ---

func TestQuestionsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: requires Docker")
	}

	ctx := context.Background()
	pool := setupTestDB(t)

	users := NewUserRepo(pool)
	sessions := NewSessionRepo(pool)
	questions := NewQuestionRepo(pool)
	imgs := NewImageRepo(pool)

	f := seedQuestionsFixture(t, ctx, users, sessions, questions, imgs)

	// Subtests run sequentially and share f. Do not add t.Parallel() or
	// reorder: (b)/(c) item-counts assume (h) has not yet created its extra
	// question, and (e)/(f) share qAModID while (g) mutates qAErrID across
	// transitions.

	// ----------------------------------------------------------------
	// (a) IDOR — cross-session read blocked at the handler.
	// ----------------------------------------------------------------
	t.Run("(a) IDOR cross-session read blocked at handler", func(t *testing.T) {
		// userA tries to read userB's session → 403.
		r := newIntQuestionRouter("user", f.userA.ID, questions, sessions)
		w := intDoReq(t, r, "GET", "/api/v1/questions?session_id="+f.sessionB.ID, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("userA reading sessionB: got %d want 403: %s", w.Code, w.Body.String())
		}

		// userA reads own session → 200, only sessionA question IDs.
		w = intDoReq(t, r, "GET", "/api/v1/questions?session_id="+f.sessionA.ID, "")
		if w.Code != http.StatusOK {
			t.Fatalf("userA reading sessionA: got %d want 200: %s", w.Code, w.Body.String())
		}
		data := dataFromResponse(t, w)
		if len(data) != 3 {
			t.Fatalf("sessionA has %d questions, want 3", len(data))
		}
		ids := make(map[string]bool, 3)
		for _, item := range data {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["id"].(string)
			ids[id] = true
		}
		if !ids[f.qAModID] || !ids[f.qAVerID] || !ids[f.qAErrID] {
			t.Fatalf("sessionA missing expected IDs: got %v", ids)
		}
	})

	// ----------------------------------------------------------------
	// (b) Unified scoping — session_id yields that session's questions
	//     for both roles.
	// ----------------------------------------------------------------
	t.Run("(b) Unified scoping", func(t *testing.T) {
		// Repo-level: ListForSession(sessionB) → exactly qB.
		items, err := questions.ListForSession(ctx, f.sessionB.ID, "", 20, 0)
		if err != nil {
			t.Fatalf("ListForSession sessionB: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("sessionB has %d questions, want 1", len(items))
		}
		if items[0].Question.ID != f.qBID {
			t.Fatalf("sessionB question: got %s want %s", items[0].Question.ID, f.qBID)
		}

		// Expert session-scoped → sessionA's questions.
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)
		w := intDoReq(t, rExp, "GET", "/api/v1/questions?session_id="+f.sessionA.ID, "")
		if w.Code != http.StatusOK {
			t.Fatalf("expert sessionA: %d: %s", w.Code, w.Body.String())
		}
		data := dataFromResponse(t, w)
		if len(data) != 3 {
			t.Fatalf("expert sessionA: got %d items want 3", len(data))
		}

		// Expert global queue (no session_id) → questions from BOTH sessions.
		w = intDoReq(t, rExp, "GET", "/api/v1/questions", "")
		if w.Code != http.StatusOK {
			t.Fatalf("expert global: %d: %s", w.Code, w.Body.String())
		}
		data = dataFromResponse(t, w)
		if len(data) != 4 {
			t.Fatalf("expert global: got %d items want 4", len(data))
		}

		// User without session_id → 403.
		rUser := newIntQuestionRouter("user", f.userA.ID, questions, sessions)
		w = intDoReq(t, rUser, "GET", "/api/v1/questions", "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("user no session_id: got %d want 403", w.Code)
		}
	})

	// ----------------------------------------------------------------
	// (c) Status-absent means all statuses (within scope).
	// ----------------------------------------------------------------
	t.Run("(c) Status absent means all statuses", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)
		w := intDoReq(t, rExp, "GET", "/api/v1/questions?session_id="+f.sessionA.ID, "")
		if w.Code != http.StatusOK {
			t.Fatalf("expert no status: %d: %s", w.Code, w.Body.String())
		}
		data := dataFromResponse(t, w)
		if len(data) != 3 {
			t.Fatalf("expert no status: want 3 items (all statuses), got %d", len(data))
		}

		rUser := newIntQuestionRouter("user", f.userA.ID, questions, sessions)
		w = intDoReq(t, rUser, "GET", "/api/v1/questions?session_id="+f.sessionA.ID, "")
		if w.Code != http.StatusOK {
			t.Fatalf("user no status: %d: %s", w.Code, w.Body.String())
		}
		data = dataFromResponse(t, w)
		if len(data) != 3 {
			t.Fatalf("user no status: want 3 items, got %d", len(data))
		}
	})

	// ----------------------------------------------------------------
	// (d) Status filter narrows the scope.
	// ----------------------------------------------------------------
	t.Run("(d) Status filter narrows scope", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)
		w := intDoReq(t, rExp, "GET", "/api/v1/questions?session_id="+f.sessionA.ID+"&status=verified", "")
		if w.Code != http.StatusOK {
			t.Fatalf("expert verified filter: %d: %s", w.Code, w.Body.String())
		}
		data := dataFromResponse(t, w)
		if len(data) != 1 {
			t.Fatalf("verified filter: want 1 item, got %d", len(data))
		}
		m, _ := data[0].(map[string]any)
		if id, _ := m["id"].(string); id != f.qAVerID {
			t.Fatalf("verified filter: got id %s want %s", id, f.qAVerID)
		}

		// Repo-level confirmation of the conditional WHERE.
		items, err := questions.ListForSession(ctx, f.sessionA.ID, domain.QuestionStatusVerified, 20, 0)
		if err != nil {
			t.Fatalf("ListForSession verified: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("repo verified: want 1, got %d", len(items))
		}
		if items[0].ID != f.qAVerID {
			t.Fatalf("repo verified: got %s want %s", items[0].ID, f.qAVerID)
		}
	})

	// ----------------------------------------------------------------
	// (e) PUT full-replace — incomplete payload rejected, row untouched.
	// ----------------------------------------------------------------
	t.Run("(e) PUT incomplete payload rejected row untouched", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)

		// Snapshot pre-state.
		pre, err := questions.FindExpertByID(ctx, f.qAModID)
		if err != nil {
			t.Fatalf("find pre: %v", err)
		}

		w := intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAModID, `{"status":"verified"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("incomplete payload: got %d want 400: %s", w.Code, w.Body.String())
		}

		// Re-read; assert unchanged.
		post, err := questions.FindExpertByID(ctx, f.qAModID)
		if err != nil {
			t.Fatalf("find post: %v", err)
		}
		if pre.Status != post.Status {
			t.Fatalf("status changed: %q → %q", pre.Status, post.Status)
		}
		if !eqSlice(pre.Choices, post.Choices) {
			t.Fatalf("choices changed: %v → %v", pre.Choices, post.Choices)
		}
		if !eqSlice(pre.Answers, post.Answers) {
			t.Fatalf("answers changed: %v → %v", pre.Answers, post.Answers)
		}
	})

	// ----------------------------------------------------------------
	// (f) PUT full-replace — complete payload updates every editable field.
	// ----------------------------------------------------------------
	t.Run("(f) PUT complete payload updates every editable field", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)
		w := intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAModID,
			`{"status":"verified","choices":["X","Y","Z"],"answers":["X","Y"],"explanation":"new expl","tags":["tag1","tag2"],"confidence":0.85}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT complete: got %d want 200: %s", w.Code, w.Body.String())
		}

		ev, err := questions.FindExpertByID(ctx, f.qAModID)
		if err != nil {
			t.Fatalf("find after update: %v", err)
		}
		if !eqSlice(ev.Choices, []string{"X", "Y", "Z"}) {
			t.Fatalf("choices: got %v", ev.Choices)
		}
		if !eqSlice(ev.Answers, []string{"X", "Y"}) {
			t.Fatalf("answers: got %v", ev.Answers)
		}
		if ev.Explanation != "new expl" {
			t.Fatalf("explanation: got %q", ev.Explanation)
		}
		if ev.Confidence != 0.85 {
			t.Fatalf("confidence: got %v", ev.Confidence)
		}
		if !setEq(ev.Tags, []string{"tag1", "tag2"}) {
			t.Fatalf("tags: got %v", ev.Tags)
		}
	})

	// ----------------------------------------------------------------
	// (g) verified_at / verified_by invariant (NOT NULL ⇔ status='verified').
	// ----------------------------------------------------------------
	t.Run("(g) verified_at verified_by invariant", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)

		// Start from qAErr (status=error). Transition: error → verified.
		w := intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAErrID,
			`{"status":"verified","choices":["A","B"],"answers":["A"]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT verified: %d: %s", w.Code, w.Body.String())
		}
		ev, err := questions.FindExpertByID(ctx, f.qAErrID)
		if err != nil {
			t.Fatalf("find after verified: %v", err)
		}
		if ev.VerifiedAt == nil {
			t.Fatal("verified_at must be set after status=verified")
		}
		if ev.VerifiedBy == nil {
			t.Fatal("verified_by must be set after status=verified")
		}
		if *ev.VerifiedBy != f.expert.ID {
			t.Fatalf("verified_by: got %s want %s", *ev.VerifiedBy, f.expert.ID)
		}

		// verified → moderation => both NULL.
		w = intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAErrID,
			`{"status":"moderation","choices":["A","B"],"answers":["A"]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT moderation: %d: %s", w.Code, w.Body.String())
		}
		ev, err = questions.FindExpertByID(ctx, f.qAErrID)
		if err != nil {
			t.Fatalf("find after moderation: %v", err)
		}
		if ev.VerifiedAt != nil {
			t.Fatal("verified_at must be NULL after status=moderation")
		}
		if ev.VerifiedBy != nil {
			t.Fatal("verified_by must be NULL after status=moderation")
		}

		// moderation → error => both NULL.
		w = intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAErrID,
			`{"status":"error","choices":["A","B"],"answers":["A"]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT error: %d: %s", w.Code, w.Body.String())
		}
		ev, err = questions.FindExpertByID(ctx, f.qAErrID)
		if err != nil {
			t.Fatalf("find after error: %v", err)
		}
		if ev.VerifiedAt != nil {
			t.Fatal("verified_at must be NULL after status=error")
		}
		if ev.VerifiedBy != nil {
			t.Fatal("verified_by must be NULL after status=error")
		}
	})

	// ----------------------------------------------------------------
	// (h) Migration 0004 — multiple_correct column dropped; derived works.
	// ----------------------------------------------------------------
	t.Run("(h) Migration 0004 multiple correct column gone", func(t *testing.T) {
		// Column does not exist.
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='questions' AND column_name='multiple_correct')`,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("query information_schema: %v", err)
		}
		if exists {
			t.Fatal("multiple_correct column still exists after migration 0004")
		}

		// Question with multiple answers => MultipleCorrect() == true.
		id, err := questions.Create(ctx, &domain.Question{
			Text: "multi-answer-q", TextHash: "multi-ans-hash-999", TextNorm: "multi-ans",
			Status: domain.QuestionStatusModeration,
			Choices: []string{"A", "B", "C"}, Answers: []string{"A", "C"},
			ChoiceLabeling: "letter",
		})
		if err != nil {
			t.Fatalf("create multi-answer question: %v", err)
		}
		ev, err := questions.FindExpertByID(ctx, id)
		if err != nil {
			t.Fatalf("find multi-answer question: %v", err)
		}
		if !ev.MultipleCorrect() {
			t.Fatal("MultipleCorrect() must be true for 2 answers")
		}
	})

	// ----------------------------------------------------------------
	// (i) answers ⊆ choices enforcement (end-to-end).
	// ----------------------------------------------------------------
	t.Run("(i) answers subset of choices enforcement", func(t *testing.T) {
		rExp := newIntQuestionRouter("expert", f.expert.ID, questions, sessions)

		// Snapshot pre-state.
		pre, err := questions.FindExpertByID(ctx, f.qAVerID)
		if err != nil {
			t.Fatalf("find pre: %v", err)
		}

		// Answer "C" is not in choices ["A","B"] → 400.
		w := intDoReq(t, rExp, "PUT", "/api/v1/questions/"+f.qAVerID,
			`{"status":"verified","choices":["A","B"],"answers":["C"]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("answers not subset: got %d want 400: %s", w.Code, w.Body.String())
		}

		// Re-read; row untouched.
		post, err := questions.FindExpertByID(ctx, f.qAVerID)
		if err != nil {
			t.Fatalf("find post: %v", err)
		}
		if pre.Status != post.Status {
			t.Fatalf("status changed: %q → %q", pre.Status, post.Status)
		}
		if !eqSlice(pre.Choices, post.Choices) {
			t.Fatalf("choices changed: %v → %v", pre.Choices, post.Choices)
		}
		if !eqSlice(pre.Answers, post.Answers) {
			t.Fatalf("answers changed: %v → %v", pre.Answers, post.Answers)
		}
	})
}
