package cli

import (
	"strings"
	"testing"
)

func TestWindowsScriptTransportSelection(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		wantFile bool
	}{
		{name: "empty", script: "", wantFile: false},
		{name: "small", script: strings.Repeat("a", 128), wantFile: false},
		{name: "at limit", script: strings.Repeat("a", windowsInlineScriptLimit), wantFile: false},
		{name: "over limit", script: strings.Repeat("a", windowsInlineScriptLimit+1), wantFile: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsScriptNeedsFileTransport(tc.script); got != tc.wantFile {
				t.Fatalf("windowsScriptNeedsFileTransport(len=%d)=%v want %v", len(tc.script), got, tc.wantFile)
			}
		})
	}
}

// The desktop launch payload embeds the launcher service source, so it must
// never be pushed through SSH stdin: Windows OpenSSH intermittently wedges its
// connection while forwarding a payload that large.
func TestWindowsDesktopLaunchPayloadUsesFileTransport(t *testing.T) {
	script := windowsDesktopLaunchPowerShell(
		`C:\crabbox\cbx_test\repo`,
		map[string]string{"CRABBOX_TEST": "1"},
		[]string{`C:\Program Files\Git\usr\bin\mintty.exe`, "-t", "Crabbox Desktop", "/usr/bin/bash", "-lc", "echo hi"},
	)
	if !windowsScriptNeedsFileTransport(script) {
		t.Fatalf("desktop launch payload (%d bytes) must use file transport", len(script))
	}
}
