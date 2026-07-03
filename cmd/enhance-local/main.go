// Command enhance-local reads an image file, runs it through the Enhancer
// pipeline (contrast/gamma/sharpen), and saves the result so you can visually
// inspect the effect of the image-enhancement step.
//
// Usage:
//
//	go run ./cmd/enhance-local -in exam.jpg -out exam_enhanced.jpg
//
// If -out is omitted it defaults to <stem>_enhanced.<ext>.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/vlgrigoriev/coeus/internal/ai/enhancer"
)

func main() {
	inPath := flag.String("in", "", "path to input image (required)")
	outPath := flag.String("out", "", "path to output image (default: <stem>_enhanced.<ext>)")
	flag.Parse()

	if *inPath == "" {
		fmt.Fprintln(os.Stderr, "error: -in flag is required")
		flag.Usage()
		os.Exit(1)
	}

	// Read input.
	src, err := os.ReadFile(*inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", *inPath, err)
		os.Exit(1)
	}

	// Derive MIME from extension.
	mime := mimeForExt(filepath.Ext(*inPath))
	if mime == "" {
		fmt.Fprintf(os.Stderr, "error: unsupported extension %q (want .jpg/.jpeg/.png/.webp)\n", filepath.Ext(*inPath))
		os.Exit(1)
	}

	// Derive output path.
	out := *outPath
	if out == "" {
		ext := filepath.Ext(*inPath)
		stem := strings.TrimSuffix(*inPath, ext)
		out = stem + "_enhanced" + ext
	}

	// Startup libvips.
	vips.Startup(nil)
	defer vips.Shutdown()

	// Enhance.
	e := enhancer.New(slog.Default())
	result, err := e.Enhance(context.Background(), src, mime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enhance failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(out, result, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", out, err)
		os.Exit(1)
	}

	fmt.Printf("enhanced image saved to %s (input: %s, output: %d bytes)\n", out, *inPath, len(result))
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}
