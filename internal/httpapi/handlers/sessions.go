package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type SessionHandler struct {
	sessions storage.SessionRepo
	images   storage.ImageRepo
}

func NewSessionHandler(sessions storage.SessionRepo, images storage.ImageRepo) *SessionHandler {
	return &SessionHandler{sessions: sessions, images: images}
}

func (h *SessionHandler) Create(c *gin.Context) {
	var req dto.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	userID := c.GetString("user_id")
	sess, err := h.sessions.Create(c.Request.Context(), userID, req.DurationSeconds, req.BufferSeconds)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusCreated, dto.SessionResponse{
		ID:        sess.ID,
		ExpiresAt: sess.ExpiresAt,
		Status:    sess.Status,
	})
}

func (h *SessionHandler) List(c *gin.Context) {
	userID := c.GetString("user_id")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	offset := (page - 1) * perPage

	sessions, err := h.sessions.ListByUser(c.Request.Context(), userID, perPage, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	data := make([]dto.SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		data = append(data, dto.SessionResponse{
			ID:        s.ID,
			ExpiresAt: s.ExpiresAt,
			Status:    s.Status,
		})
	}

	c.JSON(http.StatusOK, dto.SessionListResponse{Data: data, Page: page, PerPage: perPage})
}

func (h *SessionHandler) Get(c *gin.Context) {
	userID := c.GetString("user_id")
	id := c.Param("id")

	sess, err := h.sessions.FindByID(c.Request.Context(), id)
	if err != nil || sess.UserID != userID {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}

	imageCount, err := h.images.CountBySession(c.Request.Context(), id)
	if err != nil {
		slog.Warn("count images failed, showing 0", "session", id, "error", err)
	}

	c.JSON(http.StatusOK, dto.SessionDetailResponse{
		SessionResponse: dto.SessionResponse{
			ID:        sess.ID,
			ExpiresAt: sess.ExpiresAt,
			Status:    sess.Status,
		},
		DurationSeconds: sess.DurationSeconds,
		BufferSeconds:   sess.BufferSeconds,
		StartedAt:       sess.StartedAt,
		ImageCount:      imageCount,
	})
}

func (h *SessionHandler) Close(c *gin.Context) {
	userID := c.GetString("user_id")
	id := c.Param("id")

	sess, err := h.sessions.FindByID(c.Request.Context(), id)
	if err != nil || sess.UserID != userID {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}

	if err := h.sessions.Close(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.Status(http.StatusNoContent)
}
