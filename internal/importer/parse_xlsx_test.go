package importer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// xlsxFixture builds a one-sheet workbook in memory with the given rows.
func xlsxFixture(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	for i, row := range rows {
		for j, v := range row {
			cell, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}
			if err := f.SetCellValue("Sheet1", cell, v); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func TestParseXLSX_FirstSheetRows(t *testing.T) {
	data := xlsxFixture(t, [][]string{
		{"q1", "a;b", "a", "expl", "t1;t2"},
		{"q2", "", "42", "", ""},
	})
	rows, err := parseXLSX(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseXLSX: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0][0] != "q1" || rows[0][1] != "a;b" || rows[0][4] != "t1;t2" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1][0] != "q2" {
		t.Errorf("row 1 = %v", rows[1])
	}
}

func TestParseXLSX_FirstSheetOnly(t *testing.T) {
	f := excelize.NewFile()
	if err := f.SetCellValue("Sheet1", "A1", "from-sheet-1"); err != nil {
		t.Fatalf("set cell: %v", err)
	}
	f.NewSheet("Sheet2")
	if err := f.SetCellValue("Sheet2", "A1", "from-sheet-2"); err != nil {
		t.Fatalf("set cell: %v", err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	_ = f.Close()

	rows, err := parseXLSX(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("parseXLSX: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "from-sheet-1" {
		t.Errorf("rows = %v, want first sheet only", rows)
	}
}

func TestParseXLSX_Malformed(t *testing.T) {
	// Truncated zip — OpenReader must fail and surface a file-level error.
	data := xlsxFixture(t, [][]string{{"q", "a;b", "a", "", ""}})
	_, err := parseXLSX(bytes.NewReader(data[:len(data)/2]))
	if err == nil {
		t.Fatal("expected error for truncated xlsx, got nil")
	}
	if !strings.Contains(err.Error(), "malformed xlsx") {
		t.Errorf("err = %q, want it to mention malformed xlsx", err.Error())
	}
}
