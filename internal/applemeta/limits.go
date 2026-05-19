package applemeta

import (
	"fmt"
	"unicode/utf8"
)

// Field names a length-limited App Store Connect text field.
type Field string

// Length-limited fields. Limits follow App Store Connect Help ("App
// information", "Platform version information", TestFlight and in-app
// purchase pages).
const (
	FieldName             Field = "name"
	FieldSubtitle         Field = "subtitle"
	FieldKeywords         Field = "keywords"
	FieldPromotionalText  Field = "promotionalText"
	FieldDescription      Field = "description"
	FieldWhatsNew         Field = "whatsNew"
	FieldReviewNotes      Field = "reviewNotes"
	FieldWhatToTest       Field = "whatsNewBeta"
	FieldBetaDescription  Field = "betaDescription"
	FieldIAPReferenceName Field = "iapReferenceName"
	FieldIAPDisplayName   Field = "iapDisplayName"
	FieldIAPDescription   Field = "iapDescription"
	FieldReviewResponse   Field = "reviewResponse"
)

type limit struct {
	max   int
	bytes bool
}

var limits = map[Field]limit{
	FieldName:             {max: 30},
	FieldSubtitle:         {max: 30},
	FieldKeywords:         {max: 100, bytes: true},
	FieldPromotionalText:  {max: 170},
	FieldDescription:      {max: 4000},
	FieldWhatsNew:         {max: 4000},
	FieldReviewNotes:      {max: 4000},
	FieldWhatToTest:       {max: 4000},
	FieldBetaDescription:  {max: 4000},
	FieldIAPReferenceName: {max: 64},
	FieldIAPDisplayName:   {max: 30},
	FieldIAPDescription:   {max: 45},
	FieldReviewResponse:   {max: 5970},
}

// LimitError reports a value longer than Apple allows.
type LimitError struct {
	Field Field  `json:"field"`
	Max   int    `json:"max"`
	Got   int    `json:"got"`
	Unit  string `json:"unit"` // "bytes" or "characters"
}

// Error implements error.
func (e *LimitError) Error() string {
	return fmt.Sprintf("%s is %d %s; App Store Connect allows at most %d", e.Field, e.Got, e.Unit, e.Max)
}

// Limit returns the maximum length of f and whether it is counted in UTF-8
// bytes (keywords) rather than characters.
func Limit(f Field) (maxLen int, bytes, ok bool) {
	l, ok := limits[f]
	return l.max, l.bytes, ok
}

// Length measures value the way App Store Connect does for f: UTF-8 bytes for
// keywords (commas included), Unicode characters otherwise.
func Length(f Field, value string) int {
	if limits[f].bytes {
		return len(value)
	}
	return utf8.RuneCountInString(value)
}

// Check returns a *LimitError when value exceeds the limit for f, and a plain
// error for an unknown field.
func Check(f Field, value string) error {
	l, ok := limits[f]
	if !ok {
		return fmt.Errorf("applemeta: no length limit known for field %q", f)
	}
	got := Length(f, value)
	if got <= l.max {
		return nil
	}
	unit := "characters"
	if l.bytes {
		unit = "bytes"
	}
	return &LimitError{Field: f, Max: l.max, Got: got, Unit: unit}
}
