package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/vlgrigoriev/coeus/internal/httpapi/dto"
)

// --- Fakes ---

type fakeImageRepoFull struct {
	created   []byte
	mime      string
	width     int
	height    int
	returnID  string
	list      []*domain.Image
	err       error
}

func (f *fakeImageRepoFull) Create(_ context.Context, _ string, original []byte, mime string, w, h int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created = original
	f.mime = mime
	f.width = w
	f.height = h
	if f.returnID == "" {
		return "img-new", nil
	}
	return f.returnID, nil
}
func (f *fakeImageRepoFull) FindByID(context.Context, string) (*domain.Image, error) {
	return nil, nil
}
func (f *fakeImageRepoFull) ListBySession(_ context.Context, _ string) ([]*domain.Image, error) {
	return f.list, nil
}
func (f *fakeImageRepoFull) UpdateEnhanced(context.Context, string, []byte) error  { return nil }
func (f *fakeImageRepoFull) UpdateVerificationReport(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoFull) UpdateExtractionError(context.Context, string, []byte) error {
	return nil
}
func (f *fakeImageRepoFull) CleanBytes(context.Context, string) error            { return nil }
func (f *fakeImageRepoFull) CountBySession(context.Context, string) (int, error) { return 0, nil }

type fakeJobQueueForImages struct {
	enqueued   bool
	imageID    string
	sessionID  string
	jobByImage *domain.Job
}

func (q *fakeJobQueueForImages) Enqueue(_ context.Context, imageID, sessionID string) (string, error) {
	q.enqueued = true
	q.imageID = imageID
	q.sessionID = sessionID
	return "job-new", nil
}
func (q *fakeJobQueueForImages) Claim(context.Context) (*domain.Job, error) { return nil, nil }
func (q *fakeJobQueueForImages) Complete(context.Context, string) error     { return nil }
func (q *fakeJobQueueForImages) Fail(context.Context, string, string) error { return nil }
func (q *fakeJobQueueForImages) ReaperReclaim(context.Context, time.Duration, int) (reclaimed int, failed int, err error) {
	return 0, 0, nil
}
func (q *fakeJobQueueForImages) FindByImageID(_ context.Context, _ string) (*domain.Job, error) {
	return q.jobByImage, nil
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func newImageRouter(h *ImageHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", "user-1"); c.Next() })
	r.POST("/sessions/:id/images", func(c *gin.Context) {
		c.Set("session", &domain.Session{ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen})
		h.Upload(c)
	})
	r.GET("/sessions/:id/images", func(c *gin.Context) {
		c.Set("session", &domain.Session{ID: "sess-1", UserID: "user-1", Status: domain.SessionStatusOpen})
		h.List(c)
	})
	return r
}

func TestImageHandler_Upload(t *testing.T) {
	imgRepo := &fakeImageRepoFull{}
	jq := &fakeJobQueueForImages{}
	uploadCfg := config.UploadConfig{
		MaxBytes:     10 * 1024 * 1024,
		AllowedMimes: []string{"image/png", "image/jpeg", "image/webp"},
	}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	pngBytes := validPNG(t)
	body := &bytes.Buffer{}
	w := newMultipartForm(body, pngBytes)
	req := httptest.NewRequest("POST", "/sessions/sess-1/images", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp dto.ImageUploadResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ImageID == "" || resp.JobID == "" {
		t.Errorf("expected image_id and job_id, got %+v", resp)
	}
	if !jq.enqueued {
		t.Error("job was not enqueued")
	}
	if imgRepo.width != 4 || imgRepo.height != 4 {
		t.Errorf("dimensions = %dx%d, want 4x4", imgRepo.width, imgRepo.height)
	}
}

func TestImageHandler_UploadWrongMime(t *testing.T) {
	imgRepo := &fakeImageRepoFull{}
	jq := &fakeJobQueueForImages{}
	uploadCfg := config.UploadConfig{
		MaxBytes:     10 * 1024 * 1024,
		AllowedMimes: []string{"image/png", "image/jpeg", "image/webp"},
	}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	body := &bytes.Buffer{}
	w := newMultipartForm(body, []byte("this is not an image"))
	req := httptest.NewRequest("POST", "/sessions/sess-1/images", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid MIME", rr.Code)
	}
}

func TestImageHandler_List(t *testing.T) {
	imgRepo := &fakeImageRepoFull{
		list: []*domain.Image{
			{ID: "img-1", Mime: "image/png", Width: 100, Height: 200, CreatedAt: "2026-06-20T12:00:00Z"},
		},
	}
	jq := &fakeJobQueueForImages{jobByImage: &domain.Job{ID: "job-1", Status: domain.JobStatusDone}}
	uploadCfg := config.UploadConfig{}
	h := NewImageHandler(imgRepo, jq, uploadCfg)
	r := newImageRouter(h)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/sessions/sess-1/images", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp dto.ImageListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(resp.Data))
	}
	if resp.Data[0].JobStatus != domain.JobStatusDone {
		t.Errorf("job_status = %q, want done", resp.Data[0].JobStatus)
	}
}

// newMultipartForm creates a multipart form with an "image" field.
func newMultipartForm(buf *bytes.Buffer, data []byte) *multipart.Writer {
	w := multipart.NewWriter(buf)
	fw, _ := w.CreateFormFile("image", "test.png")
	fw.Write(data)
	w.Close()
	return w
}
