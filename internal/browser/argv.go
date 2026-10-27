package browser

import "strings"

// profileArgForms lists how a Windows command line can carry
// --user-data-dir=<profile>: contains holds substrings that match anywhere,
// suffix matches an unquoted last argument. Go's exec quotes the whole
// argument when the path has a space ("--user-data-dir=C:\A B\p"), while
// Chromium writes its children as --user-data-dir="C:\A B\p".
func profileArgForms(profile string) (contains []string, suffix string) {
	arg := "--user-data-dir=" + profile
	return []string{
		arg + " ",
		`--user-data-dir="` + profile + `"`,
		`"` + arg + `"`,
	}, arg
}

// cmdlineHasProfile applies profileArgForms to one command line, the same
// test the PowerShell filter in proc_windows.go runs.
func cmdlineHasProfile(cmdline, profile string) bool {
	contains, suffix := profileArgForms(profile)
	for _, c := range contains {
		if strings.Contains(cmdline, c) {
			return true
		}
	}
	return strings.HasSuffix(cmdline, suffix)
}
