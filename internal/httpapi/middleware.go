package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

func AuthMiddleware(jwtMgr *auth.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := jwtMgr.Verify(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, apiError(domain.ErrUnauthorized))
			return
		}
		c.Set("claims", claims)
		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)
		c.Next()
	}
}

func RoleGuard(requiredRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role.(string) != requiredRole {
			c.AbortWithStatusJSON(http.StatusForbidden, apiError(domain.ErrForbidden))
			return
		}
		c.Next()
	}
}

func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-Id")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-Id", requestID)

		start := time.Now()
		c.Next()

		slog.Info("request",
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", "request_id", c.GetString("request_id"), "error", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{"code": "internal", "message": "internal server error"},
				})
			}
		}()
		c.Next()
	}
}

// apiError converts a domain error into the uniform API error shape.
// This is the middleware's private copy; handlers have their own in common.go.
func apiError(err error) gin.H {
	var de *domain.Error
	if errors.As(err, &de) {
		return gin.H{"error": gin.H{"code": de.Code, "message": de.Message}}
	}
	return gin.H{"error": gin.H{"code": "internal", "message": "internal server error"}}
}

// SessionWindow guards upload/list paths behind session ownership + open status + expiry.
// It expects AuthMiddleware to have set "user_id" in the gin context.
// Not-found and wrong-owner both return 404 to avoid leaking session existence.
func SessionWindow(sessions storage.SessionRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		sessionID := c.Param("id")

		sess, err := sessions.FindByID(c.Request.Context(), sessionID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, apiError(domain.ErrNotFound))
			return
		}

		if sess.UserID != userID {
			c.AbortWithStatusJSON(http.StatusNotFound, apiError(domain.ErrNotFound))
			return
		}

		if sess.Status != domain.SessionStatusOpen {
			c.AbortWithStatusJSON(http.StatusGone, apiError(domain.ErrSessionExpired))
			return
		}

		expiresAt, err := time.Parse(time.RFC3339, sess.ExpiresAt)
		if err != nil || time.Now().After(expiresAt) {
			c.AbortWithStatusJSON(http.StatusGone, apiError(domain.ErrSessionExpired))
			return
		}

		c.Set("session", sess)
		c.Next()
	}
}
