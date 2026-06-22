package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// --- minimal fakes ---

type fakeQuestionRepo struct {
	expertByID     func(id string) (*storage.QuestionExpertView, error)
	listModeration func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error)
	listForUser    func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
	forUserByID    func(qid, uid string) (*storage.QuestionWithSession, error)
	updateByExpert func(id string, answers, choices []string, explanation string, conf float64, tags []string, expertID string) error
	updateCalled   bool
	updateArgs     struct {
		id, expertID     string
		answers, choices []string
		explanation      string
		conf             float64
		tags             []string
	}
}

func (f *fakeQuestionRepo) Create(context.Context, *domain.Question) (string, error) { return "", nil }
func (f *fakeQuestionRepo) FindByID(context.Context, string) (*domain.Question, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeQuestionRepo) FindExact(context.Context, string) (*domain.Question, error) { return nil, nil }
func (f *fakeQuestionRepo) FindSemantic(context.Context, []float32, float64) (*domain.Question, error) {
	return nil, nil
}
func (f *fakeQuestionRepo) UpdateFromVerification(context.Context, string, float64, string) error {
	return nil
}
func (f *fakeQuestionRepo) ListForUser(ctx context.Context, sid, st string, l, o int) ([]*storage.QuestionWithSession, error) {
	return f.listForUser(sid, st, l, o)
}
func (f *fakeQuestionRepo) ListForModeration(context.Context, string, string, int, int) ([]*domain.Question, error) {
	return nil, nil
}
func (f *fakeQuestionRepo) ListForModerationExpert(ctx context.Context, st, tag string, l, o int) ([]*storage.QuestionExpertView, error) {
	return f.listModeration(st, tag, l, o)
}
func (f *fakeQuestionRepo) UpdateByExpert(ctx context.Context, id string, ans, ch []string, expl string, c float64, tags []string, eid string) error {
	f.updateCalled = true
	f.updateArgs.id, f.updateArgs.expertID = id, eid
	f.updateArgs.answers, f.updateArgs.choices = ans, ch
	f.updateArgs.explanation, f.updateArgs.conf, f.updateArgs.tags = expl, c, tags
	if f.updateByExpert != nil {
		return f.updateByExpert(id, ans, ch, expl, c, tags, eid)
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

type fakeQuestionSessionRepo struct {
	byID func(id string) (*domain.Session, error)
}

func (f *fakeQuestionSessionRepo) Create(context.Context, string, int, int) (*domain.Session, error) { return nil, nil }
func (f *fakeQuestionSessionRepo) ListByUser(context.Context, string, int, int) ([]*domain.Session, error) {
	return nil, nil
}
func (f *fakeQuestionSessionRepo) Close(context.Context, string) error { return nil }
func (f *fakeQuestionSessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	return f.byID(id)
}

// --- helpers ---

func newQuestionRouter(role, userID string, q storage.QuestionRepo, s storage.SessionRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewQuestionHandler(q, s)
	r.Use(func(c *gin.Context) { c.Set("role", role); c.Set("user_id", userID); c.Next() })
	r.GET("/api/v1/questions", h.List)
	r.GET("/api/v1/questions/:id", h.Get)
	r.PATCH("/api/v1/questions/:id", h.Update)
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

func TestList_UserRoleRequiresSessionID(t *testing.T) {
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing session_id: got %d want 400", w.Code)
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

func TestList_UserRoleNotOwner404(t *testing.T) {
	s := &fakeQuestionSessionRepo{byID: func(string) (*domain.Session, error) {
		return &domain.Session{UserID: "other", Status: "open", ExpiresAt: "2999-01-01T00:00:00Z"}, nil
	}}
	r := newQuestionRouter("user", "u1", &fakeQuestionRepo{}, s)
	w := doReq(t, r, "GET", "/api/v1/questions?session_id=s1", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("not owner: got %d want 404", w.Code)
	}
}

func TestList_ExpertModerationDefaultAndFilter(t *testing.T) {
	q := &fakeQuestionRepo{
		listModeration: func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
			if status != "moderation" {
				t.Errorf("default status: got %q want moderation", status)
			}
			return []*storage.QuestionExpertView{{Question: &domain.Question{ID: "q1"}, ImageID: "img1"}}, nil
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "GET", "/api/v1/questions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0]["image_id"] != "img1" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}

	// tag filter path
	var gotTag string
	q.listModeration = func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error) {
		gotTag = tag
		return nil, nil
	}
	_ = doReq(t, r, "GET", "/api/v1/questions?tag=chemistry", "")
	if gotTag != "chemistry" {
		t.Fatalf("tag filter: got %q want chemistry", gotTag)
	}
}

func TestList_UserRoleForwardsStatusParam(t *testing.T) {
	var gotStatus string
	q := &fakeQuestionRepo{
		listForUser: func(sessionID, status string, l, o int) ([]*storage.QuestionWithSession, error) {
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
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
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
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"moderation"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestUpdate_ExpertNotFound404(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, []string, []string, string, float64, []string, string) error {
			return domain.ErrNotFound
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", w.Code)
	}
}

func TestUpdate_ExpertRepoError500(t *testing.T) {
	q := &fakeQuestionRepo{
		updateByExpert: func(string, []string, []string, string, float64, []string, string) error {
			return errors.New("boom")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d want 500", w.Code)
	}
}
