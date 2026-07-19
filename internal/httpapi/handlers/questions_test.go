package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// --- minimal fakes ---

type fakeQuestionRepo struct {
	expertByID     func(id string) (*storage.QuestionExpertView, error)
	listModeration func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error)
	listForSession func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
	forUserByID    func(qid, uid string) (*storage.QuestionWithSession, error)
	updateByExpert func(id string, upd domain.QuestionUpdate, expertID string) error
	create         func(ctx context.Context, q *domain.Question) (string, error)
	createArg      *domain.Question
	findExact      func(ctx context.Context, hash string) (*domain.Question, error)
	updateCalled   bool
	updateArgs     struct {
		id, expertID     string
		answers, choices []string
		explanation      string
		conf             float64
		tags             []string
		typ              string
	}
}

func (f *fakeQuestionRepo) Create(ctx context.Context, q *domain.Question) (string, error) {
	f.createArg = q
	if f.create != nil {
		return f.create(ctx, q)
	}
	return "q-new", nil
}
func (f *fakeQuestionRepo) FindByID(context.Context, string) (*domain.Question, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeQuestionRepo) FindExact(ctx context.Context, hash string) (*domain.Question, error) {
	if f.findExact != nil {
		return f.findExact(ctx, hash)
	}
	return nil, nil
}
func (f *fakeQuestionRepo) FindSemantic(context.Context, []float32, float64) (*domain.Question, error) {
	return nil, nil
}
func (f *fakeQuestionRepo) UpdateFromVerification(context.Context, string, []string, float64, string) error {
	return nil
}
func (f *fakeQuestionRepo) ListForSession(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
	if f.listForSession != nil {
		return f.listForSession(sid, st, l, o)
	}
	return nil, nil
}
func (f *fakeQuestionRepo) ListForModerationExpert(ctx context.Context, st, tag string, l, o int) ([]*storage.QuestionExpertView, error) {
	return f.listModeration(st, tag, l, o)
}
func (f *fakeQuestionRepo) UpdateByExpert(ctx context.Context, id string, upd domain.QuestionUpdate, expertID string) error {
	f.updateCalled = true
	f.updateArgs.id, f.updateArgs.expertID = id, expertID
	f.updateArgs.answers, f.updateArgs.choices = upd.Answers, upd.Choices
	f.updateArgs.explanation, f.updateArgs.conf, f.updateArgs.tags = upd.Explanation, upd.Confidence, upd.Tags
	f.updateArgs.typ = upd.Type
	if f.updateByExpert != nil {
		return f.updateByExpert(id, upd, expertID)
	}
	return nil
}
func (f *fakeQuestionRepo) CountUnresolvedForImage(context.Context, string) (int, error) { return 0, nil }
func (f *fakeQuestionRepo) LinkToSession(context.Context, string, string, string, int, float64) error {
	return nil
}
func (f *fakeQuestionRepo) FindExpertByID(ctx context.Context, id string) (*storage.QuestionExpertView, error) {
	return f.expertByID(id)
}
func (f *fakeQuestionRepo) FindForUserByID(ctx context.Context, qid, uid string) (*storage.QuestionWithSession, error) {
	return f.forUserByID(qid, uid)
}
func (f *fakeQuestionRepo) Delete(context.Context, string) error { return nil }
func (f *fakeQuestionRepo) UpsertFromImport(context.Context, *domain.Question) (bool, error) {
	return false, nil
}

type fakeQuestionSessionRepo struct {
	byID func(id string) (*domain.Session, error)
}

func (f *fakeQuestionSessionRepo) Create(context.Context, string, int, int) (*domain.Session, error) { return nil, nil }
func (f *fakeQuestionSessionRepo) ListByUser(context.Context, string, int, int) ([]*domain.Session, error) {
	return nil, nil
}
func (f *fakeQuestionSessionRepo) Close(context.Context, string) error { return nil }
func (f *fakeQuestionSessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	if f.byID != nil {
		return f.byID(id)
	}
	return nil, domain.ErrNotFound
}

