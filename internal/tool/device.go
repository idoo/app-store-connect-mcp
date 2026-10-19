package tool

import "regexp"

// DeviceKind names a device field shape checked by [DeviceField].
type DeviceKind int

// Device field kinds.
const (
	// DeviceModel is a hardware identifier such as iPhone17,1.
	DeviceModel DeviceKind = iota + 1
	// OSVersion is a version such as 18.1, 17.4.1 or 26.0 (23A341).
	OSVersion
	// Locale is a BCP-47-like locale such as en, en-US or zh_Hans_CN.
	Locale
)

var deviceShapes = map[DeviceKind]*regexp.Regexp{
	DeviceModel: regexp.MustCompile(`^[A-Za-z]{2,12}[0-9]{1,3},[0-9]{1,3}$`),
	OSVersion:   regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){0,3}( \([0-9A-Za-z]{1,10}\))?$`),
	Locale:      regexp.MustCompile(`^[a-z]{2,3}([-_][A-Za-z0-9]{2,8}){0,2}$`),
}

// DeviceField keeps third-party device metadata only when it has the
// field's shape, so free text (e.g. instructions typed into a device model)
// never reaches the agent. It returns s and false when s matches, and "" and
// true when a non-empty s was dropped (an unknown kind drops every non-empty
// value). An empty s returns "" and false.
func DeviceField(kind DeviceKind, s string) (clean string, dropped bool) {
	if s == "" {
		return "", false
	}
	if re := deviceShapes[kind]; re != nil && re.MatchString(s) {
		return s, false
	}
	return "", true
}
