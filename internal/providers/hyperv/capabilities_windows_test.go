package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	core "github.com/openclaw/crabbox/internal/cli"
)

func TestWindowsCapabilityConfigCarriesLeaseOptions(t *testing.T) {
	cfg := windowsCapabilityConfig(testBackend(&recordingRunner{}).configForRun(), core.LeaseOptions{
		Desktop: true,
		Browser: true,
	})
	if !cfg.Desktop || !cfg.Browser {
		t.Fatalf("capability config desktop=%v browser=%v, want both true", cfg.Desktop, cfg.Browser)
	}
}

func TestWindowsCapabilityLabelsAppearOnlyAfterReadiness(t *testing.T) {
	cfg := testBackend(&recordingRunner{}).configForRun()
	cfg.Desktop = true
	cfg.Browser = true

	labels := provisionalWindowsCapabilityLabels(cfg, "cbx_capability123", "capability", false, time.Unix(1_800_000_000, 0))
	if labels["desktop"] != "" || labels["browser"] != "" {
		t.Fatalf("provisional labels exposed successful capabilities: %#v", labels)
	}

	markWindowsCapabilitiesReady(labels, cfg)
	if labels["desktop"] != "true" || labels["browser"] != "true" {
		t.Fatalf("ready labels missing capabilities: %#v", labels)
	}
}

func TestPersistWindowsActionsRunnerCredentialUsesEnvironmentAndProtectedTransaction(t *testing.T) {
	runner := &recordingRunner{}
	b := testBackend(runner)
	password := b.guestPassword()

	if err := b.persistWindowsActionsRunnerCredential(context.Background(), "crabbox-credential-1234", "crabbox"); err != nil {
		t.Fatalf("persistWindowsActionsRunnerCredential: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("PowerShell calls=%d want 1", len(runner.calls))
	}
	call := runner.calls[0]
	args := strings.Join(call.Args, "\n")
	if strings.Contains(args, password) {
		t.Fatal("guest password appeared on the PowerShell argv")
	}
	if !containsEnv(call.Env, "_CRABBOX_GP="+password) {
		t.Fatal("guest password was not passed through _CRABBOX_GP")
	}
	for _, want := range []string{
		"-ArgumentList $env:_CRABBOX_GP",
		core.WindowsActionsRunnerCredentialPath,
		"[Text.UTF8Encoding]::new($false)",
		"[IO.FileShare]::None",
		"SetAccessRuleProtection($true, $false)",
		"$tempStream.SetAccessControl($acl)",
		"$tempStream.Write($encodedPassword, 0, $encodedPassword.Length)",
		"MoveFileEx($tempPath, $targetPath",
		"S-1-5-32-544",
		"S-1-5-18",
		"$rules.Count -ne $allowedSids.Count",
		"$replaced -and -not $committed",
		"[IO.File]::Delete($targetPath)",
		"finally",
		"[IO.File]::Delete($tempPath)",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("credential persistence command missing %q", want)
		}
	}
	create := strings.Index(args, "[IO.FileStream]::new(")
	harden := strings.Index(args, "$tempStream.SetAccessControl($acl)")
	write := strings.Index(args, "$tempStream.Write($encodedPassword, 0, $encodedPassword.Length)")
	move := strings.Index(args, "MoveFileEx($tempPath, $targetPath")
	if create < 0 || harden <= create || write <= harden || move <= write {
		t.Fatalf("credential transaction order create=%d harden=%d write=%d move=%d", create, harden, write, move)
	}
}

func TestPrepareWindowsGuestNetworkPersistsCredentialBeforeSSHAndNetwork(t *testing.T) {
	runner := &recordingRunner{}
	b := testBackend(runner)
	cfg := b.configForRun()

	if err := b.prepareWindowsGuestNetwork(context.Background(), cfg, "crabbox-order-1234", "ssh-ed25519 AAAATEST crabbox-test"); err != nil {
		t.Fatalf("prepareWindowsGuestNetwork: %v", err)
	}
	joined := make([]string, len(runner.calls))
	for i, call := range runner.calls {
		joined[i] = strings.Join(call.Args, "\n")
	}
	indexOf := func(marker string) int {
		for i, call := range joined {
			if strings.Contains(call, marker) {
				return i
			}
		}
		return -1
	}
	ready := indexOf("ScriptBlock { $true }")
	credential := indexOf(core.WindowsActionsRunnerCredentialPath)
	ssh := indexOf("administrators_authorized_keys")
	network := indexOf("Connect-VMNetworkAdapter")
	if ready < 0 || credential <= ready || ssh <= credential || network <= ssh {
		t.Fatalf("pre-network order ready=%d credential=%d ssh=%d network=%d", ready, credential, ssh, network)
	}
}

