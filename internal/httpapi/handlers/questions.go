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
	defaultPerPage = 20
	maxPerPage     = 100
)

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

// List — GET /api/v1/questions. Behavior splits by role.
func (h *QuestionHandler) List(c *gin.Context) {
	role := c.GetString("role")
	page, perPage, offset := parsePaging(c)

	if role == roleExpert {
		status := c.DefaultQuery("status", domain.QuestionStatusModeration)
		if status != domain.QuestionStatusModeration && status != domain.QuestionStatusError {
			c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
			return
		}
		tag := c.Query("tag")
		items, err := h.questions.ListForModerationExpert(c.Request.Context(), status, tag, perPage, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(err))
			return
		}
		data := make([]any, 0, len(items))
		for _, q := range items {
			data = append(data, toExpertResponse(q))
		}
		c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
		return
	}

	// user role
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	userID := c.GetString("user_id")

	// Inline SessionWindow-equivalent check (session_id is a query param here,
	// so the path-param SessionWindow middleware cannot be reused — plan Decision #1).
	sess, err := h.sessions.FindByID(c.Request.Context(), sessionID)
	if err != nil || sess.UserID != userID {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	if sess.Status != domain.SessionStatusOpen {
		c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil || time.Now().After(expiresAt) {
		c.JSON(http.StatusGone, errorResponse(domain.ErrSessionExpired))
		return
	}

	items, err := h.questions.ListForUser(c.Request.Context(), sessionID, c.Query("status"), perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	data := make([]any, 0, len(items))
	for _, q := range items {
		data = append(data, toUserResponse(q))
	}
	c.JSON(http.StatusOK, dto.QuestionListResponse{Data: data, Page: page, PerPage: perPage})
}

// Get — GET /api/v1/questions/:id. Behavior splits by role.
func (h *QuestionHandler) Get(c *gin.Context) {
	id := c.Param("id")
	role := c.GetString("role")

	if role == roleExpert {
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

// Update — PATCH /api/v1/questions/:id. Expert only (RoleGuard enforces 403 at the route).
func (h *QuestionHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Status      string   `json:"status" binding:"required"`
		Answers     []string `json:"answers"`
		Choices     []string `json:"choices"`
		Explanation string   `json:"explanation"`
		Tags        []string `json:"tags"`
		Confidence  *float64 `json:"confidence"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	if req.Status != domain.QuestionStatusVerified {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	confidence := 1.0 // expert confirms -> full confidence when omitted
	if req.Confidence != nil {
		confidence = *req.Confidence
	}

	expertID := c.GetString("user_id")
	if err := h.questions.UpdateByExpert(c.Request.Context(), id, req.Answers, req.Choices, req.Explanation, confidence, req.Tags, expertID); err != nil {
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

	// Re-fetch the updated expert view for the response (Decision #4).
	ev, err := h.questions.FindExpertByID(c.Request.Context(), id)
	if err != nil {
		slog.Warn("re-fetch after expert update failed, returning partial body",
			"question_id", id,
			"err", err)
		c.JSON(http.StatusOK, gin.H{"id": id, "status": domain.QuestionStatusVerified})
		return
	}
	c.JSON(http.StatusOK, toExpertResponse(ev))
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

	// Exact-hash dedup. On hit, return the existing id inline so the expert can PATCH it.
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

	q := &domain.Question{
		Number:          0,
		Text:            req.Question,
		TextNorm:        norm,
		TextHash:        hash,
		Choices:         req.Choices,
		Answers:         req.Answers,
		ChoiceLabeling:  choiceLabeling,
		Confidence:      confidence,
		Explanation:     req.Explanation,
		Embedding:       embedding,
		Status:          domain.QuestionStatusVerified,
		VerifiedAt:      &now,
		VerifiedBy:      &expertID,
		Tags:            tags,
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
