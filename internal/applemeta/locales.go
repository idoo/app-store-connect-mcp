package applemeta

import (
	"fmt"
	"slices"
	"strings"
)

// locales is every locale code App Store Connect accepts for app info and
// version localizations. The spec carries no enum for it, so this follows
// Apple's "App Store localizations" reference, including the 2025 additions
// (Bangla … Urdu, Slovenian). fastlane deliver uses the same directory names.
var locales = []string{
	"ar-SA", "bn-BD", "ca", "cs", "da", "de-DE", "el", "en-AU", "en-CA", "en-GB", "en-US",
	"es-ES", "es-MX", "fi", "fr-CA", "fr-FR", "gu-IN", "he", "hi", "hr", "hu", "id", "it",
	"ja", "kn-IN", "ko", "ml-IN", "mr-IN", "ms", "nl-NL", "no", "or-IN", "pa-IN", "pl",
	"pt-BR", "pt-PT", "ro", "ru", "sk", "sl-SI", "sv", "ta-IN", "te-IN", "th", "tr", "uk",
	"ur-PK", "vi", "zh-Hans", "zh-Hant",
}

var localeByLower = func() map[string]string {
	m := make(map[string]string, len(locales))
	for _, l := range locales {
		m[strings.ToLower(l)] = l
	}
	return m
}()

// localeAliases maps lowercased, hyphenated inputs to ASC codes. Language-only
// codes not listed here fall back to the unique ASC code for that language.
var localeAliases = map[string]string{
	"ar": "ar-SA", "de": "de-DE", "en": "en-US", "es": "es-ES", "fr": "fr-FR", "nl": "nl-NL", "pt": "pt-BR",
	"iw": "he", "in": "id", "sl": "sl-SI",
	"en-nz": "en-AU", "en-ie": "en-GB", "en-za": "en-GB", "en-in": "en-GB", "en-sg": "en-GB",
	"en-uk": "en-GB", "es-419": "es-MX", "es-us": "es-MX", "pt-pt": "pt-PT", "fr-ca": "fr-CA",
	"zh": "zh-Hans", "zh-cn": "zh-Hans", "zh-sg": "zh-Hans", "zh-hans-cn": "zh-Hans", "zh-chs": "zh-Hans",
	"zh-tw": "zh-Hant", "zh-hk": "zh-Hant", "zh-mo": "zh-Hant", "zh-hant-tw": "zh-Hant", "zh-hant-hk": "zh-Hant", "zh-cht": "zh-Hant",
	"nb": "no", "nb-no": "no", "nn": "no", "nn-no": "no", "no-no": "no",
	// English names (and the labels App Store Connect shows).
	"arabic": "ar-SA", "bangla": "bn-BD", "bengali": "bn-BD", "catalan": "ca", "czech": "cs", "danish": "da",
	"german": "de-DE", "greek": "el", "english": "en-US", "english (u.s.)": "en-US", "english (us)": "en-US",
	"american english": "en-US", "english (australia)": "en-AU", "australian english": "en-AU",
	"english (canada)": "en-CA", "canadian english": "en-CA", "english (u.k.)": "en-GB", "english (uk)": "en-GB",
	"british english": "en-GB", "spanish": "es-ES", "spanish (spain)": "es-ES", "spanish (mexico)": "es-MX",
	"mexican spanish": "es-MX", "latin american spanish": "es-MX", "finnish": "fi", "french": "fr-FR",
	"french (france)": "fr-FR", "french (canada)": "fr-CA", "canadian french": "fr-CA", "gujarati": "gu-IN",
	"hebrew": "he", "hindi": "hi", "croatian": "hr", "hungarian": "hu", "indonesian": "id", "italian": "it",
	"japanese": "ja", "kannada": "kn-IN", "korean": "ko", "malayalam": "ml-IN", "marathi": "mr-IN", "malay": "ms",
	"dutch": "nl-NL", "norwegian": "no", "norwegian bokmål": "no", "norwegian bokmal": "no", "bokmål": "no",
	"odia": "or-IN", "oriya": "or-IN", "punjabi": "pa-IN", "polish": "pl", "portuguese": "pt-BR",
	"portuguese (brazil)": "pt-BR", "brazilian portuguese": "pt-BR", "portuguese (portugal)": "pt-PT",
	"european portuguese": "pt-PT", "romanian": "ro", "russian": "ru", "slovak": "sk", "slovenian": "sl-SI",
	"swedish": "sv", "tamil": "ta-IN", "telugu": "te-IN", "thai": "th", "turkish": "tr", "ukrainian": "uk",
	"urdu": "ur-PK", "vietnamese": "vi", "chinese": "zh-Hans", "simplified chinese": "zh-Hans",
	"chinese (simplified)": "zh-Hans", "chinese, simplified": "zh-Hans", "traditional chinese": "zh-Hant",
	"chinese (traditional)": "zh-Hant", "chinese, traditional": "zh-Hant",
}

