package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// ExpertHandler serves expert-only image access endpoints (spec §4.5).
type ExpertHandler struct {
	images storage.ImageRepo
}

func NewExpertHandler(images storage.ImageRepo) *ExpertHandler {
	return &ExpertHandler{images: images}
}

// GetImage serves the original image bytes with the stored MIME type.
// 404 if the image is missing (ErrNotFound) OR its bytes were already cleaned
// (original IS NULL) per spec §3.5 / §4.7; 500 + slog on other repo errors.
func (h *ExpertHandler) GetImage(c *gin.Context) {
	id := c.Param("id")
	img, err := h.images.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		slog.Error("find image failed",
			"image_id", id,
			"request_id", c.GetString("request_id"),
			"err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	if img.Original == nil {
		c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
		return
	}
	c.Data(http.StatusOK, img.Mime, img.Original)
}

// GetVerificationReport returns the raw verification_report JSON for the image.
// 200 with body "null" when the image exists but has no report; 404 if the
// image itself is missing; 500 + slog on other repo errors.
func (h *ExpertHandler) GetVerificationReport(c *gin.Context) {
	id := c.Param("id")
	img, err := h.images.FindByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse(domain.ErrNotFound))
			return
		}
		slog.Error("find image for report failed",
			"image_id", id,
			"request_id", c.GetString("request_id"),
			"err", err)
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}
	if img.VerificationReport == nil {
		c.Data(http.StatusOK, "application/json", []byte("null"))
		return
	}
	c.Data(http.StatusOK, "application/json", img.VerificationReport)
}
