package handlers

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
)

// errorResponse converts a domain error into the uniform API error shape.
func errorResponse(err error) gin.H {
	var de *domain.Error
	if errors.As(err, &de) {
		return gin.H{"error": gin.H{"code": de.Code, "message": de.Message}}
	}
	return gin.H{"error": gin.H{"code": "internal", "message": "internal server error"}}
}
