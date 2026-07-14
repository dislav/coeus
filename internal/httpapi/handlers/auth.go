package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type AuthHandler struct {
	users  storage.UserRepo
	jwtMgr *auth.JWTManager
}

func NewAuthHandler(users storage.UserRepo, jwtMgr *auth.JWTManager) *AuthHandler {
	return &AuthHandler{users: users, jwtMgr: jwtMgr}
}

type registerRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type authResponse struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	user, err := h.users.Create(c.Request.Context(), req.Email, hash, "user")
	if err != nil {
		if errors.Is(err, domain.ErrDuplicate) {
			c.JSON(http.StatusConflict, errorResponse(domain.ErrDuplicate))
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusCreated, userResponse{ID: user.ID, Email: user.Email, Role: user.Role})
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	user, err := h.users.FindByEmail(c.Request.Context(), req.Email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusUnauthorized, errorResponse(domain.ErrUnauthorized))
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, errorResponse(domain.ErrUnauthorized))
		return
	}

	token, err := h.jwtMgr.Issue(user.ID, user.Role, user.Active, user.TokenVersion)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusOK, authResponse{Token: token, Role: user.Role})
}

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

// Profile — GET /api/v1/profile. Reads the *storage.User stashed by AuthMiddleware.
func (h *AuthHandler) Profile(c *gin.Context) {
	v, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	user := v.(*storage.User)
	c.JSON(http.StatusOK, userToResponse(user))
}
