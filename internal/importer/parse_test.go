package importer

import (
	"strings"
	"testing"
)

func sniffable(prefix []byte) []byte {
	// http.DetectContentType inspects up to the first 512 bytes.
	buf := make([]byte, 512)
	copy(buf, prefix)
	return buf
}

func TestSniffKind(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    FileKind
		wantErr error
	}{
		{"csv text", []byte("What is 2+2?,3;4,4,math,arith\n"), KindCSV, nil},
		{"xlsx zip", sniffable([]byte("PK\x03\x04")), KindXLSX, nil},
		{"legacy xls", sniffable([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}), 0, ErrLegacyXLS},
		{"png unsupported", sniffable([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}), 0, ErrUnsupportedFormat},
		{"empty", nil, 0, ErrEmptyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, err := SniffKind(tt.data)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("SniffKind() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SniffKind() unexpected err = %v", err)
			}
			if kind != tt.want {
				t.Errorf("SniffKind() = %v, want %v", kind, tt.want)
			}
		})
	}
}

func TestParseCSV(t *testing.T) {
	in := "q1,a;b,a,e1,t1\nq2,,42,e2,t1;t2\n"
	rows, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0][1] != "a;b" || rows[1][2] != "42" {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseCSV_VariableFields(t *testing.T) {
	// FieldsPerRecord = -1: ragged rows must not error (handled per-row later).
	in := "q1,a;b,a\nq2,a;b,a,e,t,EXTRA\n"
	rows, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if len(rows) != 2 || len(rows[0]) != 3 || len(rows[1]) != 6 {
		t.Errorf("rows = %v", rows)
	}
}

func TestParseCSV_TrimLeadingSpace(t *testing.T) {
	rows, err := parseCSV(strings.NewReader("q1, a;b ,a,e,t\n"))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	if rows[0][1] != "a;b " {
		t.Errorf("rows[0][1] = %q, want %q (leading trimmed, trailing kept)", rows[0][1], "a;b ")
	}
}

func TestParseCSV_Malformed(t *testing.T) {
	_, err := parseCSV(strings.NewReader("q1,\"unterminated,a\n"))
	if err == nil {
		t.Fatal("expected error for malformed csv, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "malformed csv") {
		t.Errorf("err = %q, want it to mention malformed csv", msg)
	}
	if !strings.Contains(msg, "line 1") {
		t.Errorf("err = %q, want it to mention the line number", msg)
	}
}

func TestNormalizeRow(t *testing.T) {
	tests := []struct {
		name    string
		cells   []string
		want    [5]string
		wantErr bool
	}{
		{"exactly 5", []string{"q", "c", "a", "e", "t"}, [5]string{"q", "c", "a", "e", "t"}, false},
		{"pad short row", []string{"q", "c", "a"}, [5]string{"q", "c", "a", "", ""}, false},
		{"trim trailing empties", []string{"q", "c", "a", "", ""}, [5]string{"q", "c", "a", "", ""}, false},
		{"trim then pad", []string{"q", "c", "a", "", " ", ""}, [5]string{"q", "c", "a", "", ""}, false},
		{"over 5 after trim errors", []string{"q", "c", "a", "e", "t", "EXTRA"}, [5]string{}, true},
		{"over 5 only via empties is fine", []string{"q", "c", "a", "e", "t", ""}, [5]string{"q", "c", "a", "e", "t"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRow(tt.cells)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRow: %v", err)
			}
			if got != tt.want {
				t.Errorf("normalizeRow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitMulti(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a;b;c", []string{"a", "b", "c"}},
		{"a; b ;c", []string{"a", "b", "c"}},
		{"a;;b", []string{"a", "b"}},
		{"", []string{}},
		{" ; ", []string{}},
		{"single", []string{"single"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := splitMulti(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitMulti(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitMulti(%q) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}
