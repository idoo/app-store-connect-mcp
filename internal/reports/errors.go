package reports

import "errors"

// ErrTooLarge reports that a report file exceeds a configured decoding limit.
var ErrTooLarge = errors.New("report too large")

// ErrCorrupt reports that a report file is malformed (bad gzip, missing
// header, unparsable required value).
var ErrCorrupt = errors.New("report corrupt")

// Default decoding limits.
const (
	DefaultMaxBytes   int64 = 64 << 20
	DefaultMaxColumns       = 512
	DefaultMaxRows          = 2_000_000
)

// Limits bounds the work Decode performs. Zero fields use the defaults.
type Limits struct {
	// MaxBytes caps the decompressed size in bytes.
	MaxBytes int64
	// MaxColumns caps the number of columns in any row.
	MaxColumns int
	// MaxRows caps the number of data rows (header and footer excluded).
	MaxRows int
}

// DefaultLimits returns the production limits: 64 MiB, 512 columns, 2M rows.
func DefaultLimits() Limits {
	return Limits{MaxBytes: DefaultMaxBytes, MaxColumns: DefaultMaxColumns, MaxRows: DefaultMaxRows}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxColumns <= 0 {
		l.MaxColumns = d.MaxColumns
	}
	if l.MaxRows <= 0 {
		l.MaxRows = d.MaxRows
	}
	return l
}
