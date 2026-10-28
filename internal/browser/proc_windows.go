//go:build windows

package browser

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// profilePIDs lists processes whose command line carries this
// --user-data-dir in any of the forms of profileArgForms.
func profilePIDs(profile string) []int {
	return queryPIDs(profile, "")
}

// queryPIDs runs the Win32_Process query for profile, narrowed by the
// optional PowerShell filter (a Where-Object block body over $_). The
// patterns travel in environment variables (UTF-16, so non-ASCII paths
// survive), never in the PowerShell source.
func queryPIDs(profile, filter string) []int {
	script := `$pats = @($env:ASC_MCP_ARG0, $env:ASC_MCP_ARG1, $env:ASC_MCP_ARG2); $suf = $env:ASC_MCP_ARGEND; ` +
		`Get-CimInstance Win32_Process | Where-Object { $c = $_.CommandLine; $c -and ($c.EndsWith($suf) -or @($pats | Where-Object { $c.Contains($_) }).Count -gt 0) }`
	if filter != "" {
		script += ` | Where-Object { ` + filter + ` }`
	}
	script += ` | ForEach-Object { $_.ProcessId }`
	contains, suffix := profileArgForms(profile)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script) //nolint:gosec // fixed program; script is built from constants, the path travels in env
	cmd.Env = append(os.Environ(),
		"ASC_MCP_ARG0="+contains[0], "ASC_MCP_ARG1="+contains[1], "ASC_MCP_ARG2="+contains[2],
		"ASC_MCP_ARGEND="+suffix)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil && n != os.Getpid() {
			pids = append(pids, n)
		}
	}
	return pids
}

// waitProfileExit waits up to d for every process of the profile to exit.
func waitProfileExit(profile string, d time.Duration) bool {
	for end := time.Now().Add(d); ; {
		if len(profilePIDs(profile)) == 0 {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// killProfile terminates every process using profile and waits up to 3 s
// for them to exit, so the caller can delete the profile. Windows has no
// SIGTERM; processes are killed outright.
func killProfile(profile string) {
	pids := profilePIDs(profile)
	if len(pids) == 0 {
		return
	}
	for _, p := range pids {
		if pr, err := os.FindProcess(p); err == nil {
			_ = pr.Kill()
		}
	}
	waitProfileExit(profile, 3*time.Second)
}

// orphanFilter keeps headless browsers whose parent process is gone: a
// renew left behind by a server the MCP client terminated. Helpers are
// children of the browser itself, so they are never selected; a live renew
// or an interactive window has a running parent.
const orphanFilter = `$_.CommandLine.Contains('--headless') -and -not (Get-Process -Id $_.ParentProcessId -ErrorAction SilentlyContinue)`

// killOrphans kills headless browsers of profile whose parent has died.
func killOrphans(profile string) {
	pids := queryPIDs(profile, orphanFilter)
	for _, p := range pids {
		if pr, err := os.FindProcess(p); err == nil {
			_ = pr.Kill()
		}
	}
	if len(pids) > 0 {
		waitProfileExit(profile, 3*time.Second)
	}
}
