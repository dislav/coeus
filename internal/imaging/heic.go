// Package imaging provides format detection and conversion helpers built on
// govips/libvips. It is used by the HTTP upload layer to normalize incoming
// formats (e.g. iPhone HEIC → JPEG) before storage, so the rest of the system
// — browser-facing image viewer, enhancer, AI vision extractor — always sees a
// universally decodable image.
//
// govips requires a one-time vips.Startup before any operation. That lifecycle
// is owned by app.Build (see internal/app/wire.go); the functions here assume
// libvips is already initialized.
package imaging

import (
	"encoding/binary"
	"fmt"

	"github.com/davidbyttow/govips/v2/vips"
)

// heicBrands are the ISO Base Media File Format "ftyp" brands that identify a
// HEIF/HEIC container. iPhones write major brand "heic".
var heicBrands = map[string]bool{
	"heic": true, // Apple HEIC
	"heix": true, // HEIC extension
	"hevc": true,
	"heim": true, // HEIF image (multi-image)
	"heis": true, // HEIF image sequence
	"mif1": true, // HEIF baseline / generic
	"msf1": true, // HEIF sequence
}

// IsHEIC reports whether data begins with an ISO BMFF "ftyp" box carrying a
// HEIC/HEIF brand.
//
// Go's net/http content sniffer (http.DetectContentType) does not recognize
// HEIC and returns "application/octet-stream" for it, so the upload layer must
// detect the format itself. The ftyp box is always the first box and is tiny,
// so this only ever inspects the header — never the whole upload.
func IsHEIC(data []byte) bool {
	// ftyp box layout: uint32 size | "ftyp" | major brand (4) | minor version (4) | compatible brands (4 each)...
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	if heicBrands[string(data[8:12])] {
		return true
	}
	// Compatible brands follow the minor version, four bytes each. Bound the
	// scan to the declared ftyp box size (falling back to len(data)) and a hard
	// cap so a malformed size can never make us scan a large upload.
	end := int(binary.BigEndian.Uint32(data[0:4]))
	if end > len(data) {
		end = len(data)
	}
	if end > 64 { // ftyp boxes are small; this is more than enough
		end = 64
	}
	for i := 16; i+4 <= end; i += 4 {
		if heicBrands[string(data[i:i+4])] {
			return true
		}
	}
	return false
}

// ConvertToJPEG decodes data (any format libvips understands, including HEIC),
// honors EXIF orientation, and re-encodes it as JPEG at the given quality.
// It makes no other adjustments — enhancement happens later in the pipeline.
func ConvertToJPEG(data []byte, quality int) ([]byte, error) {
	img, err := vips.NewImageFromBuffer(data)
	if err != nil {
		return nil, fmt.Errorf("imaging: decode: %w", err)
	}
	defer img.Close()

	if err := img.AutoRotate(); err != nil {
		return nil, fmt.Errorf("imaging: auto-rotate: %w", err)
	}

	buf, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: quality})
	if err != nil {
		return nil, fmt.Errorf("imaging: export jpeg: %w", err)
	}
	return buf, nil
}
