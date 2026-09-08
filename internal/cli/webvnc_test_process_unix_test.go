//go:build !windows

package cli

import (
	"os/exec"
	"testing"
)

func startPlatformWebVNCTestProcess(t *testing.T, nonce string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "while :; do sleep 1; done", "crabbox-webvnc-test", nonce)
	configureDaemonCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}
