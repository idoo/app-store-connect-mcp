//go:build unix

package browser

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// profilePIDs lists processes started with exactly this --user-data-dir.
func profilePIDs(profile string) []int {
	pat := "--user-data-dir=" + regexp.QuoteMeta(profile) + "( |$)"
	out, err := exec.Command("pgrep", "-f", "--", pat).Output() //nolint:gosec // fixed program; pat is a regexp-quoted path after "--"
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
		time.Sleep(100 * time.Millisecond)
	}
}

// killProfile terminates every process using profile: SIGTERM, then SIGKILL
// after 3 s. Used for browsers we don't own the pid of (LaunchServices
// launches, survivors of a killed server, logout).
func killProfile(profile string) {
	pids := profilePIDs(profile)
	if len(pids) == 0 {
		return
	}
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	if waitProfileExit(profile, 3*time.Second) {
		return
	}
	for _, p := range profilePIDs(profile) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

// killOrphans kills headless browsers of profile whose parent has died
// (re-parented to pid 1): a renew left behind by a SIGKILLed server. A live
// renew or an interactive window is never touched.
func killOrphans(profile string) {
	for _, p := range profilePIDs(profile) {
		out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(p)).Output() //nolint:gosec // fixed program, numeric pid
		if err != nil {
			continue
		}
		ppid, cmdline, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
		if strings.TrimSpace(ppid) == "1" && strings.Contains(cmdline, "--headless") {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}
}
