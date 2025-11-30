package reports

import "strings"

// Category groups Apple Product Type Identifiers for sales aggregation.
type Category string

// Product categories.
const (
	CategoryDownload     Category = "download"
	CategoryUpdate       Category = "update"
	CategoryRedownload   Category = "redownload"
	CategoryIAP          Category = "iap"
	CategorySubscription Category = "subscription"
	CategoryOther        Category = "other"
)

// Categorizer maps a Product Type Identifier to a Category.
type Categorizer func(productTypeID string) Category

// defaultCategories follows Apple's Product Type Identifier table for the
// Sales SUMMARY report (iOS, Mac, tvOS, watchOS, bundles, custom apps).
var defaultCategories = map[string]Category{
	"1": CategoryDownload, "1F": CategoryDownload, "1T": CategoryDownload, "F1": CategoryDownload,
	"1E": CategoryDownload, "1EP": CategoryDownload, "1EU": CategoryDownload,
	"1-B": CategoryDownload, "F1-B": CategoryDownload, "1-A": CategoryDownload,
	"7": CategoryUpdate, "7F": CategoryUpdate, "7T": CategoryUpdate, "F7": CategoryUpdate,
	"3": CategoryRedownload, "3F": CategoryRedownload, "3T": CategoryRedownload, "F3": CategoryRedownload,
	"IA1": CategoryIAP, "IA9": CategoryIAP, "IA1-M": CategoryIAP, "IA9-M": CategoryIAP,
	"FI1": CategoryIAP,
	"IAY": CategorySubscription, "IAC": CategorySubscription, "IAY-M": CategorySubscription,
	"IAC-M": CategorySubscription,
}

// DefaultCategory is the built-in Categorizer; unknown identifiers map to
// CategoryOther.
func DefaultCategory(productTypeID string) Category {
	if c, ok := defaultCategories[strings.ToUpper(strings.TrimSpace(productTypeID))]; ok {
		return c
	}
	return CategoryOther
}