type fakeEmbedder struct {
	embed func(ctx context.Context, text string) ([]float32, error)
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

func (f *fakeEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

// --- helpers ---

func newQuestionRouter(role, userID string, q storage.QuestionRepo, s storage.SessionRepo) *gin.Engine {
	return newQuestionRouterWithEmbedder(role, userID, q, s, nil)
}

func newQuestionRouterWithEmbedder(role, userID string, q storage.QuestionRepo, s storage.SessionRepo, emb pipeline.AIEmbedder) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewQuestionHandler(q, s, emb)
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	r.GET("/api/v1/questions", h.List)
	r.GET("/api/v1/questions/:id", h.Get)
	r.PUT("/api/v1/questions/:id", h.Update)
	r.POST("/api/v1/questions", h.Create)
	return r
}

func doReq(t *testing.T, r http.Handler, method, target, body string) *httptest.ResponseRecorder {
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

// --- tests ---

func TestList_UserRoleWithoutSessionForbidden403(t *testing.T) {
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("user without session_id: got %d want 403", w.Code)
	}
}

func TestList_UserRoleExpiredSession410(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "u1", Status: "open", ExpiresAt: "2000-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusGone {
		t.Fatalf("expired session: got %d want 410", w.Code)
	}
}

func TestList_UserRoleNotOwnerForbidden403(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "other", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("not owner: got %d want 403", w.Code)
	}
}

func TestList_UserRoleSessionMissing404(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return nil, domain.ErrNotFound
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("session missing: got %d want 404", w.Code)
	}
}

func TestList_ExpertGlobalQueueAllStatusesAndFilter(t *testing.T) {
	var gotStatus, gotTag string
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			gotStatus, gotTag = status, tag
			return []*storage.QuestionExpertView{{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})

	// No status param => no filter (empty status forwarded), all statuses.
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotStatus != "" {
		t.Errorf("default status: got %q want empty (all statuses)", gotStatus)
	}

	// Explicit status filter is forwarded.
	_ = doReq(t, r, "GET", "/api/v1/questions?status=moderation", "")
	if gotStatus != "moderation" {
		t.Errorf("status filter: got %q want moderation", gotStatus)
	}

	// Tag filter is forwarded.
	_ = doReq(t, r, "GET", "/api/v1/questions?tag=chemistry", "")
	if gotTag != "chemistry" {
		t.Errorf("tag filter: got %q want chemistry", gotTag)
	}
}

func TestList_UserRoleForwardsStatusParam(t *testing.T) {
	var gotStatus string
	q := &fakeQuestionRepo{
		listForSession: func(sessionID, status string, l, o int) ([]*storage.QuestionWithSession, error) {
			gotStatus = status
			return nil, nil
		},
	}
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "u1", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", q, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1&status=verified", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotStatus != "verified" {
		t.Fatalf("status forwarded: got %q want %q", gotStatus, "verified")
	}
}

func TestGet_UserNotOwner404(t *testing.T) {
	q := &fakeQuestionRepo{forUserByID: func(string, string) (*storage.QuestionWithSession, error) {
		return nil, domain.ErrNotFound
	}}
	r := newQuestionRouter("user", "u1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

// Admin is a superuser (admin ⊇ expert): it must get expert behavior on the
// role-split List/Get endpoints, not fall into the user branch.

func TestList_AdminGlobalQueue200(t *testing.T) {
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			return []*storage.QuestionExpertView{{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("admin", "a1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin global queue: got %d want 200: %s", w.Code, w.Body.String())
	}
}

func TestList_AdminSessionScopedExemptOwnershipAndExpiry(t *testing.T) {
	q := &fakeQuestionRepo{
		listForSession: func(sessionID, status string, l, o int) ([]*storage.QuestionWithSession, error) {
			return nil, nil
		},
	}
	// Session owned by someone else AND expired: a plain user would 403/410,
	// but admin (like expert) is exempt from both gates.
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "other", Status: "open", ExpiresAt: "2000-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("admin", "a1", q, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin session-scoped: got %d want 200: %s", w.Code, w.Body.String())
	}
}

func TestGet_AdminUsesExpertPath(t *testing.T) {
	q := &fakeQuestionRepo{
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}, nil
		},
		forUserByID: func(string, string) (*storage.QuestionWithSession, error) {
			return nil, domain.ErrNotFound // user path would 404 for admin
		},
	}
	r := newQuestionRouter("admin", "a1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin get: got %d want 200: %s", w.Code, w.Body.String())
	}
}