// byLanguage maps a bare language subtag to its ASC code when exactly one ASC
// code uses that language (e.g. "ja" → "ja", "sv" → "sv", "ta" → "ta-IN").
var byLanguage = func() map[string]string {
	count := map[string]int{}
	first := map[string]string{}
	for _, l := range locales {
		lang, _, _ := strings.Cut(strings.ToLower(l), "-")
		count[lang]++
		first[lang] = l
	}
	m := map[string]string{}
	for lang, n := range count {
		if n == 1 {
			m[lang] = first[lang]
		}
	}
	return m
}()

// LocaleError reports an input that is not an ASC locale.
type LocaleError struct {
	Input string   `json:"input"`
	Valid []string `json:"valid"`
}

// Error implements error.
func (e *LocaleError) Error() string {
	return fmt.Sprintf("unknown locale %q; valid ASC locales: %s", e.Input, strings.Join(e.Valid, ", "))
}

// Locales returns every ASC locale code, sorted.
func Locales() []string { return slices.Clone(locales) }

// IsLocale reports whether s is exactly an ASC locale code.
func IsLocale(s string) bool {
	return slices.Contains(locales, s)
}

// NormalizeLocale maps a locale code, alias, BCP-47 variant ("ja-JP",
// "pt_BR", "zh-CN"), fastlane directory name or English language name
// ("German", case-insensitive) to the ASC code. Unknown input returns a
// *LocaleError listing the valid codes.
func NormalizeLocale(in string) (string, error) {
	key := strings.ToLower(strings.Join(strings.Fields(in), " "))
	key = strings.ReplaceAll(key, "_", "-")
	if l, ok := localeByLower[key]; ok {
		return l, nil
	}
	if l, ok := localeAliases[key]; ok {
		return l, nil
	}
	if lang, region, ok := strings.Cut(key, "-"); ok {
		if strings.ContainsAny(region, "- ") || len(region) < 2 || len(region) > 4 {
			return "", &LocaleError{Input: in, Valid: Locales()}
		}
		if l, ok := byLanguage[lang]; ok {
			return l, nil
		}
		if lang == "es" {
			return "es-MX", nil // Latin American Spanish variants
		}
	} else if l, ok := byLanguage[key]; ok {
		return l, nil
	}
	return "", &LocaleError{Input: in, Valid: Locales()}
}

// sameLocale lists lowercased regional inputs that name exactly the ASC
// locale they normalize to (the region adds nothing), so mapping them is not
// a fold.
var sameLocale = map[string]bool{
	"ja-jp": true, "ko-kr": true, "sv-se": true, "it-it": true, "fi-fi": true, "da-dk": true, "cs-cz": true,
	"el-gr": true, "he-il": true, "hi-in": true, "hr-hr": true, "hu-hu": true, "id-id": true, "ms-my": true,
	"pl-pl": true, "ro-ro": true, "ru-ru": true, "sk-sk": true, "th-th": true, "tr-tr": true, "uk-ua": true,
	"vi-vn": true, "ca-es": true, "nb-no": true, "no-no": true, "en-uk": true, "es-419": true,
	"zh-cn": true, "zh-hans-cn": true, "zh-tw": true, "zh-hant-tw": true,
}

