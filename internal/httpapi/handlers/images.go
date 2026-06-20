package handlers

import (
	"bytes"
	"image"
	"io"
	"log/slog"
	"net/http"
	"strings"

	_ "image/jpeg"              // register jpeg for DecodeConfig
	_ "image/png"               // register png for DecodeConfig
	_ "golang.org/x/image/webp" // register webp for DecodeConfig

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

// ImageHandler handles image uploads and listing for a session.
type ImageHandler struct {
	images    storage.ImageRepo
	jobs      storage.JobQueue
	uploadCfg config.UploadConfig
}

// NewImageHandler creates a new ImageHandler.
func NewImageHandler(images storage.ImageRepo, jobs storage.JobQueue, uploadCfg config.UploadConfig) *ImageHandler {
	return &ImageHandler{images: images, jobs: jobs, uploadCfg: uploadCfg}
}

// Upload accepts a multipart image, validates its MIME type, stores it, and
// enqueues a processing job. Responds with 202 Accepted.
func (h *ImageHandler) Upload(c *gin.Context) {
	sess, exists := c.Get("session")
	if !exists {
		c.JSON(http.StatusInternalServerError, errorResponse(domain.NewError("internal", "session missing")))
		return
	}
	session := sess.(*domain.Session)

	// Enforce size cap
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.uploadCfg.MaxBytes)

	file, _, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Sniff actual content type from first 512 bytes
	mime := http.DetectContentType(data)
	if !h.uploadCfg.AllowedMimesMap()[strings.ToLower(mime)] {
		c.JSON(http.StatusBadRequest, errorResponse(domain.ErrValidation))
		return
	}

	// Decode dimensions (best-effort)
	width, height := 0, 0
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		width, height = cfg.Width, cfg.Height
	}

	ctx := c.Request.Context()

	imgID, err := h.images.Create(ctx, session.ID, data, mime, width, height)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	// Enqueue fires NOTIFY jobs_new internally — workers wake without polling.
	jobID, err := h.jobs.Enqueue(ctx, imgID, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	c.JSON(http.StatusAccepted, dto.ImageUploadResponse{ImageID: imgID, JobID: jobID})
}

// List returns all images for the current session along with their job status.
func (h *ImageHandler) List(c *gin.Context) {
	sess, exists := c.Get("session")
	if !exists {
		c.JSON(http.StatusInternalServerError, errorResponse(domain.NewError("internal", "session missing")))
		return
	}
	session := sess.(*domain.Session)

	ctx := c.Request.Context()

	images, err := h.images.ListBySession(ctx, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err))
		return
	}

	data := make([]dto.ImageResponse, 0, len(images))
	for _, img := range images {
		jobStatus := "unknown"
		job, err := h.jobs.FindByImageID(ctx, img.ID)
		if err != nil {
			slog.Warn("find job for image failed", "image", img.ID, "error", err)
		}
		if job != nil {
			jobStatus = job.Status
		}
		data = append(data, dto.ImageResponse{
			ID:        img.ID,
			Mime:      img.Mime,
			Width:     img.Width,
			Height:    img.Height,
			JobStatus: jobStatus,
			CreatedAt: img.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, dto.ImageListResponse{Data: data})
}