func TestGet_UserShapeHasDerivedAnswerIDs(t *testing.T) {
	q := &fakeQuestionRepo{forUserByID: func(string, string) (*storage.QuestionWithSession, error) {
		return &storage.QuestionWithSession{
			Question:        &domain.Question{ID: "q1", Text: "q", Choices: []string{"A", "B", "C"}, Answers: []string{"C"}, ChoiceLabeling: "letter", Status: "moderation", Confidence: 0.5},
			ExtractedNumber: 2,
		}, nil
	}}
	r := newQuestionRouter("user", "u1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["number"].(float64) != 2 {
		t.Errorf("number: want 2 (extracted), got %v", body["number"])
	}
	ans := body["answers"].([]any)[0].(map[string]any)
	if ans["id"] != "C" || ans["value"] != "C" {
		t.Errorf("derived answer id wrong: %v", ans)
	}
	if _, hasExpl := body["explanation"]; hasExpl {
		t.Errorf("user shape must not expose explanation")
	}
}

func TestUpdate_ExpertSuccessCallsRepo(t *testing.T) {
	q := &fakeQuestionRepo{
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: "verified"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if !q.updateCalled {
		t.Fatal("UpdateByExpert was not called")
	}
	if q.updateArgs.expertID != "e1" || q.updateArgs.id != "q1" || q.updateArgs.answers[0] != "X" {
		t.Errorf("unexpected args: %+v", q.updateArgs)
	}
	if q.updateArgs.conf != 1.0 {
		t.Errorf("default confidence: got %v want 1.0", q.updateArgs.conf)
	}
}

func TestUpdate_ExpertInvalidStatus400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"moderation"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestUpdate_ExpertNotFound404(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, domain.QuestionUpdate, string) error {
			return domain.ErrNotFound
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestUpdate_ExpertRepoError500(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, domain.QuestionUpdate, string) error {
			return errors.New("boom")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","answers":["X"],"choices":["X","Y"]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500", w.Code)
	}
}

func TestGet_ExpertNotFound404(t *testing.T) {
	q := &fakeQuestionRepo{expertByID: func(string) (*storage.QuestionExpertView, error) {
		return nil, domain.ErrNotFound
	}}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestGet_ExpertRepoError500(t *testing.T) {
	q := &fakeQuestionRepo{expertByID: func(string) (*storage.QuestionExpertView, error) {
		return nil, errors.New("boom")
	}}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions/q1", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500", w.Code)
	}
}

func TestUpdate_ReFetchFallbackReturnsPartialBody(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, domain.QuestionUpdate, string) error {
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return nil, errors.New("refetch failed")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", validUpdateBody())
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["id"] != "q1" || body["status"] != "verified" {
		t.Fatalf("unexpected partial body: %s", w.Body.String())
	}
}

func TestUpdate_RejectsIncompletePayload400(t *testing.T) {
	q := &fakeQuestionRepo{}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	// {"status":"verified"} alone must NOT null out choices/answers.
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("incomplete payload: got %d want 400", w.Code)
	}
}

func TestUpdate_AnswersNotSubsetOfChoices400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["C"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answers not in choices: got %d want 400", w.Code)
	}
	// Case-sensitive: "a" must not match choice "A".
	w = doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["a"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("case-sensitive mismatch: got %d want 400", w.Code)
	}
}

func TestUpdate_ConfidenceOutOfRange400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"confidence":1.5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confidence > 1: got %d want 400", w.Code)
	}

	// confidence < 0 is also rejected.
	w = doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"confidence":-0.5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confidence < 0: got %d want 400", w.Code)
	}
}

