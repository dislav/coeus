package importer

import (
	"fmt"
	"io"

	"github.com/vlgrigoriev/coeus/internal/domain"
	"github.com/xuri/excelize/v2"
)

// parseXLSX streams all rows of the FIRST sheet (spec §5.3). Decompression
// limits are explicit: the library defaults (16 GB / 16 MB) are unsafe
// against the 10 MB upload cap.
func parseXLSX(r io.Reader) ([][]string, error) {
	f, err := excelize.OpenReader(r, excelize.Options{
		UnzipSizeLimit:    100 << 20,
		UnzipXMLSizeLimit: 64 << 20,
	})
	if err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, domain.NewError("validation", "malformed xlsx: no sheets")
	}

	rows, err := f.Rows(sheets[0])
	if err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	defer func() { _ = rows.Close() }()

	var out [][]string
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
		}
		out = append(out, cols)
	}
	if err := rows.Error(); err != nil {
		return nil, domain.NewError("validation", fmt.Sprintf("malformed xlsx: %v", err))
	}
	return out, nil
}
