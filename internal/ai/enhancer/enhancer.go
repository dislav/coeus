// Package enhancer implements pipeline.ImageEnhancer using govips (libvips).
// It applies deterministic contrast/gamma/sharpen adjustments and re-encodes
// the image to the same MIME the caller provided. It makes no AI calls.
package enhancer

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// Compile-time guarantee that Enhancer satisfies the port.
var _ pipeline.ImageEnhancer = (*Enhancer)(nil)

type Enhancer struct {
	log *slog.Logger
}

func New(log *slog.Logger) *Enhancer {
	if log == nil {
		log = slog.Default()
	}
	return &Enhancer{log: log}
}

// Enhance applies auto-rotate, +15% contrast, gamma 1.15, mild sharpen, then
// re-encodes to the same MIME. Any failure returns (nil, err); the pipeline
// falls back to the original bytes.
func (e *Enhancer) Enhance(ctx context.Context, original []byte, mime string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("enhance: %w", err)
	}

	img, err := vips.NewImageFromBuffer(original)
	if err != nil {
		return nil, fmt.Errorf("enhance: decode: %w", err)
	}
	defer img.Close()

	if err := img.AutoRotate(); err != nil {
		return nil, fmt.Errorf("enhance: auto-rotate: %w", err)
	}

	if err := img.ThumbnailWithSize(1000, 1000, vips.InterestingNone, vips.SizeDown); err != nil {
		return nil, fmt.Errorf("enhance: resize: %w", err)
	}

	if err := img.Modulate(1.25, 0.0, 0.0); err != nil {
		return nil, fmt.Errorf("enhance: modulate: %w", err)
	}

	if err := img.ExtractBand(0, 1); err != nil {
		return nil, fmt.Errorf("enhance: extract band: %w", err)
	}

	if err := img.Linear1(2.0, -250); err != nil {
		return nil, fmt.Errorf("enhance: contrast: %w", err)
	}

	if err := img.Sharpen(1.25, 2.0, 2.5); err != nil {
		return nil, fmt.Errorf("enhance: sharpen: %w", err)
	}

	switch mime {
	case "image/jpeg":
		buf, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: 40})
		return buf, err
	case "image/png":
		buf, _, err := img.ExportPng(&vips.PngExportParams{Compression: 7})
		return buf, err
	case "image/webp":
		buf, _, err := img.ExportWebp(&vips.WebpExportParams{Quality: 40})
		return buf, err
	default:
		return nil, fmt.Errorf("enhance: unsupported MIME %q", mime)
	}
}