func TestUpdate_TooManyTags400(t *testing.T) {
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	// 21 tags => over the 20 limit.
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = "t"
	}
	body := fmt.Sprintf(`{"status":"moderation","type":"multiple_choice","choices":["A","B"],"answers":["A"],"tags":%s}`, asJSON(tags))
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too many tags: got %d want 400", w.Code)
	}
}

func TestUpdate_ConfidenceAbsentDefaultsToOne(t *testing.T) {
	var gotConf float64
	q := &fakeQuestionRepo{
		updateByExpert: func(id string, upd domain.QuestionUpdate, expertID string) error {
			gotConf = upd.Confidence
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if gotConf != 1.0 {
		t.Errorf("default confidence: got %v want 1.0", gotConf)
	}
}

func TestUpdate_ModerationStatusClearsVerification(t *testing.T) {
	var got domain.QuestionUpdate
	q := &fakeQuestionRepo{
		updateByExpert: func(id string, upd domain.QuestionUpdate, expertID string) error {
			got = upd
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusModeration}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1", `{"status":"moderation","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if got.Status != domain.QuestionStatusModeration {
		t.Errorf("status forwarded: got %q want moderation", got.Status)
	}
}

func TestList_ExpertPagingBoundsResetToDefault(t *testing.T) {
	var gotLimit int
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			gotLimit = limit
			return nil, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})

	_ = doReq(t, r, "GET", "/api/v1/questions?per_page=999", "")
	if gotLimit != 20 {
		t.Fatalf("per_page=999: got limit %d want 20", gotLimit)
	}

	_ = doReq(t, r, "GET", "/api/v1/questions?per_page=0", "")
	if gotLimit != 20 {
		t.Fatalf("per_page=0: got limit %d want 20", gotLimit)
	}
}

func TestList_InvalidStatus400BothRoles(t *testing.T) {
	// The user role resolves the session (h.sessions.FindByID) before building
	// a response, so the fake MUST supply a valid OWNED session — UserID equal
	// to the authenticated "x", status open, far-future expiry. Otherwise byID
	// is nil and the fake dereferences a nil func, panicking before the request
	// can reach the shared status validation. The expert role is exempt from the
	// ownership/expiry gates but uses the same session repo, so this session is
	// harmless for it.
	owned := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{
			UserID:    "x",
			Status:    domain.SessionStatusOpen,
			ExpiresAt: "2999-01-01T00:00:00Z",
		}, nil
	}}

	// Invalid status must be 400 for BOTH roles on the session-scoped path.
	for _, role := range []string{"user", "expert"} {
		r := newQuestionRouter(role, "x", &fakeQuestionRepo{}, owned)
		w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1&status=bogus", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: invalid status (session-scoped) got %d, want 400", role, w.Code)
		}
	}

	// Expert on the global-queue path (no session_id) hits the shared status
	// validation directly; no session repo is consulted.
	r := newQuestionRouter("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions?status=bogus", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expert invalid status (global queue): got %d want 400", w.Code)
	}
}

func TestList_ExpertSessionScopedUsesListForSession(t *testing.T) {
	called := false
	q := &fakeQuestionRepo{
		listForSession: func(sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
			called = true
			if sid != "s1" {
				t.Errorf("session id: got %q want s1", sid)
			}
			return []*storage.QuestionWithSession{{Question: &domain.Question{ID: "q1", Status: "verified"}, ImageID: "img1"}}, nil
		},
	}
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "e1", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("expert", "e1", q, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("expert session-scoped request did not call ListForSession")
	}
}

