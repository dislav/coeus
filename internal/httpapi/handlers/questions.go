package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

const (
	roleExpert     = "expert"
	roleAdmin      = "admin"
	defaultPerPage = 20
	maxPerPage     = 100
)

// isExpert reports whether the role carries expert (moderation) privileges.
// admin is a superuser: it has every expert power plus user management, so it
// must take the expert branch everywhere the handlers split on role.
func isExpert(role string) bool {
	return role == roleExpert || role == roleAdmin
}

// QuestionHandler serves the role-split /api/v1/questions endpoints (spec §4.4).
type QuestionHandler struct {
	questions storage.QuestionRepo
	sessions  storage.SessionRepo
	embedder  pipeline.AIEmbedder
}

func NewQuestionHandler(questions storage.QuestionRepo, sessions storage.SessionRepo, embedder pipeline.AIEmbedder) *QuestionHandler {
	return &QuestionHandler{questions: questions, sessions: sessions, embedder: embedder}
}

func parsePaging(c *gin.Context) (page, perPage, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ = strconv.Atoi(c.DefaultQuery("per_page", strconv.Itoa(defaultPerPage)))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > maxPerPage {
		perPage = defaultPerPage
	}
	return page, perPage, (page - 1) * perPage
}

// List — GET /api/v1/questions. session_id drives scoping; role drives only
// authorization and response shape (spec §3.1).
func (h *QuestionHandler) List(c *gin.Context) {
	role := c.GetString("role")
	page, perPage, offset := parsePaging(c)

	// Shared status validation runs BEFORE the role split (spec §3.1.2).
	status := c.Query("status")
	if status != "" &&
		status != domain.QuestionStatusModeration &&
		status != domain.QuestionStatusVerified &&
		status != domain.QuestionStatusError {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	sessionID := c.Query("session_id")
	userID := c.GetString("user_id")

	if sessionID != "" {
		// Session-scoped read path, shared by both roles.
		sess, err := h.sessions.FindByID(c.Request.Context(), sessionID)
		if err != nil {
			// Session genuinely missing => 404 (spec §3.1.4).
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		// Ownership: user must own the session (403 on mismatch); expert exempt.
		if !isExpert(role) && sess.UserID != userID {
			c.JSON(http.StatusForbidden, errorResponse(domain.ErrForbidden))
			return
		}
		// Expiry gate applies to the user role only (experts may inspect any session).
		if !isExpert(role) {
			if sess.Status != domain.SessionStatusOpen {
				c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
				return
			}
			expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
			if err != nil || time.Now().After(expiresAt) {
				c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
				return
			}
		}

		items, err := h.questions.ListForSession(c.Request.Context(), sessionID, status, perPage, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(err))
			return
		}
		data := make([]any, 0, len(items))
		for _, q := range items {
			if isExpert(role) {
				data = append(data, toExpertResponseFromSession(q))
			} else {
				data = append(data, toUserResponse(q))
			}
		}
		c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
		return
	}

	// No session_id: experts get the global queue; users are forbidden (403).
	if !isExpert(role) {
		c.JSON(http.StatusForbidden, errorResponse(domain.ErrForbidden))
		return
	}
	search := c.Query("search")
	items, err := h.questions.ListForModerationExpert(c.Request.Context(), status, search, perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	data := make([]any, 0, len(items))
	for _, q := range items {
		data = append(data, toExpertResponse(q))
	}
	c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
}

// Get — GET /api/v1/questions/:id. Behavior splits by role.
func (h *QuestionHandler) Get(c *gin.Context) {
	id := c.Param("id")
	role := c.GetString("role")

	if isExpert(role) {
		ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
				return
			}
			slog.Error("find expert question failed",
				"question_id", id,
				"request_id", c.GetString("request_id"),
				"err", err)
			c.JSON(http.StatusInternalServerError, errorResponse(err))
			return
		}
		c.JSON(http.StatusOK, toExpertResponse(ev))
		return
	}

	// user: ownership checked at repo level (404 if not linked to caller's session)
	userID := c.GetString("user_id")
	qws, err := h.questions.FindForUserByID(c.Request.Context(), id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	c.JSON(http.StatusOK, toUserResponse(qws))
}

// Update — PUT /api/v1/questions/:id. Expert only (RoleGuard enforces 403 at
// the route). Full-replace of editable fields with backend validation (spec §3.2).
func (h *QuestionHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req dto.UpdateQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Structural rules are type-conditional (spec §3.5.4), shared with Create
	// and the importer via domain.ValidateDraft. Binding guarantees:
	//   - req.Type is one of {multiple_choice, free_response}
	//   - every present choice is non-empty
	//   - len(req.Answers) >= 1
	// Update's DTO carries no question text, so the placeholder keeps
	// ValidateDraft's non-empty-text check trivially satisfied; only the
	// type-conditional checks are load-bearing here.
	if err := domain.ValidateDraft(" ", req.Choices, req.Answers, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Field rules: confidence range; tags count + non-empty (spec §3.2.3).
	confidence := 1.0 // matches today's "expert confirms => full confidence" default
	if req.Confidence != nil {
		if *req.Confidence < 0 || *req.Confidence > 1 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		confidence = *req.Confidence
	}
	if len(req.Tags) > 20 {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	for _, tg := range req.Tags {
		if tg == "" {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
	}

	expertID := c.GetString("user_id")
	upd := domain.QuestionUpdate{
		Status:      req.Status,
		Type:        req.Type,
		Choices:     req.Choices,
		Answers:     req.Answers,
		Explanation: req.Explanation,
		Tags:        req.Tags,
		Confidence:  confidence,
	}
	if err := h.questions.UpdateByExpert(c.Request.Context(), id, upd, expertID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		slog.Error("update question by expert failed",
			"question_id", id,
			"expert_id", expertID,
			"request_id", c.GetString("request_id"),
			"err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	// Re-fetch the updated expert view for the response.
	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		slog.Warn("re-fetch after expert update failed, returning partial body",
			"question_id", id,
			"err", err)
		c.JSON(http.StatusOK, gin.H{"id": id, "status": req.Status})
		return
	}
	c.JSON(http.StatusOK, toExpertResponse(ev))
}

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

// Create — POST /api/v1/questions. Expert only (RoleGuard enforces 403 at the route).
// Hand-authors a canonical verified question, bypassing the image pipeline (spec §3.2).
func (h *QuestionHandler) Create(c *gin.Context) {
	var req dto.CreateQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	if req.ChoiceLabeling != "" &&
		req.ChoiceLabeling != domain.ChoiceLabelingLetter &&
		req.ChoiceLabeling != domain.ChoiceLabelingNumber {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Type-conditional structural validation (spec §3.5.4), shared with Update
	// and the importer via domain.ValidateDraft.
	if err := domain.ValidateDraft(req.Question, req.Choices, req.Answers, req.Type); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	confidence := 0.99
	if req.Confidence != nil {
		if *req.Confidence < 0 || *req.Confidence > 1 {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		confidence = *req.Confidence
	}
	choiceLabeling := req.ChoiceLabeling
	if choiceLabeling == "" {
		choiceLabeling = domain.ChoiceLabelingLetter
	}

	norm := domain.NormalizeQuestion(req.Question)
	hash := domain.HashQuestion(norm)

	// Exact-hash dedup. On hit, return the existing id inline so the expert can PUT it.
	existing, err := h.questions.FindExact(c.Request.Context(), hash)
	if err != nil {
		slog.Error("manual question exact dedup failed",
			"request_id", c.GetString("request_id"), "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	if existing != nil {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":        "duplicate",
			"message":     "question already exists",
			"question_id": existing.ID,
		}})
		return
	}

	// Best-effort embedding — never fails the request. Skipped entirely when unconfigured.
	var embedding []float32
	if h.embedder != nil {
		emb, embErr := h.embedder.Embed(c.Request.Context(), req.Question)
		if embErr != nil {
			slog.Error("manual question embedder failed",
				"request_id", c.GetString("request_id"), "err", embErr)
		} else {
			embedding = emb
		}
	}

	expertID := c.GetString("user_id")
	now := time.Now().UTC().Format(time.RFC3339)
	// tags = req.Tags + ["manual-entry"]; copy to avoid aliasing req.Tags.
	tags := make([]string, 0, len(req.Tags)+1)
	tags = append(tags, req.Tags...)
	tags = append(tags, "manual-entry")

	choices := req.Choices
	if choices == nil {
		choices = []string{}
	}

	q := &domain.Question{
		Number:         0,
		Text:           req.Question,
		TextNorm:       norm,
		TextHash:       hash,
		Choices:        choices,
		Answers:        req.Answers,
		ChoiceLabeling: choiceLabeling,
		Type:           req.Type,
		Confidence:     confidence,
		Explanation:    req.Explanation,
		Embedding:      embedding,
		Status:         domain.QuestionStatusVerified,
		VerifiedAt:     &now,
		VerifiedBy:     &expertID,
		Tags:           tags,
	}
	id, err := h.questions.Create(c.Request.Context(), q)
	if err != nil {
		slog.Error("manual question create failed",
			"expert_id", expertID, "request_id", c.GetString("request_id"), "err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		slog.Warn("manual question re-fetch failed", "question_id", id, "err", err)
		c.JSON(http.StatusCreated, gin.H{"id": id, "status": domain.QuestionStatusVerified})
		return
	}
	c.JSON(http.StatusCreated, toExpertResponse(ev))
}

func toUserResponse(q *storage.QuestionWithSession) dto.UserQuestionResponse {
	qq := q.Question
	return dto.UserQuestionResponse{
		ID:              qq.ID,
		Number:          q.ExtractedNumber,
		Question:        qq.Text,
		Type:            qq.Type,
		MultipleCorrect: qq.MultipleCorrect(),
		Choices:         qq.Choices,
		Answers:         dto.DeriveAnswerRefs(qq.Choices, qq.Answers, qq.ChoiceLabeling),
		Status:          qq.Status,
		Confidence:      qq.Confidence,
	}
}

func toExpertResponse(ev *storage.QuestionExpertView) dto.ExpertQuestionResponse {
	q := ev.Question
	resp := dto.ExpertQuestionResponse{
		ID:                    q.ID,
		Number:                q.Number,
		Question:              q.Text,
		MultipleCorrect:       q.MultipleCorrect(),
		Choices:               q.Choices,
		Answers:               q.Answers,
		ChoiceLabeling:        q.ChoiceLabeling,
		Type:                  q.Type,
		Confidence:            q.Confidence,
		Explanation:           q.Explanation,
		Tags:                  q.Tags,
		Status:                q.Status,
		ImageID:               ev.ImageID,
		HasVerificationReport: ev.HasVerificationReport,
		VerifiedAt:            q.VerifiedAt,
		VerifiedBy:            q.VerifiedBy,
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}

// toExpertResponseFromSession builds the expert DTO from the session-scoped read
// (QuestionWithSession). HasVerificationReport is not available on this path
// (it is a moderation-queue convenience) and defaults to false.
func toExpertResponseFromSession(qws *storage.QuestionWithSession) dto.ExpertQuestionResponse {
	q := qws.Question
	resp := dto.ExpertQuestionResponse{
		ID:              q.ID,
		Number:          qws.ExtractedNumber,
		Question:        q.Text,
		MultipleCorrect: q.MultipleCorrect(),
		Choices:         q.Choices,
		Answers:         q.Answers,
		ChoiceLabeling:  q.ChoiceLabeling,
		Type:            q.Type,
		Confidence:      q.Confidence,
		Explanation:     q.Explanation,
		Tags:            q.Tags,
		Status:          q.Status,
		ImageID:         qws.ImageID,
		VerifiedAt:      q.VerifiedAt,
		VerifiedBy:      q.VerifiedBy,
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}
