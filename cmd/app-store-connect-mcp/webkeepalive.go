package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// Test hooks for `web keepalive`.
var (
	webKeepalive = app.WebKeepalive
	// launchctl runs launchctl with args (darwin only).
	launchctl = func(args ...string) error {
		out, err := exec.Command("launchctl", args...).CombinedOutput() //nolint:gosec // fixed program; args are ours
		if err != nil {
			return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	keepaliveGOOS       = runtime.GOOS
	keepaliveExecutable = os.Executable
	keepaliveHome       = os.UserHomeDir
)

// keepaliveEnv are passed on to the scheduled job: they pick the session
// store and the state and config dirs.
var keepaliveEnv = []string{websession.EnvStore, "XDG_STATE_HOME", "XDG_CONFIG_HOME"}

// keepaliveLabelPrefix is the reverse-DNS launchd label; the profile follows.
const keepaliveLabelPrefix = "io.github.idoo.app-store-connect-mcp.keepalive."

func webKeepaliveCmd(ctx context.Context, o webOptions, profile, stateDir string, stdout io.Writer) error {
	if o.install || o.uninstall {
		return keepaliveSchedule(o, profile, stateDir, stdout)
	}
	now := webNow().Format(time.RFC3339)
	apple, err := webKeepalive(ctx, stateDir, profile)
	switch {
	case errors.Is(err, app.ErrKeepaliveWindowOpen):
		fmt.Fprintf(stdout, "%s web session (profile %s): expired; sign-in window open, skipping\n", now, profile)
		return &guardExit{1}
	case errors.Is(err, app.ErrKeepaliveSignIn):
		fmt.Fprintf(stdout, "%s web session (profile %s): expired; run `app-store-connect-mcp web login`\n", now, profile)
		return &guardExit{1}
	case err != nil:
		fmt.Fprintf(stdout, "%s web session (profile %s): not checked: %v\n", now, profile, err)
		return &guardExit{1}
	}
	_, err = fmt.Fprintf(stdout, "%s web session (profile %s): live as %s\n", now, profile, apple)
	return err
}

// keepaliveJob is one scheduled keepalive.
type keepaliveJob struct {
	label, bin, profile, log string
	interval                 time.Duration
	env                      map[string]string
}

// keepaliveSchedule installs or removes the background keepalive: a
// LaunchAgent on macOS, a printed cron line elsewhere. Both are idempotent.
func keepaliveSchedule(o webOptions, profile, stateDir string, stdout io.Writer) error {
	bin, err := keepaliveBinary()
	if err != nil {
		return err
	}
	j := keepaliveJob{
		label:    keepaliveLabelPrefix + launchdSafe(profile),
		bin:      bin,
		profile:  profile,
		log:      filepath.Join(stateDir, "web", "keepalive", launchdSafe(profile)+".log"),
		interval: o.interval,
		env:      map[string]string{},
	}
	for _, k := range keepaliveEnv {
		if v := os.Getenv(k); v != "" {
			j.env[k] = v
		}
	}
	if keepaliveGOOS != "darwin" {
		if o.uninstall {
			_, err := fmt.Fprintf(stdout, "Remove the line running `web keepalive --profile %s` from `crontab -e`.\n", profile)
			return err
		}
		_, err := fmt.Fprintf(stdout, "Add this line with `crontab -e`:\n%s\n", j.cronLine())
		return err
	}
	home, err := keepaliveHome()
	if err != nil {
		return err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", j.label+".plist")
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = launchctl("bootout", domain+"/"+j.label) // not loaded is fine
	if o.uninstall {
		if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err := fmt.Fprintf(stdout, "Background keepalive for profile %s removed.\n", profile)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.log), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(j.plist()), 0o600); err != nil {
		return err
	}
	if err := launchctl("bootstrap", domain, plist); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Background keepalive for profile %s runs every %s (%s); log: %s\n", profile, j.interval, j.label, j.log)
	return err
}

// keepaliveBinary is this executable's path; a Homebrew Cellar path is
// replaced by the stable bin symlink so upgrades keep the job working.
func keepaliveBinary() (string, error) {
	bin, err := keepaliveExecutable()
	if err != nil {
		return "", err
	}
	if i := strings.Index(bin, "/Cellar/"); i > 0 {
		stable := bin[:i] + "/bin/" + filepath.Base(bin)
		if _, err := os.Stat(stable); err == nil {
			return stable, nil
		}
	}
	return bin, nil
}

// launchdSafe keeps letters, digits, '.', '_' and '-'.
func launchdSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, s)
}

func (j keepaliveJob) plist() string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + esc(j.label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(j.bin) + `</string>
		<string>web</string>
		<string>keepalive</string>
		<string>--profile</string>
		<string>` + esc(j.profile) + `</string>
	</array>
	<key>StartInterval</key>
	<integer>` + strconv.Itoa(int(j.interval.Seconds())) + `</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + esc(j.log) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(j.log) + `</string>
`)
	if len(j.env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range keepaliveEnv {
			if v, ok := j.env[k]; ok {
				b.WriteString("\t\t<key>" + esc(k) + "</key>\n\t\t<string>" + esc(v) + "</string>\n")
			}
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// cronLine is the crontab entry equivalent to the LaunchAgent.
func (j keepaliveJob) cronLine() string {
	sched := "*/" + strconv.Itoa(max(1, int(j.interval.Minutes()))) + " * * * *"
	if j.interval >= time.Hour {
		sched = "0 */" + strconv.Itoa(int(j.interval.Hours())) + " * * *"
	}
	var env strings.Builder
	for _, k := range keepaliveEnv {
		if v, ok := j.env[k]; ok {
			env.WriteString(k + "=" + shellQuote(v) + " ")
		}
	}
	return sched + " " + env.String() + shellQuote(j.bin) + " web keepalive --profile " + shellQuote(j.profile) + " >> " + shellQuote(j.log) + " 2>&1"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
