package reports

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openFixture(t *testing.T, name string) *Table {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tab, err := Decode(f, DefaultLimits())
	if err != nil {
		t.Fatalf("Decode(%s): %v", name, err)
	}
	return tab
}

func gzipBytes(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeGzipMatchesPlain(t *testing.T) {
	for _, name := range []string{"sales_summary.tsv", "subscription_event.tsv", "finance_financial.tsv"} {
		t.Run(name, func(t *testing.T) {
			plain, gz := openFixture(t, name), openFixture(t, name+".gz")
			if len(plain.Rows) == 0 {
				t.Fatal("no rows decoded")
			}
			if !equalTables(plain, gz) {
				t.Fatal("gzip and plain decodes differ")
			}
		})
	}
}

func equalTables(a, b *Table) bool {
	if strings.Join(a.Columns, "\t") != strings.Join(b.Columns, "\t") || len(a.Rows) != len(b.Rows) {
		return false
	}
	for i := range a.Rows {
		if strings.Join(a.Rows[i], "\t") != strings.Join(b.Rows[i], "\t") {
			return false
		}
	}
	return len(a.Footer) == len(b.Footer)
}

func TestDecodeShapes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		cols     int
		rows     int
		footer   map[string]string
		lookup   string
		wantCell string
	}{
		{"tsv", "A\tB\n1\t2\n", 2, 1, nil, " b ", "2"},
		{"csv quoted", "Name,Territory\nx,\"Korea, Republic of\"\n", 2, 1, nil, "territory", "Korea, Republic of"},
		{"bom crlf", "\ufeffUnits\tSKU\r\n3\tabc\r\n", 2, 1, nil, "sku", "abc"},
		{"short row padded", "A\tB\tC\n1\n", 3, 1, nil, "c", ""},
		{"blank lines skipped", "A\tB\n\n1\t2\n\n", 2, 1, nil, "a", "1"},
		{"underscore header", "Product_Type Identifier\tUnits\nIA1\t1\n", 2, 1, nil, "product type identifier", "IA1"},
		{
			"finance footer", "Quantity\tTitle\n1\tx\n\nTotal_Rows\t\t1\nTotal_Amount\t2.10\n", 2, 1,
			map[string]string{"Total_Rows": "1", "Total_Amount": "2.10"},
			"title", "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab, err := Decode(strings.NewReader(tt.in), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tab.Columns) != tt.cols || len(tab.Rows) != tt.rows {
				t.Fatalf("got %d cols %d rows, want %d/%d", len(tab.Columns), len(tab.Rows), tt.cols, tt.rows)
			}
			for k, v := range tt.footer {
				if tab.Footer[k] != v {
					t.Errorf("footer %s = %q, want %q", k, tab.Footer[k], v)
				}
			}
			i := tab.Col(tt.lookup)
			if i < 0 || tab.Rows[0][i] != tt.wantCell {
				t.Fatalf("Col(%q) = %d; want cell %q", tt.lookup, i, tt.wantCell)
			}
		})
	}
}

func TestDecodeFinanceFixtureFooter(t *testing.T) {
	tab := openFixture(t, "finance_financial.tsv.gz")
	if len(tab.Rows) != 5 || tab.Footer["Total_Rows"] != "5" || tab.Footer["Total_Units"] != "15" {
		t.Fatalf("rows=%d footer=%v", len(tab.Rows), tab.Footer)
	}
}

func TestDecodeErrors(t *testing.T) {
	bomb := gzipBytes(t, make([]byte, 4<<20)) // 4 MiB of zeros compresses to ~4 KiB
	tests := []struct {
		name string
		in   []byte
		lim  Limits
		want error
	}{
		{"gzip bomb", bomb, Limits{MaxBytes: 1 << 20}, ErrTooLarge},
		{"plain too large", []byte(strings.Repeat("a", 2048)), Limits{MaxBytes: 1024}, ErrTooLarge},
		{"too many columns", []byte(strings.Repeat("c\t", 10) + "c\n"), Limits{MaxColumns: 10}, ErrTooLarge},
		{"too many rows", []byte("A\n1\n2\n3\n"), Limits{MaxRows: 2}, ErrTooLarge},
		{"truncated gzip", bomb[:len(bomb)/2], Limits{}, ErrCorrupt},
		{"bad gzip header", []byte{0x1f, 0x8b, 0, 0}, Limits{}, ErrCorrupt},
		{"empty", nil, Limits{}, ErrCorrupt},
		{"only blanks", []byte("\n\n\t\n"), Limits{}, ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(bytes.NewReader(tt.in), tt.lim)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodeExactRowLimitOK(t *testing.T) {
	tab, err := Decode(strings.NewReader("A\n1\n2\n"), Limits{MaxRows: 2, MaxColumns: 1})
	if err != nil || len(tab.Rows) != 2 {
		t.Fatalf("tab=%v err=%v", tab, err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, name := range []string{"sales_summary.tsv.gz", "finance_financial.tsv", "analytics_segment.csv"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte("a,\"b\n\"\"c\t"))
	f.Fuzz(func(t *testing.T, b []byte) {
		tab, err := Decode(bytes.NewReader(b), Limits{MaxBytes: 1 << 20, MaxColumns: 64, MaxRows: 1000})
		if err != nil {
			return
		}
		for _, r := range tab.Rows {
			if len(r) != len(tab.Columns) {
				t.Fatalf("row width %d != %d columns", len(r), len(tab.Columns))
			}
		}
		// Typed views must fail cleanly, never panic, on arbitrary tables.
		_, _ = SalesRows(tab)
		_, _ = SubscriptionRows(tab)
		_, _ = SubscriptionEventRows(tab)
		_, _ = SubscriberRows(tab)
		_, _ = FinanceRows(tab)
	})
}
