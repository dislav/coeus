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
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// --- minimal fakes ---

type fakeQuestionRepo struct {
	expertByID     func(id string) (*storage.QuestionExpertView, error)
	listModeration func(status, tag string, limit, off int) ([]*storage.QuestionExpertView, error)
	listForUser    func(sessionID, status string, limit, off int) ([]*storage.QuestionWithSession, error)
	forUserByID    func(qid, uid string) (*storage.QuestionWithSession, error)
	updateByExpert func(id string, answers, choices []string, explanation string, conf float64, tags []string, expertID string) error
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

type fakeEmbedder struct {
	embed func(ctx context.Context, text string) ([]float32, error)
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, text)
	}
	return []float32{0.1, 0.2, 0.3}, nil
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
	r.PATCH("/api/v1/questions/:id", h.Update)
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
		updateByExpert: func(string, []string, []string, string, float64, []string, string) error {
			return nil
		},
		expertByID: func(string) (*storage.QuestionExpertView, error) {
			return nil, errors.New("refetch failed")
		},
	}
	r := newQuestionRouter("expert", "e1", q, &fakeQuestionSessionRepo{})
	w := doReq(t, r, "PATCH", "/api/v1/questions/q1", `{"status":"verified","answers":["X"],"choices":["X"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["id"] != "q1" || body["status"] != "verified" {
		t.Fatalf("unexpected partial body: %s", w.Body.String())
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
		"question":"dup","choices":["a","b"],"answers":["a"]
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
		"question":"q","choices":["a","b"],"answers":["a"]
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
		"question":"q","choices":["a","b"],"answers":["a"]
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
		"question":"q","choices":["a","b"],"answers":["a"],"choice_labeling":"emoji"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", w.Code)
	}
}

func TestCreate_ConfidenceOutOfRange400(t *testing.T) {
	bad := 1.5
	body := `{"question":"q","choices":["a","b"],"answers":["a"],"confidence":` + jsonFloat(bad) + `}`
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	w := doReq(t, r, "POST", "/api/v1/questions", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 for confidence 1.5", w.Code)
	}
}

func TestCreate_MissingRequiredFields400(t *testing.T) {
	r := newQuestionRouterWithEmbedder("expert", "e1", &fakeQuestionRepo{}, &fakeQuestionSessionRepo{}, nil)
	// choices has only 1 element (< min=2)
	w := doReq(t, r, "POST", "/api/v1/questions", `{"question":"q","choices":["only"],"answers":["a"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400 (choices min=2)", w.Code)
	}
}

// jsonFloat formats a float for inline JSON in table-ish tests.
func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
