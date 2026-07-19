package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
	"github.com/vlgrigoriev/coeus/internal/importer"
)

// --- fakes ---

type fakeImportUpserter struct {
	createdByHash map[string]bool
	userIDs       []string
}

func (f *fakeImportUpserter) UpsertFromImport(_ context.Context, q *domain.Question) (bool, error) {
	if f.createdByHash == nil {
		f.createdByHash = map[string]bool{}
	}
	if q.VerifiedBy != nil {
		f.userIDs = append(f.userIDs, *q.VerifiedBy)
	}
	if f.createdByHash[q.TextHash] {
		return false, nil
	}
	f.createdByHash[q.TextHash] = true
	return true, nil
}

// --- helpers ---

func newImportHandler(maxBytes int64) (*QuestionImportHandler, *fakeImportUpserter) {
	up := &fakeImportUpserter{}
	svc := importer.New(up, nil, 100, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return NewQuestionImportHandler(svc, config.UploadConfig{MaxBytes: maxBytes}), up
}

func newImportRouter(h *QuestionImportHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/api/v1/questions/upload", h.Upload)
	return r
}

// newFileMultipartForm creates a multipart form with a "file" field.
func newFileMultipartForm(buf *bytes.Buffer, data []byte) *multipart.Writer {
	w := multipart.NewWriter(buf)
	fw, _ := w.CreateFormFile("file", "questions.csv")
	fw.Write(data)
	w.Close()
	return w
}

func postUpload(t *testing.T, r *gin.Engine, body *bytes.Buffer, w *multipart.Writer) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/questions/upload", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func sniffable512(prefix []byte) []byte {
	buf := make([]byte, 512)
	copy(buf, prefix)
	return buf
}

// --- tests ---

func TestImportHandler_UploadCSVReport(t *testing.T) {
	h, up := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	csvData := []byte("What is 2+2?,3;4,4,math,arith\nBad row?,only,a,,\n")
	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, csvData)
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp dto.ImportReportResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TotalRows != 2 || resp.Created != 1 || resp.Updated != 0 || resp.Failed != 1 {
		t.Errorf("report = %+v", resp)
	}
	if len(resp.Errors) != 1 || resp.Errors[0].Row != 2 {
		t.Errorf("errors = %+v, want one error at row 2", resp.Errors)
	}
	if len(up.userIDs) == 0 || up.userIDs[0] != "user-1" {
		t.Errorf("verified_by = %v, want user-1 from JWT context", up.userIDs)
	}
}

func TestImportHandler_UnsupportedFormat(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, sniffable512([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}))
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "validation" || env.Error.Message != "unsupported file format" {
		t.Errorf("body = %s, want validation/unsupported file format", rr.Body.String())
	}
}

func TestImportHandler_LegacyXLS(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, sniffable512([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}))
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("legacy .xls not supported")) {
		t.Errorf("body = %s, want legacy .xls message", rr.Body.String())
	}
}

func TestImportHandler_EmptyFile(t *testing.T) {
	h, _ := newImportHandler(10 * 1024 * 1024)
	r := newImportRouter(h)

	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, []byte{})
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("empty file")) {
		t.Errorf("body = %s, want empty file message", rr.Body.String())
	}
}

func TestImportHandler_OversizeBody(t *testing.T) {
	h, _ := newImportHandler(16) // 16-byte cap
	r := newImportRouter(h)

	csvData := []byte("What is 2+2?,3;4,4,math,arith\n")
	body := &bytes.Buffer{}
	w := newFileMultipartForm(body, csvData)
	rr := postUpload(t, r, body, w)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize body", rr.Code)
	}
}
