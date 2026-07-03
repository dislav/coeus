package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// filenameForMime derives an upload filename from the image MIME type. Moonshot
// only needs a reasonable extension; the base name is arbitrary.
func filenameForMime(mime string) string {
	ext := ".bin"
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	}
	return "image" + ext
}

// uploadImage POSTs the raw image bytes to {baseURL}/files as multipart
// form-data with purpose=image and returns the Moonshot file id. Any non-2xx
// response is returned as a transport error so the pipeline retries it.
func (e *Extractor) uploadImage(ctx context.Context, image []byte, mime string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("purpose", "image"); err != nil {
		return "", fmt.Errorf("upload: write purpose: %w", err)
	}
	part, err := mw.CreateFormFile("file", filenameForMime(mime))
	if err != nil {
		return "", fmt.Errorf("upload: create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(image)); err != nil {
		return "", fmt.Errorf("upload: copy bytes: %w", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("upload: close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/files", &buf)
	if err != nil {
		return "", fmt.Errorf("upload: new request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("upload: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var fr struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		return "", fmt.Errorf("upload: decode response: %w", err)
	}
	if fr.ID == "" {
		return "", fmt.Errorf("upload: empty file id in response")
	}
	return fr.ID, nil
}

// deleteFile removes an uploaded file via DELETE {baseURL}/files/{id}.
// Best-effort: callers log the returned error and never propagate it.
func (e *Extractor) deleteFile(ctx context.Context, fileID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+"/files/"+fileID, nil)
	if err != nil {
		return fmt.Errorf("delete file %s: new request: %w", fileID, err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", fileID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("delete file %s: unexpected status %d", fileID, resp.StatusCode)
	}
	return nil
}