func TestAcquireWindowsCredentialPersistenceFailureRespectsKeepPolicy(t *testing.T) {
	oldOS := hypervHostOS
	hypervHostOS = "windows"
	t.Cleanup(func() { hypervHostOS = oldOS })

	for _, keep := range []bool{false, true} {
		t.Run("keep="+strconv.FormatBool(keep), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("USERPROFILE", root)
			t.Setenv("APPDATA", filepath.Join(root, "AppData", "Roaming"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

			credentialCalls := 0
			runner := &recordingRunner{
				respond: func(req core.LocalCommandRequest) (core.LocalCommandResult, error, bool) {
					if strings.Contains(strings.Join(req.Args, "\n"), core.WindowsActionsRunnerCredentialPath) {
						credentialCalls++
						return core.LocalCommandResult{Stderr: "credential persistence fixture failure", ExitCode: 1}, errors.New("fixture failure"), true
					}
					return core.LocalCommandResult{}, nil, false
				},
			}
			b := testBackend(runner)
			b.guestRetryBackoff = 0

			_, err := b.Acquire(context.Background(), core.AcquireRequest{
				Repo:          core.Repo{Root: root},
				RequestedSlug: "credential-failure",
				Keep:          keep,
			})
			if err == nil || !strings.Contains(err.Error(), "credential persistence failed") {
				t.Fatalf("Acquire error=%v, want credential persistence failure", err)
			}
			if credentialCalls != 5 {
				t.Fatalf("credential persistence calls=%d want 5 bounded retries", credentialCalls)
			}
			credentialIndex := -1
			cleanupIndex := -1
			for i, call := range runner.calls {
				args := strings.Join(call.Args, "\n")
				if credentialIndex < 0 && strings.Contains(args, core.WindowsActionsRunnerCredentialPath) {
					credentialIndex = i
				}
				if credentialIndex >= 0 && i > credentialIndex && strings.Contains(args, "Get-VM -ErrorAction Stop | Where-Object") {
					cleanupIndex = i
					break
				}
			}
			if keep && cleanupIndex >= 0 {
				t.Fatalf("retained failed acquisition unexpectedly removed VM at call %d", cleanupIndex)
			}
			if !keep && cleanupIndex <= credentialIndex {
				t.Fatalf("non-retained failed acquisition did not clean up after persistence failure: credential=%d cleanup=%d", credentialIndex, cleanupIndex)
			}
		})
	}
}

func TestWindowsActionsRunnerCredentialPowerShellPreservesExactUTF8AndACL(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows ACL behavior")
	}
	user := strings.TrimSpace(os.Getenv("USERNAME"))
	if user == "" || exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Get-LocalUser -Name $env:USERNAME -ErrorAction Stop | Out-Null").Run() != nil {
		currentSID, err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Security.Principal.WindowsIdentity]::GetCurrent().User.Value").Output()
		if err != nil || strings.TrimSpace(string(currentSID)) != "S-1-5-18" {
			t.Skip("current identity is neither a local user nor LocalSystem")
		}
		user = "Administrator"
	}
	if err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Get-LocalUser -Name '"+strings.ReplaceAll(user, "'", "''")+"' -ErrorAction Stop | Out-Null").Run(); err != nil {
		t.Skipf("no suitable local user is available: %v", err)
	}

	root := t.TempDir()
	target := filepath.Join(root, "credential", "windows.password")
	password := " \u03c0-crabbox-test \t"
	scriptPath := filepath.Join(root, "persist-credential.ps1")
	wrapper := "$persist = {\n" + windowsActionsRunnerCredentialPowerShell(user, target) + "\n}\n& $persist -Password $env:CRABBOX_TEST_PASSWORD\n"
	if err := os.WriteFile(scriptPath, []byte(wrapper), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.Env = append(os.Environ(), "CRABBOX_TEST_PASSWORD="+password)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("credential persistence script failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(password)) {
		t.Fatalf("credential bytes=%x want exact UTF-8 bytes=%x", got, []byte(password))
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".windows.password.*.tmp")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("credential transaction left temp files: %v", matches)
	}

	aclCommand := `$targetPath = $env:CRABBOX_TEST_TARGET
$userSid = ([Security.Principal.NTAccount]::new($env:COMPUTERNAME, $env:CRABBOX_TEST_USER)).Translate([Security.Principal.SecurityIdentifier]).Value
$acl = [IO.File]::GetAccessControl($targetPath, [Security.AccessControl.AccessControlSections]::Access)
$rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) | ForEach-Object {
  [pscustomobject]@{
    Sid = $_.IdentityReference.Value
    Rights = $_.FileSystemRights.ToString()
    Type = $_.AccessControlType.ToString()
    Inherited = $_.IsInherited
  }
})
[pscustomobject]@{ Protected = $acl.AreAccessRulesProtected; UserSid = $userSid; Rules = $rules } | ConvertTo-Json -Depth 4 -Compress`
	aclCmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", aclCommand)
	aclCmd.Env = append(os.Environ(), "CRABBOX_TEST_TARGET="+target, "CRABBOX_TEST_USER="+user)
	aclOutput, err := aclCmd.Output()
	if err != nil {
		t.Fatalf("read credential ACL: %v", err)
	}
	var acl struct {
		Protected bool   `json:"Protected"`
		UserSID   string `json:"UserSid"`
		Rules     []struct {
			SID       string `json:"Sid"`
			Rights    string `json:"Rights"`
			Type      string `json:"Type"`
			Inherited bool   `json:"Inherited"`
		} `json:"Rules"`
	}
	if err := json.Unmarshal(aclOutput, &acl); err != nil {
		t.Fatalf("parse credential ACL %q: %v", aclOutput, err)
	}
	if !acl.Protected || len(acl.Rules) != 3 {
		t.Fatalf("credential ACL protected=%v rules=%#v", acl.Protected, acl.Rules)
	}
	expectedSIDs := map[string]bool{acl.UserSID: true, "S-1-5-32-544": true, "S-1-5-18": true}
	for _, rule := range acl.Rules {
		if !expectedSIDs[rule.SID] || rule.Rights != "FullControl" || rule.Type != "Allow" || rule.Inherited {
			t.Fatalf("unexpected credential ACL rule: %#v", rule)
		}
		delete(expectedSIDs, rule.SID)
	}
	if len(expectedSIDs) != 0 {
		t.Fatalf("credential ACL missing SIDs: %#v", expectedSIDs)
	}
}

