package enhancer

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
)

func TestMain(m *testing.M) {
	vips.Startup(nil)
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// encodePNG builds a small in-memory PNG for round-trip tests.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// toMime re-encodes the PNG source into the target MIME via govips so the
// enhancer receives the same container it would in production.
func toMime(t *testing.T, pngBytes []byte, mime string) []byte {
	t.Helper()
	img, err := vips.NewImageFromBuffer(pngBytes)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	defer img.Close()
	switch mime {
	case "image/jpeg":
		b, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: 92})
		if err != nil {
			t.Fatalf("export jpeg: %v", err)
		}
		return b
	case "image/png":
		b, _, err := img.ExportPng(&vips.PngExportParams{Compression: 6})
		if err != nil {
			t.Fatalf("export png: %v", err)
		}
		return b
	case "image/webp":
		b, _, err := img.ExportWebp(&vips.WebpExportParams{Quality: 92})
		if err != nil {
			t.Fatalf("export webp: %v", err)
		}
		return b
	default:
		t.Fatalf("unsupported test mime %q", mime)
		return nil
	}
}

// decodedDims re-decodes the enhancer output and returns its dimensions.
func decodedDims(t *testing.T, buf []byte) (int, int) {
	t.Helper()
	img, err := vips.NewImageFromBuffer(buf)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	defer img.Close()
	return img.Width(), img.Height()
}

func TestEnhancer_RoundTrip(t *testing.T) {
	e := New(quietLogger())
	src := encodePNG(t, 8, 6)

	for _, mime := range []string{"image/jpeg", "image/png", "image/webp"} {
		t.Run(mime, func(t *testing.T) {
			in := toMime(t, src, mime)
			out, err := e.Enhance(t.Context(), in, mime)
			if err != nil {
				t.Fatalf("Enhance(%s): %v", mime, err)
			}
			if len(out) == 0 {
				t.Fatalf("output is empty")
			}
			if bytes.Equal(out, in) {
				t.Errorf("output identical to input — enhance is a no-op")
			}
			w, h := decodedDims(t, out)
			if w != 8 || h != 6 {
				t.Errorf("dims = %dx%d, want 8x6", w, h)
			}
		})
	}
}

func TestEnhancer_InvalidBytes(t *testing.T) {
	e := New(quietLogger())
	_, err := e.Enhance(t.Context(), []byte("not an image"), "image/jpeg")
	if err == nil {
		t.Fatal("expected error for invalid bytes, got nil")
	}
}

func TestEnhancer_UnsupportedMime(t *testing.T) {
	e := New(quietLogger())
	pngBytes := toMime(t, encodePNG(t, 4, 4), "image/png")
	_, err := e.Enhance(t.Context(), pngBytes, "application/pdf")
	if err == nil {
		t.Fatal("expected error for unsupported MIME, got nil")
	}
}
