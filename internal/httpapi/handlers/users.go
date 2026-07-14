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

func userToResponse(u *storage.User) dto.UserResponse {
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
		data = append(data, userToResponse(u))
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
	c.JSON(http.StatusOK, userToResponse(updated))
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
