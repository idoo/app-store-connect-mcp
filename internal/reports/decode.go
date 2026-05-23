package reports

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Table is a decoded report: one header row and string cells. Every row has
// exactly len(Columns) cells. Footer holds trailing summary lines such as
// Total_Rows found in finance reports, keyed by their label.
type Table struct {
	Columns []string
	Rows    [][]string
	Footer  map[string]string
}

// Col returns the index of the named column, matching case-insensitively and
// ignoring surrounding whitespace, underscores and repeated spaces; -1 if
// absent.
func (t *Table) Col(name string) int {
	want := normalizeHeader(name)
	for i, c := range t.Columns {
		if normalizeHeader(c) == want {
			return i
		}
	}
	return -1
}

// colAny returns the index of the first matching name, or -1.
func (t *Table) colAny(names ...string) int {
	for _, n := range names {
		if i := t.Col(n); i >= 0 {
			return i
		}
	}
	return -1
}

func normalizeHeader(s string) string {
	s = strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// Decode reads a gzip-compressed or plain TSV/CSV report. It enforces lim on
// the decompressed size, columns and rows, returning errors wrapping
// ErrTooLarge or ErrCorrupt.
func Decode(r io.Reader, lim Limits) (*Table, error) {
	lim = lim.withDefaults()
	data, err := readAll(r, lim.MaxBytes)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		data = bytes.ToValidUTF8(data, []byte("\uFFFD"))
	}
	records, err := splitRecords(data, sniffDelimiter(data))
	if err != nil {
		return nil, err
	}
	return buildTable(records, lim)
}

// readAll returns the decompressed bytes, at most max of them.
func readAll(r io.Reader, maxBytes int64) ([]byte, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reports: read header: %w", err)
	}
	src := io.Reader(br)
	gz := len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b
	if gz {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("reports: gzip header: %w: %w", ErrCorrupt, err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	}
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		if gz {
			return nil, fmt.Errorf("reports: gzip stream: %w: %w", ErrCorrupt, err)
		}
		return nil, fmt.Errorf("reports: read: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("reports: decompressed size exceeds %d bytes: %w", maxBytes, ErrTooLarge)
	}
	return data, nil
}

// sniffDelimiter picks tab or comma from the first line.
func sniffDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	if bytes.Count(line, []byte{','}) > bytes.Count(line, []byte{'\t'}) {
		return ','
	}
	return '\t'
}

// splitRecords splits data into cells. TSV is split literally (Apple does not
// quote TSV and titles may contain quotes); CSV uses RFC 4180 parsing.
func splitRecords(data []byte, delim rune) ([][]string, error) {
	if delim == '\t' {
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		out := make([][]string, 0, len(lines))
		for _, l := range lines {
			out = append(out, strings.Split(strings.TrimSuffix(l, "\r"), "\t"))
		}
		return out, nil
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	out, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reports: csv: %w: %w", ErrCorrupt, err)
	}
	return out, nil
}

func blank(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// footerKey reports whether rec is a trailing summary line like
// "Total_Rows\t42" and returns its label.
func footerKey(rec []string) (string, bool) {
	k := strings.TrimSpace(rec[0])
	if !strings.HasPrefix(strings.ToLower(k), "total_") {
		return "", false
	}
	return k, true
}

func firstValue(rec []string) string {
	for _, c := range rec[1:] {
		if v := strings.TrimSpace(c); v != "" {
			return v
		}
	}
	return ""
}

func buildTable(records [][]string, lim Limits) (*Table, error) {
	t := &Table{}
	inFooter := false
	for _, rec := range records {
		if len(rec) > lim.MaxColumns {
			return nil, fmt.Errorf("reports: %d columns exceeds %d: %w", len(rec), lim.MaxColumns, ErrTooLarge)
		}
		if blank(rec) {
			continue
		}
		if t.Columns == nil {
			t.Columns = make([]string, len(rec))
			for i, c := range rec {
				t.Columns[i] = strings.TrimSpace(c)
			}
			continue
		}
		if k, ok := footerKey(rec); ok || inFooter {
			inFooter = true
			if t.Footer == nil {
				t.Footer = map[string]string{}
			}
			if k == "" {
				k = strings.TrimSpace(rec[0])
			}
			t.Footer[k] = firstValue(rec)
			continue
		}
		if len(t.Rows) >= lim.MaxRows {
			return nil, fmt.Errorf("reports: more than %d rows: %w", lim.MaxRows, ErrTooLarge)
		}
		row := make([]string, len(t.Columns))
		for i := range row {
			if i < len(rec) {
				row[i] = strings.TrimSpace(rec[i])
			}
		}
		t.Rows = append(t.Rows, row)
	}
	if t.Columns == nil {
		return nil, fmt.Errorf("reports: no header row: %w", ErrCorrupt)
	}
	return t, nil
}
