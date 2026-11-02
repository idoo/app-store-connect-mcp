package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// keepaliveHooks fakes macOS, launchctl and the binary path; it returns the
// launchctl calls and the fake home.
func keepaliveHooks(t *testing.T, goos string) (*[]string, string) {
	t.Helper()
	webSandbox(t)
	oldGOOS, oldCtl, oldExe, oldHome := keepaliveGOOS, launchctl, keepaliveExecutable, keepaliveHome
	t.Cleanup(func() {
		keepaliveGOOS, launchctl, keepaliveExecutable, keepaliveHome = oldGOOS, oldCtl, oldExe, oldHome
	})
	home := t.TempDir()
	var calls []string
	keepaliveGOOS = goos
	launchctl = func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	keepaliveExecutable = func() (string, error) { return "/usr/local/bin/app-store-connect-mcp", nil }
	keepaliveHome = func() (string, error) { return home, nil }
	return &calls, home
}

func TestKeepaliveInstallWritesLaunchAgent(t *testing.T) {
	calls, home := keepaliveHooks(t, "darwin")
	const label = "io.github.idoo.app-store-connect-mcp.keepalive.my-team"
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	for range 2 { // idempotent
		if _, errOut, err := runCLI(t, "", "web", "keepalive", "--install", "--interval", "15m", "--profile", "my team"); err != nil {
			t.Fatalf("install: %v %s", err, errOut)
		}
	}
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	stateDir, _ := webStateDir()
	got := string(data)
	for _, want := range []string{
		"<string>" + label + "</string>",
		"<string>/usr/local/bin/app-store-connect-mcp</string>\n\t\t<string>web</string>\n\t\t<string>keepalive</string>\n\t\t<string>--profile</string>\n\t\t<string>my team</string>",
		"<key>StartInterval</key>\n\t<integer>900</integer>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<string>" + filepath.Join(stateDir, "web", "keepalive", "my-team.log") + "</string>",
		"<key>ASC_MCP_WEB_STORE</key>\n\t\t<string>file</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist lacks %q:\n%s", want, got)
		}
	}
	// launchd's Background class throttles CPU and network so hard that a run
	// never finished.
	if strings.Contains(got, "ProcessType") {
		t.Errorf("plist sets a ProcessType:\n%s", got)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	wantCalls := []string{"bootout " + domain + "/" + label, "bootstrap " + domain + " " + plist}
	if len(*calls) != 4 || (*calls)[0] != wantCalls[0] || (*calls)[1] != wantCalls[1] || (*calls)[2] != wantCalls[0] || (*calls)[3] != wantCalls[1] {
		t.Fatalf("launchctl calls = %q", *calls)
	}
	if _, _, err := runCLI(t, "", "web", "keepalive", "--uninstall", "--profile", "my team"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) || (*calls)[4] != wantCalls[0] {
		t.Fatalf("after uninstall: %v, calls %q", err, *calls)
	}
}

func TestKeepaliveInstallPrintsCronElsewhere(t *testing.T) {
	calls, _ := keepaliveHooks(t, "linux")
	out, _, err := runCLI(t, "", "web", "keepalive", "--install", "--interval", "2h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 */2 * * * ASC_MCP_WEB_STORE='file' ") || !strings.Contains(out, "'/usr/local/bin/app-store-connect-mcp' web keepalive --profile 'default' >> ") || len(*calls) != 0 {
		t.Fatalf("out %q, launchctl %q", out, *calls)
	}
}
