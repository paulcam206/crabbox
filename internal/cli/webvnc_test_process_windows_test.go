//go:build windows

package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const windowsWebVNCLongRunningHelperEnv = "CRABBOX_WINDOWS_WEBVNC_LONG_RUNNING_HELPER"

// windowsWebVNCLongRunningHelperLifetime bounds an orphaned helper if a test
// crashes before its cleanup terminates the process tree.
const windowsWebVNCLongRunningHelperLifetime = 5 * time.Minute

func startPlatformWebVNCTestProcess(t *testing.T, nonce string) *exec.Cmd {
	t.Helper()
	// Windows has no POSIX shell to hold a process open, and the nonce has to
	// stay on the command line so webVNCDaemonProcessCommand can observe it.
	// "-test.timeout=0" keeps the helper's own deadline from racing its parent.
	cmd := exec.Command(
		os.Args[0],
		"-test.run=^TestWindowsWebVNCLongRunningHelper$",
		"-test.timeout=0",
		"--",
		"crabbox-webvnc-test",
		nonce,
	)
	cmd.Env = append(os.Environ(), windowsWebVNCLongRunningHelperEnv+"=1")
	configureDaemonCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestWindowsWebVNCLongRunningHelper(t *testing.T) {
	if os.Getenv(windowsWebVNCLongRunningHelperEnv) != "1" {
		return
	}
	time.Sleep(windowsWebVNCLongRunningHelperLifetime)
}

func TestWindowsWebVNCDaemonSurvivorsExcludeExitedProcessWithOpenHandle(t *testing.T) {
	cmd := startTestWebVNCDaemonProcess(t, strings.Repeat("d", 32))
	started, err := webVNCDaemonProcessStartIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	tree := []windowsWebVNCDaemonProcessIdentity{{pid: cmd.Process.Pid, started: started}}
	if survivors := windowsWebVNCDaemonSurvivors(tree); len(survivors) != 1 {
		t.Fatalf("running WebVNC daemon survivors=%v want the tracked pid", survivors)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Deliberately skip Wait here: os.Process keeps the Windows process handle
	// open, so OpenProcess and GetProcessTimes keep reporting the recorded start
	// identity for the corpse. Survivor detection must still see it exit.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if len(windowsWebVNCDaemonSurvivors(tree)) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("exited WebVNC daemon pid %d is still reported as a survivor", cmd.Process.Pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
