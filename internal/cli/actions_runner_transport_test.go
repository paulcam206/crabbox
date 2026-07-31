package cli

import (
	"strings"
	"testing"
)

// The Windows runner install script is well over the stdin threshold, so it
// must travel as a file. Streaming it strands setup before the guest emits its
// first stage, with no diagnostics to read.
func TestWindowsActionsRunnerInstallScriptExceedsStdinThreshold(t *testing.T) {
	script := githubActionsRunnerInstallScriptForTarget(
		"",
		true,
		SSHTarget{TargetOS: targetWindows, WindowsMode: windowsModeNormal},
	)
	if !windowsScriptNeedsFileTransport(script) {
		t.Fatalf("windows runner install script (%d bytes) must use file transport", len(script))
	}
}

func TestWindowsActionsRunnerUploadedCommandKeepsCredentialsOnStdin(t *testing.T) {
	remote := decodePowerShellCommand(t, githubActionsRunnerInstallRemoteCommandForUploadedScript(`C:\ProgramData\crabbox\install-runner-abc.ps1`))
	for _, want := range []string{
		"RUNNER_REPO",
		"RUNNER_NAME",
		"RUNNER_LABELS",
		"RUNNER_TOKEN",
		"Read-CrabboxRunnerValue",
		"install-runner-abc.ps1",
		"Remove-Item",
	} {
		if !strings.Contains(remote, want) {
			t.Fatalf("uploaded-script remote command missing %q", want)
		}
	}
	// The script is already on disk; nothing should still be read from stdin
	// beyond the four credential lines.
	if strings.Contains(remote, "ReadToEnd") {
		t.Fatalf("uploaded-script remote command must not stream the script over stdin: %s", remote)
	}
}

func TestActionsRunnerCredentialInputCarriesNoScript(t *testing.T) {
	input := githubActionsRunnerCredentialInput("example-org/my-app", "runner-1", "self-hosted,windows", "tok")
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("credential input must be exactly four lines, got %d: %q", len(lines), input)
	}
	if len(input) > windowsInlineScriptLimit {
		t.Fatalf("credential input (%d bytes) must stay under the stdin threshold", len(input))
	}
}

// The uploaded file must never carry the short-lived registration token: it is
// supplied through stdin into an environment variable instead.
func TestWindowsActionsRunnerInstallScriptOmitsToken(t *testing.T) {
	script := githubActionsRunnerInstallScriptForTarget(
		"",
		true,
		SSHTarget{TargetOS: targetWindows, WindowsMode: windowsModeNormal},
	)
	if !strings.Contains(script, "$env:RUNNER_TOKEN") {
		t.Fatal("install script should reference the token through the environment")
	}
	for _, forbidden := range []string{"ghs_", "ghp_", "github_pat_"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("install script must not embed a credential (%s)", forbidden)
		}
	}
}
