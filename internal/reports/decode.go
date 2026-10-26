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
	return scan(data, sniffDelimiter(data), lim)
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
	data, err := readCapped(src, maxBytes)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return nil, err
		}
		if gz {
			return nil, fmt.Errorf("reports: gzip stream: %w: %w", ErrCorrupt, err)
		}
		return nil, fmt.Errorf("reports: read: %w", err)
	}
	return data, nil
}

// readCapped reads at most maxBytes from r into one exactly sized buffer. It
// fills chunks of growing size (64 KiB up to 4 MiB) and joins them once, so a
// report costs about twice its size in allocations rather than the repeated
// regrowth of io.ReadAll.
func readCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	const minChunk, maxChunk = 64 << 10, 4 << 20
	var (
		chunks [][]byte
		total  int64
		size   = minChunk
	)
	lr := io.LimitReader(r, maxBytes+1)
	for {
		buf := make([]byte, size)
		n, err := fill(lr, buf)
		total += int64(n)
		if n > 0 {
			chunks = append(chunks, buf[:n])
		}
		if total > maxBytes {
			return nil, fmt.Errorf("reports: decompressed size exceeds %d bytes: %w", maxBytes, ErrTooLarge)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		size = min(size*2, maxChunk)
	}
	if len(chunks) == 1 {
		return chunks[0], nil
	}
	data := make([]byte, 0, total)
	for _, c := range chunks {
		data = append(data, c...)
	}
	return data, nil
}

// fill reads into buf until it is full or r fails. Unlike io.ReadFull it
// returns r's own error, so a truncated gzip stream's io.ErrUnexpectedEOF is
// not mistaken for a clean end of input.
func fill(r io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
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

// scan decodes data record by record without materialising every record
// first. TSV is split literally line by line (Apple does not quote TSV and
// titles may contain quotes), checking the column count before splitting;
// CSV uses RFC 4180 parsing one record at a time.
func scan(data []byte, delim rune, lim Limits) (*Table, error) {
	b := tableBuilder{lim: lim}
	if delim == '\t' {
		for len(data) > 0 {
			// Skip a run of line breaks at once: blank lines cost O(1)
			// memory and no per-line work.
			if data[0] == '\n' || data[0] == '\r' {
				data = bytes.TrimLeft(data, "\r\n")
				continue
			}
			line := data
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				line, data = data[:i], data[i+1:]
			} else {
				data = nil
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if n := bytes.Count(line, []byte{'\t'}) + 1; n > lim.MaxColumns {
				return nil, tooManyColumns(n, lim.MaxColumns)
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := b.add(strings.Split(string(line), "\t")); err != nil {
				return nil, err
			}
		}
		return b.table()
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return b.table()
		}
		if err != nil {
			return nil, fmt.Errorf("reports: csv: %w: %w", ErrCorrupt, err)
		}
		if len(rec) > lim.MaxColumns {
			return nil, tooManyColumns(len(rec), lim.MaxColumns)
		}
		if blank(rec) {
			continue
		}
		if err := b.add(rec); err != nil {
			return nil, err
		}
	}
}

func tooManyColumns(n, maxCols int) error {
	return fmt.Errorf("reports: %d columns exceeds %d: %w", n, maxCols, ErrTooLarge)
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

// tableBuilder assembles a Table from non-blank records. Total_* rows form
// the footer of the current section; a repeated header after a footer starts
// a new section whose rows append to the same table. Any other record after
// a footer is corrupt: rows are never silently dropped.
type tableBuilder struct {
	lim      Limits
	t        Table
	header   []string // normalized header cells
	records  int      // data and footer records seen
	section  int
	inFooter bool
}

func (b *tableBuilder) add(rec []string) error {
	if b.t.Columns == nil {
		b.t.Columns = make([]string, len(rec))
		b.header = make([]string, len(rec))
		for i, c := range rec {
			b.t.Columns[i] = strings.TrimSpace(c)
			b.header[i] = normalizeHeader(c)
		}
		b.section = 1
		return nil
	}
	if b.inFooter && b.isHeader(rec) {
		b.inFooter = false
		b.section++
		return nil
	}
	if b.records >= b.lim.MaxRows {
		return fmt.Errorf("reports: more than %d rows: %w", b.lim.MaxRows, ErrTooLarge)
	}
	b.records++
	if k, ok := footerKey(rec); ok {
		b.inFooter = true
		if b.t.Footer == nil {
			b.t.Footer = map[string]string{}
		}
		if b.section > 1 {
			k = fmt.Sprintf("%s#%d", k, b.section)
		}
		b.t.Footer[k] = firstValue(rec)
		return nil
	}
	if b.inFooter {
		return fmt.Errorf("reports: data after report footer: %w", ErrCorrupt)
	}
	row := make([]string, len(b.t.Columns))
	for i := range row {
		if i < len(rec) {
			row[i] = strings.TrimSpace(rec[i])
		}
	}
	b.t.Rows = append(b.t.Rows, row)
	return nil
}

// isHeader reports whether rec repeats the header, ignoring trailing blank
// cells and the header normalisation used by Col.
func (b *tableBuilder) isHeader(rec []string) bool {
	for i := range max(len(rec), len(b.header)) {
		var got, want string
		if i < len(rec) {
			got = normalizeHeader(rec[i])
		}
		if i < len(b.header) {
			want = b.header[i]
		}
		if got != want {
			return false
		}
	}
	return true
}

func (b *tableBuilder) table() (*Table, error) {
	if b.t.Columns == nil {
		return nil, fmt.Errorf("reports: no header row: %w", ErrCorrupt)
	}
	return &b.t, nil
}