// LocaleFolded reports whether NormalizeLocale(in) == out folded a regional
// variant into a different listing locale (de-CH → de-DE, en-IN → en-GB,
// nn → no), as opposed to a case, "_" or alias spelling change of the same
// locale. Folded inputs silently overwrite the target listing, so callers
// should warn about them.
func LocaleFolded(in, out string) bool {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(in)), "_", "-")
	if key == strings.ToLower(out) || sameLocale[key] {
		return false
	}
	if key == "nn" {
		return true
	}
	return strings.Contains(key, "-") && !strings.Contains(key, " ")
}

// TerritoryLanguage maps ASC territory codes (ISO 3166-1 alpha-3, as in
// customer reviews) to the ASC locale a reply most likely should use. It is a
// hint, not a rule: multilingual territories map to the majority language.
var TerritoryLanguage = map[string]string{
	"USA": "en-US", "GBR": "en-GB", "IRL": "en-GB", "AUS": "en-AU", "NZL": "en-AU", "CAN": "en-CA",
	"IND": "hi", "SGP": "en-GB", "ZAF": "en-GB", "NGA": "en-GB", "KEN": "en-GB", "PHL": "en-US",
	"DEU": "de-DE", "AUT": "de-DE", "CHE": "de-DE", "LIE": "de-DE", "LUX": "fr-FR",
	"FRA": "fr-FR", "BEL": "nl-NL", "MCO": "fr-FR", "SEN": "fr-FR", "CIV": "fr-FR",
	"ESP": "es-ES", "MEX": "es-MX", "ARG": "es-MX", "COL": "es-MX", "CHL": "es-MX", "PER": "es-MX",
	"VEN": "es-MX", "ECU": "es-MX", "GTM": "es-MX", "BOL": "es-MX", "URY": "es-MX", "PRY": "es-MX",
	"CRI": "es-MX", "PAN": "es-MX", "DOM": "es-MX", "SLV": "es-MX", "HND": "es-MX", "NIC": "es-MX",
	"BRA": "pt-BR", "PRT": "pt-PT", "AGO": "pt-PT", "MOZ": "pt-PT",
	"ITA": "it", "NLD": "nl-NL", "SWE": "sv", "NOR": "no", "DNK": "da", "FIN": "fi",
	"POL": "pl", "CZE": "cs", "SVK": "sk", "SVN": "sl-SI", "HRV": "hr", "HUN": "hu", "ROU": "ro",
	"MDA": "ro", "GRC": "el", "CYP": "el", "TUR": "tr", "RUS": "ru", "BLR": "ru", "KAZ": "ru",
	"UKR": "uk", "ISR": "he", "SAU": "ar-SA", "ARE": "ar-SA", "EGY": "ar-SA", "QAT": "ar-SA",
	"KWT": "ar-SA", "BHR": "ar-SA", "OMN": "ar-SA", "JOR": "ar-SA", "LBN": "ar-SA", "MAR": "fr-FR",
	"DZA": "ar-SA", "TUN": "ar-SA", "IRQ": "ar-SA", "JPN": "ja", "KOR": "ko", "CHN": "zh-Hans",
	"TWN": "zh-Hant", "HKG": "zh-Hant", "MAC": "zh-Hant", "THA": "th", "VNM": "vi", "IDN": "id",
	"MYS": "ms", "BGD": "bn-BD", "PAK": "ur-PK",
}

// LocaleForTerritory returns the likely reply locale for an alpha-3 territory
// code (case-insensitive).
func LocaleForTerritory(territory string) (string, bool) {
	l, ok := TerritoryLanguage[strings.ToUpper(strings.TrimSpace(territory))]
	return l, ok
}