func TestCreate_ExpertSuccess201Verified(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{
				Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified},
			}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"What is 2+2?",
		"type":"multiple_choice",
		"choices":["3","4","5"],
		"answers":["4"],
		"tags":["math"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if q.createArg == nil {
		t.Fatal("Create was not called")
	}
	if q.createArg.Status != domain.QuestionStatusVerified {
		t.Errorf("status: got %q want verified", q.createArg.Status)
	}
	if q.createArg.Number != 0 {
		t.Errorf("number: got %d want 0", q.createArg.Number)
	}
	if q.createArg.ChoiceLabeling != domain.ChoiceLabelingLetter {
		t.Errorf("default choice_labeling: got %q want letter", q.createArg.ChoiceLabeling)
	}
	if q.createArg.Confidence != 0.99 {
		t.Errorf("default confidence: got %v want 0.99", q.createArg.Confidence)
	}
	if q.createArg.VerifiedBy == nil || *q.createArg.VerifiedBy != "e1" {
		t.Errorf("verified_by: got %v want e1", q.createArg.VerifiedBy)
	}
	if q.createArg.VerifiedAt == nil {
		t.Error("verified_at must be set")
	}
	if q.createArg.TextHash == "" {
		t.Error("TextHash must be populated")
	}
	if q.createArg.TextNorm != domain.NormalizeQuestion("What is 2+2?") {
		t.Errorf("TextNorm: got %q want %q", q.createArg.TextNorm, domain.NormalizeQuestion("What is 2+2?"))
	}
	if q.createArg.Embedding != nil {
		t.Errorf("Embedding must be nil when embedder is unconfigured (nil); got %v", q.createArg.Embedding)
	}
	// tags: req tags + manual-entry, and NO ai-generated
	gotManual, gotAI := false, false
	for _, tg := range q.createArg.Tags {
		if tg == "manual-entry" {
			gotManual = true
		}
		if tg == "ai-generated" {
			gotAI = true
		}
	}
	if !gotManual {
		t.Errorf("manual-entry tag missing: %v", q.createArg.Tags)
	}
	if gotAI {
		t.Errorf("ai-generated must NOT be injected on manual path: %v", q.createArg.Tags)
	}
}

