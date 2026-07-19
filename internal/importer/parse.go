package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// File-level sentinel errors (spec §11). All carry the "validation" code so
// domain.HTTPStatus maps them to 400 and errorResponse renders the envelope.
var (
	ErrEmptyFile         = domain.NewError("validation", "empty file")
	ErrUnsupportedFormat = domain.NewError("validation", "unsupported file format")
	ErrLegacyXLS         = domain.NewError("validation", "legacy .xls not supported — save as .xlsx")
	ErrTooManyRows       = domain.NewError("validation", "too many rows")
)

// FileKind is the sniffed upload format (spec §5.1).
type FileKind int

const (
	KindCSV FileKind = iota
	KindXLSX
)

// ole2Magic is the CFB (Compound File Binary) header that identifies legacy
// .xls files. http.DetectContentType does not recognise it, so we check it
// explicitly before the content-type switch (spec §5.1).
var ole2Magic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// SniffKind detects the upload format from content bytes — never the file
// extension — via http.DetectContentType (spec §5.1). Non-UTF-8 CSV sniffs as
// application/octet-stream and is therefore rejected as unsupported.
func SniffKind(data []byte) (FileKind, error) {
	if len(data) == 0 {
		return 0, ErrEmptyFile
	}
	if len(data) >= len(ole2Magic) && data[:len(ole2Magic)][0] == ole2Magic[0] {
		// Full bytes.Equal check only when first byte matches (cheap early-out).
		if bytes.Equal(data[:len(ole2Magic)], ole2Magic) {
			return 0, ErrLegacyXLS
		}
	}
	ct := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(ct, "text/"):
		return KindCSV, nil
	case ct == "application/zip":
		return KindXLSX, nil
	default:
		return 0, ErrUnsupportedFormat
	}
}

// parseCSV streams all rows from a UTF-8 CSV reader (spec §5.2). Variable
// column counts are allowed here and handled per-row by normalizeRow.
func parseCSV(r io.Reader) ([][]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	var rows [][]string
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				err = fmt.Errorf("line %d, column %d: %w", pe.StartLine, pe.Column, pe.Err)
			}
			return nil, domain.NewError("validation", fmt.Sprintf("malformed csv: %v", err))
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

// normalizeRow applies the deterministic row-shape rule (spec §5.4), identical
// for CSV and XLSX: trim trailing empty cells, right-pad short rows to 5
// columns, reject rows with more than 5 remaining columns.
func normalizeRow(cells []string) ([5]string, error) {
	var out [5]string
	trimmed := cells
	for len(trimmed) > 0 && strings.TrimSpace(trimmed[len(trimmed)-1]) == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) > 5 {
		return out, errors.New("too many columns (max 5)")
	}
	copy(out[:], trimmed)
	return out, nil
}

// splitMulti splits a multi-value cell on ';', trims each item, and drops
// empty items (spec §5.4).
func splitMulti(cell string) []string {
	parts := strings.Split(cell, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
