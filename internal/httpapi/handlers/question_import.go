package handlers

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/importer"
)

// QuestionImportHandler serves POST /api/v1/questions/upload (spec §4).
type QuestionImportHandler struct {
	svc       *importer.Service
	uploadCfg config.UploadConfig
}

func NewQuestionImportHandler(svc *importer.Service, uploadCfg config.UploadConfig) *QuestionImportHandler {
	return &QuestionImportHandler{svc: svc, uploadCfg: uploadCfg}
}

// Upload reads a multipart CSV/XLSX file, imports it synchronously, and
// returns the per-row report (200) or a file-level error envelope (400).
// Read pattern mirrors ImageHandler.Upload: MaxBytesReader → FormFile → ReadAll.
func (h *QuestionImportHandler) Upload(c *gin.Context) {
	// Enforce size cap (spec §4.3).
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.uploadCfg.MaxBytes)

	file, _, err := c.Request.FormFile("file")
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

	kind, err := importer.SniffKind(data)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}

	userID := c.GetString("user_id")
	// The multipart reader is fully drained by io.ReadAll + SniffKind, so a
	// fresh bytes.Reader over the byte slice — never the multipart stream —
	// is passed to the importer (spec §9.5).
	rep, err := h.svc.Import(c.Request.Context(), bytes.NewReader(data), kind, userID)
	if err != nil {
		c.JSON(domain.HTTPStatus(err), errorResponse(err))
		return
	}

	resp := dto.ImportReportResponse{
		TotalRows: rep.TotalRows,
		Created:   rep.Created,
		Updated:   rep.Updated,
		Failed:    rep.Failed,
		Errors:    make([]dto.ImportRowError, 0, len(rep.Errors)),
	}
	for _, re := range rep.Errors {
		resp.Errors = append(resp.Errors, dto.ImportRowError{Row: re.Row, Message: re.Message})
	}
	c.JSON(http.StatusOK, resp)
}
