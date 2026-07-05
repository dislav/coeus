package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
	_ "image/jpeg" // register jpeg so image.DecodeConfig can read ConvertToJPEG output
)

func TestMain(m *testing.M) {
	vips.Startup(nil)
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

// makeHEIC builds a small in-memory HEIC via govips/libheif for round-trip
// tests. It skips the test if the installed libvips cannot encode HEIC (e.g.
// a build without the libheif save plugin).
func makeHEIC(t *testing.T) []byte {
	t.Helper()
	src := &bytes.Buffer{}
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := png.Encode(src, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	vimg, err := vips.NewImageFromBuffer(src.Bytes())
	if err != nil {
		t.Fatalf("load png: %v", err)
	}
	defer vimg.Close()
	out, _, err := vimg.ExportHeif(vips.NewHeifExportParams())
	if err != nil {
		t.Skipf("libvips cannot export HEIC in this environment: %v", err)
	}
	return out
}

func TestIsHEIC(t *testing.T) {
	heic := makeHEIC(t)

	pngBuf := &bytes.Buffer{}
	if err := png.Encode(pngBuf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	// A valid ftyp box with a non-HEIC brand (MP4/ISOM).
	mp4Ftyp := []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0, 0, 0x02, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '2'}

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"heic fixture", heic, true},
		{"png", pngBuf.Bytes(), false},
		{"garbage", []byte("not an image at all"), false},
		{"empty", []byte{}, false},
		{"too short for ftyp", []byte{0, 0, 0, 4, 'f', 't'}, false},
		{"ftyp wrong brand (mp4)", mp4Ftyp, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsHEIC(tc.data); got != tc.want {
				t.Errorf("IsHEIC(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestConvertToJPEG_FromHEIC(t *testing.T) {
	heic := makeHEIC(t)

	out, err := ConvertToJPEG(heic, 90)
	if err != nil {
		t.Fatalf("ConvertToJPEG: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("output is empty")
	}

	// The output must decode as JPEG and preserve the source dimensions.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	if cfg.Width != 8 || cfg.Height != 6 {
		t.Errorf("dims = %dx%d, want 8x6", cfg.Width, cfg.Height)
	}
}

func TestConvertToJPEG_InvalidBytes(t *testing.T) {
	if _, err := ConvertToJPEG([]byte("not an image"), 90); err == nil {
		t.Fatal("expected error for invalid bytes, got nil")
	}
}