func TestFinalizeWindowsCapabilitiesDoesNotLabelFailedReadiness(t *testing.T) {
	b := testBackend(&recordingRunner{})
	b.waitWindowsVNC = func(context.Context, *SSHTarget, io.Writer, time.Duration) error {
		return errors.New("VNC not ready")
	}
	cfg := b.configForRun()
	cfg.Desktop = true
	lease := LeaseTarget{Server: Server{Labels: map[string]string{}}, SSH: SSHTarget{}}

	if err := b.finalizeWindowsCapabilities(context.Background(), cfg, "crabbox-capability-1234", &lease); err == nil {
		t.Fatal("finalizeWindowsCapabilities succeeded before VNC readiness")
	}
	if lease.Server.Labels["desktop"] != "" {
		t.Fatalf("failed readiness persisted desktop label: %#v", lease.Server.Labels)
	}
}

func TestBootstrapWindowsDesktopRetriesExpectedRebootWithinBounds(t *testing.T) {
	desktopCalls := 0
	runner := &recordingRunner{
		blockUntilCtx: func(req core.LocalCommandRequest) bool {
			if !strings.Contains(strings.Join(req.Args, "\n"), "desktop-setup-complete") {
				return false
			}
			desktopCalls++
			return desktopCalls == 1
		},
	}
	b := testBackend(runner)
	guestPassword := b.guestPassword()
	b.guestInvokeTimeout = 20 * time.Millisecond
	b.guestReadyProbeTimeout = 20 * time.Millisecond
	b.guestReadyBudget = 200 * time.Millisecond
	b.guestRetryBackoff = time.Millisecond

	started := time.Now()
	if err := b.bootstrapWindowsDesktop(context.Background(), "crabbox-capability-1234", "crabbox"); err != nil {
		t.Fatalf("bootstrapWindowsDesktop: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("desktop reboot recovery took %s, want bounded retry", elapsed)
	}
	if desktopCalls != 2 {
		t.Fatalf("desktop bootstrap calls=%d want 2", desktopCalls)
	}

	var desktopRequest *core.LocalCommandRequest
	for i := range runner.calls {
		call := &runner.calls[i]
		if strings.Contains(strings.Join(call.Args, "\n"), "desktop-setup-complete") {
			desktopRequest = call
			break
		}
	}
	if desktopRequest == nil {
		t.Fatal("desktop bootstrap was not invoked over PowerShell Direct")
	}
	args := strings.Join(desktopRequest.Args, "\n")
	if strings.Contains(args, guestPassword) {
		t.Fatal("guest password appeared on the PowerShell argv")
	}
	if !strings.Contains(args, "-ArgumentList $env:_CRABBOX_GP") {
		t.Fatalf("desktop bootstrap did not pass the guest password as a remoting argument: %s", args)
	}
	if !strings.Contains(args, `C:\ProgramData\crabbox\vnc.password`) {
		t.Fatalf("desktop bootstrap did not use the separate VNC password path: %s", args)
	}
	if !strings.Contains(args, "LoopbackOnly") || !strings.Contains(args, "SERVER_ADD_FIREWALL_EXCEPTION=0") {
		t.Fatalf("desktop bootstrap did not enforce loopback-only VNC without a firewall exception: %s", args)
	}
	if !containsEnv(desktopRequest.Env, "_CRABBOX_GP="+guestPassword) {
		t.Fatal("guest password was not supplied through the host environment")
	}
}

func TestProbeWindowsBrowserAcceptsEdgeAndChrome(t *testing.T) {
	tests := map[string]string{
		"edge":   `C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		"chrome": `C:\Program Files\Google\Chrome\Application\chrome.exe`,
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{
				respond: func(req core.LocalCommandRequest) (core.LocalCommandResult, error, bool) {
					if !strings.Contains(strings.Join(req.Args, "\n"), "Microsoft\\Edge\\Application\\msedge.exe") {
						return core.LocalCommandResult{}, nil, false
					}
					return core.LocalCommandResult{Stdout: "BROWSER=" + path + "\nCHROME_BIN=" + path + "\n"}, nil, true
				},
			}
			if err := testBackend(runner).probeWindowsBrowser(context.Background(), "crabbox-browser-1234", "crabbox"); err != nil {
				t.Fatalf("probeWindowsBrowser: %v", err)
			}
		})
	}
}

func TestProbeWindowsBrowserRejectsAbsentBrowser(t *testing.T) {
	err := testBackend(&recordingRunner{}).probeWindowsBrowser(context.Background(), "crabbox-browser-1234", "crabbox")
	if err == nil || !strings.Contains(err.Error(), "no supported Edge or Chrome browser") {
		t.Fatalf("probeWindowsBrowser err=%v", err)
	}
}

func TestProbeWindowsBrowserSurfacesProbeFailure(t *testing.T) {
	runner := &recordingRunner{
		respond: func(req core.LocalCommandRequest) (core.LocalCommandResult, error, bool) {
			if strings.Contains(strings.Join(req.Args, "\n"), "Microsoft\\Edge\\Application\\msedge.exe") {
				return core.LocalCommandResult{Stderr: "probe failed"}, errors.New("exit status 1"), true
			}
			return core.LocalCommandResult{}, nil, false
		},
	}
	err := testBackend(runner).probeWindowsBrowser(context.Background(), "crabbox-browser-1234", "crabbox")
	if err == nil || !strings.Contains(err.Error(), "browser=true requested") {
		t.Fatalf("probeWindowsBrowser err=%v", err)
	}
}

func containsEnv(env []string, want string) bool {
	for _, value := range env {
		if value == want {
			return true
		}
	}
	return false
}