func TestCreate_DuplicateHashReturns409WithQuestionID(t *testing.T) {
	q := &fakeQuestionRepo{
		findExact: func(context.Context, string) (*domain.Question, error) {
			return &domain.Question{ID: "existing-id"}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"dup","type":"multiple_choice","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d want 409: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Code       string `json:"code"`
			QuestionID string `json:"question_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("malformed 409 body: %s", w.Body.String())
	}
	if body.Error.Code != "duplicate" {
		t.Errorf("code: got %q want duplicate", body.Error.Code)
	}
	if body.Error.QuestionID != "existing-id" {
		t.Errorf("question_id: got %q want existing-id", body.Error.QuestionID)
	}
	if q.createArg != nil {
		t.Error("Create must not be called on duplicate")
	}
}

func TestCreate_EmbedderFailureStillCreates201(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	emb := &fakeEmbedder{embed: func(context.Context, string) ([]float32, error) {
		return nil, errors.New("embedder down")
	}}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, emb)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201 (embed failure must not fail request): %s", w.Code, w.Body.String())
	}
	if q.createArg == nil || q.createArg.Embedding != nil {
		t.Error("question created without embedding on embedder failure")
	}
}

func TestCreate_EmbedderConfiguredAttachesEmbedding(t *testing.T) {
	q := &fakeQuestionRepo{
		create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, &fakeEmbedder{})
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if q.createArg == nil || len(q.createArg.Embedding) == 0 {
		t.Error("embedding must be attached when embedder succeeds")
	}
}

func TestCreate_InvalidChoiceLabeling400(t *testing.T) {
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", `{
		"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"],"choice_labeling":"emoji"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestCreate_ConfidenceOutOfRange400(t *testing.T) {
	bad := 1.5
	body := `{"question":"q","type":"multiple_choice","choices":["a","b"],"answers":["a"],"confidence":` + jsonFloat(bad) + `}`
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 for confidence 1.5", w.Code)
	}
}

func TestCreate_SingleChoiceMCMustBeRejectedByHandler400(t *testing.T) {
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	// Single-choice MC: the handler's type-conditional check rejects len(choices) < 2.
	w := doReq(t, r, "POST", "/api/v1/questions", `{"question":"q","type":"multiple_choice","choices":["only"],"answers":["a"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 (len(choices)<2)", w.Code)
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonFloat formats a float for inline JSON in table-ish tests.
func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func validUpdateBody() string {
	return `{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"],"explanation":"e","tags":["t"],"confidence":0.9}`
}

func TestUpdate_TypeConditionalValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			"mc valid",
			`{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`,
			http.StatusOK,
		},
		{
			"mc one choice",
			`{"status":"verified","type":"multiple_choice","choices":["A"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"mc answer not in choices",
			`{"status":"verified","type":"multiple_choice","choices":["A","B"],"answers":["C"]}`,
			http.StatusBadRequest,
		},
		{
			"fr valid",
			`{"status":"verified","type":"free_response","choices":[],"answers":["42"]}`,
			http.StatusOK,
		},
		{
			"fr with choices",
			`{"status":"verified","type":"free_response","choices":["A"],"answers":["42"]}`,
			http.StatusBadRequest,
		},
		{
			"missing type",
			`{"status":"verified","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"invalid type",
			`{"status":"verified","type":"matching","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuestionRepo{
				updateByExpert: func(string, domain.QuestionUpdate, string) error { return nil },
				expertByID: func(string) (*storage.QuestionExpertView, error) {
					return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
				},
			}
			r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
			w := doReq(t, r, "PUT", "/api/v1/questions/q1", tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("got %d want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestUpdate_ForwardsTypeToRepo(t *testing.T) {
	var got domain.QuestionUpdate
	q := &fakeQuestionRepo{
		updateByExpert: func(_ string, upd domain.QuestionUpdate, _ string) error {
			got = upd
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q1", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PUT", "/api/v1/questions/q1",
		`{"status":"verified","type":"free_response","choices":[],"answers":["42"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if got.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("forwarded Type = %q, want free_response", got.Type)
	}
}

func TestCreate_TypeConditionalValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			"mc valid",
			`{"question":"q","type":"multiple_choice","choices":["A","B"],"answers":["A"]}`,
			http.StatusCreated,
		},
		{
			"mc one choice",
			`{"question":"q","type":"multiple_choice","choices":["A"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
		{
			"mc answer not in choices",
			`{"question":"q","type":"multiple_choice","choices":["A","B"],"answers":["C"]}`,
			http.StatusBadRequest,
		},
		{
			"fr valid",
			`{"question":"q","type":"free_response","choices":[],"answers":["42"]}`,
			http.StatusCreated,
		},
		{
			"fr with choices",
			`{"question":"q","type":"free_response","choices":["A"],"answers":["42"]}`,
			http.StatusBadRequest,
		},
		{
			"fr answers empty (binding)",
			`{"question":"q","type":"free_response","choices":[],"answers":[]}`,
			http.StatusBadRequest,
		},
		{
			"missing type",
			`{"question":"q","choices":["A","B"],"answers":["A"]}`,
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQuestionRepo{
				create: func(context.Context, *domain.Question) (string, error) { return "q-new", nil },
				expertByID: func(string) (*storage.QuestionExpertView, error) {
					return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
				},
			}
			r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
			w := doReq(t, r, "POST", "/api/v1/questions", tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("%s: got %d want %d: %s", tc.name, w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCreate_FreeResponseNormalizesNilChoices(t *testing.T) {
	// Omitting "choices" entirely binds nil in Go; Create must normalize to []string{}
	// so the DB stores '[]' not NULL (spec §3.5.4).
	var captured *domain.Question
	q := &fakeQuestionRepo{
		create: func(_ context.Context, qq *domain.Question) (string, error) {
			captured = qq
			return "q-new", nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return &storage.QuestionExpertView{Question: &domain.Question{ID: "q-new", Status: domain.QuestionStatusVerified}}, nil
		},
	}
	r := newQuestionRouterWithEmbedder("expert", "e1", q, &fakeQuestionSessionRepo{}, nil)
	// "choices" key omitted entirely.
	w := doReq(t, r, "POST", "/api/v1/questions", `{"question":"q","type":"free_response","answers":["42"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d want 201: %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("Create not called")
	}
	if captured.Choices == nil {
		t.Error("Choices is nil; expected normalized []string{}")
	}
	if captured.Type != domain.QuestionTypeFreeResponse {
		t.Errorf("Type = %q, want free_response", captured.Type)
	}
}
