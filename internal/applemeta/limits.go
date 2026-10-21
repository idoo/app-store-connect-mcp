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

var limits = map[Field]int{
	FieldName:             30,
	FieldSubtitle:         30,
	FieldKeywords:         100,
	FieldPromotionalText:  170,
	FieldDescription:      4000,
	FieldWhatsNew:         4000,
	FieldReviewNotes:      4000,
	FieldWhatToTest:       4000,
	FieldBetaDescription:  4000,
	FieldIAPReferenceName: 64,
	FieldIAPDisplayName:   30,
	FieldIAPDescription:   45,
	FieldReviewResponse:   5970,
}

// LimitError reports a value longer than Apple allows.
type LimitError struct {
	Field Field  `json:"field"`
	Max   int    `json:"max"`
	Got   int    `json:"got"`
	Unit  string `json:"unit"` // always "characters"
}

// Error implements error.
func (e *LimitError) Error() string {
	return fmt.Sprintf("%s is %d %s; App Store Connect allows at most %d", e.Field, e.Got, e.Unit, e.Max)
}

// Length measures value the way App Store Connect does: Unicode characters
// (keywords included, commas counted).
func Length(_ Field, value string) int {
	return utf8.RuneCountInString(value)
}

// Check returns a *LimitError when value exceeds the limit for f, and a plain
// error for an unknown field.
func Check(f Field, value string) error {
	maxLen, ok := limits[f]
	if !ok {
		return fmt.Errorf("applemeta: no length limit known for field %q", f)
	}
	got := Length(f, value)
	if got <= maxLen {
		return nil
	}
	return &LimitError{Field: f, Max: maxLen, Got: got, Unit: "characters"}
}
