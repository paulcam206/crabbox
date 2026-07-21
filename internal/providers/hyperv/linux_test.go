package hyperv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/openclaw/crabbox/internal/cli"
)

func TestApplyDefaultsUsesPOSIXWorkRootForLinux(t *testing.T) {
	cfg := core.BaseConfig()
	cfg.Provider = providerName
	cfg.TargetOS = targetLinux
	applyDefaults(&cfg)
	if cfg.TargetOS != targetLinux || cfg.HyperV.WorkRoot != "/work/crabbox" || cfg.WorkRoot != "/work/crabbox" {
		t.Fatalf("target=%q hyperv.workRoot=%q workRoot=%q", cfg.TargetOS, cfg.HyperV.WorkRoot, cfg.WorkRoot)
	}
	if cfg.HyperV.User != "crabbox" || cfg.HyperV.SecureBoot != secureBootAuto {
		t.Fatalf("user=%q secureBoot=%q", cfg.HyperV.User, cfg.HyperV.SecureBoot)
	}
}

func TestApplyDefaultsPreservesExplicitLinuxWorkRootSentinel(t *testing.T) {
	cfg := core.BaseConfig()
	cfg.Provider = providerName
	cfg.TargetOS = targetLinux
	cfg.WorkRoot = "/srv/global"
	cfg.HyperV.WorkRoot = "/work/crabbox"
	core.SetHyperVWorkRootExplicit(&cfg)
	applyDefaults(&cfg)
	if cfg.HyperV.WorkRoot != "/work/crabbox" || cfg.WorkRoot != "/work/crabbox" {
		t.Fatalf("hyperv.workRoot=%q workRoot=%q", cfg.HyperV.WorkRoot, cfg.WorkRoot)
	}
}

func TestLinuxAcquireDoesNotRequireGuestPassword(t *testing.T) {
	oldOS := hypervHostOS
	hypervHostOS = "windows"
	t.Cleanup(func() { hypervHostOS = oldOS })

	b := testBackend(&recordingRunner{})
	b.cfg.TargetOS = targetLinux
	b.cfg.HyperV.Image = `C:\Images\debian-cloud.vhdx`
	b.cfg.HyperV.GuestPassword = ""
	_, err := b.Acquire(context.Background(), core.AcquireRequest{})
	if err == nil || !strings.Contains(err.Error(), "requires a repository root") {
		t.Fatalf("Linux acquire should progress without a guest password, got %v", err)
	}
	if strings.Contains(err.Error(), "GUEST_PASSWORD") {
		t.Fatalf("Linux acquire incorrectly required a guest password: %v", err)
	}
}

func TestLinuxRejectsInitPassword(t *testing.T) {
	cfg := core.BaseConfig()
	cfg.Provider = providerName
	cfg.TargetOS = targetLinux
	cfg.HyperV.InitPassword = true
	_, err := (Provider{}).Configure(cfg, core.Runtime{Stdout: io.Discard, Stderr: io.Discard, Exec: &recordingRunner{}})
	if err == nil || !strings.Contains(err.Error(), "only for target=windows") {
		t.Fatalf("Configure error=%v", err)
	}
}

func TestValidLinuxSSHUser(t *testing.T) {
	for _, user := range []string{"crabbox", "_service", "user-1", "a"} {
		if !validLinuxSSHUser(user) {
			t.Errorf("validLinuxSSHUser(%q)=false", user)
		}
	}
	for _, user := range []string{"", "-guest", "Guest", "user.name", "user name", strings.Repeat("a", 33)} {
		if validLinuxSSHUser(user) {
			t.Errorf("validLinuxSSHUser(%q)=true", user)
		}
	}
}

func TestCreateLinuxVMConnectsNetworkBeforeBootWithoutPowerShellDirect(t *testing.T) {
	runner := &recordingRunner{}
	b := testBackend(runner)
	cfg := b.configForRun()
	cfg.TargetOS = targetLinux
	cfg.HyperV.Image = `C:\Images\debian-cloud.vhdx`
	cfg.HyperV.SecureBoot = secureBootAuto
	seedPath := `C:\Hyper-V\Virtual Hard Disks\crabbox-blue-1234-seed.vhdx`

	if err := b.createLinuxVM(context.Background(), cfg, "crabbox-blue-1234", seedPath); err != nil {
		t.Fatal(err)
	}
	connectIndex, startIndex := -1, -1
	for i, call := range runner.calls {
		script := call.Args[len(call.Args)-1]
		switch {
		case strings.Contains(script, "Connect-VMNetworkAdapter"):
			connectIndex = i
		case strings.Contains(script, "Start-VM"):
			startIndex = i
		}
		if strings.Contains(script, "Invoke-Command -VMName") {
			t.Fatalf("Linux VM creation used PowerShell Direct: %q", script)
		}
	}
	if connectIndex < 0 || startIndex < 0 || connectIndex >= startIndex {
		t.Fatalf("network/start order connect=%d start=%d", connectIndex, startIndex)
	}
	calls := joinedCallScripts(runner.calls)
	for _, want := range []string{
		"-Differencing",
		"-Generation 2",
		"Add-VMHardDiskDrive",
		"-ControllerType SCSI",
		"-ControllerLocation 1",
		"MicrosoftUEFICertificateAuthority",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("Linux VM calls missing %q", want)
		}
	}
}

func TestLinuxAcquireResolveReleaseLifecycle(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldOS := hypervHostOS
	hypervHostOS = "windows"
	t.Cleanup(func() { hypervHostOS = oldOS })

	vmExists := false
	vmName := ""
	runner := &recordingRunner{}
	runner.respond = func(req core.LocalCommandRequest) (core.LocalCommandResult, error, bool) {
		script := req.Args[len(req.Args)-1]
		switch {
		case strings.Contains(script, "Get-VM | Where-Object"):
			if !vmExists {
				return core.LocalCommandResult{Stdout: "null"}, nil, true
			}
			return core.LocalCommandResult{Stdout: fmt.Sprintf(`{"Name":%q,"State":2}`, vmName)}, nil, true
		case strings.Contains(script, "New-VM -Name"):
			vmExists = true
			vmName = powershellNamedArgument(script, "New-VM -Name '")
			return core.LocalCommandResult{}, nil, true
		case strings.Contains(script, "Get-VMNetworkAdapter"):
			return core.LocalCommandResult{Stdout: `["192.0.2.25"]`}, nil, true
		case strings.Contains(script, "Get-VM -ErrorAction Stop") && strings.Contains(script, "Select-Object Name, State"):
			if !vmExists {
				return core.LocalCommandResult{Stdout: "null"}, nil, true
			}
			name := powershellComparedName(script)
			return core.LocalCommandResult{Stdout: fmt.Sprintf(`{"Name":%q,"State":2}`, name)}, nil, true
		case strings.Contains(script, "Get-VMHardDiskDrive") && strings.Contains(script, "ExpandProperty Path"):
			return core.LocalCommandResult{}, nil, true
		case strings.Contains(script, "Remove-VM -Name"):
			vmExists = false
			return core.LocalCommandResult{}, nil, true
		default:
			return core.LocalCommandResult{}, nil, false
		}
	}
	b := testBackend(runner)
	b.cfg.TargetOS = targetLinux
	b.cfg.HyperV.Image = `C:\Images\debian-cloud.vhdx`
	b.cfg.HyperV.GuestPassword = ""
	var readyTarget SSHTarget
	b.sshReady = func(_ context.Context, target *SSHTarget, _ io.Writer, phase string, _ time.Duration) error {
		readyTarget = *target
		if phase != "hyperv ssh" {
			t.Errorf("phase=%q", phase)
		}
		return nil
	}

	lease, err := b.Acquire(context.Background(), core.AcquireRequest{
		Repo:          core.Repo{Root: t.TempDir()},
		RequestedSlug: "linux-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Server.Labels["target"] != targetLinux || lease.SSH.TargetOS != targetLinux {
		t.Fatalf("lease target labels=%q ssh.target=%q", lease.Server.Labels["target"], lease.SSH.TargetOS)
	}
	if readyTarget.Key == "" || readyTarget.TargetOS != targetLinux {
		t.Fatalf("ready target=%#v", readyTarget)
	}
	if !strings.Contains(readyTarget.ReadyCheck, "crabbox-ready") {
		t.Fatalf("ready check=%q", readyTarget.ReadyCheck)
	}
	for _, call := range runner.calls {
		script := call.Args[len(call.Args)-1]
		if strings.Contains(script, "Invoke-Command -VMName") {
			t.Fatalf("Linux acquire used PowerShell Direct: %q", script)
		}
	}

	views, err := b.List(context.Background(), ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Labels["target"] != targetLinux {
		t.Fatalf("views=%#v", views)
	}

	resolved, err := b.Resolve(context.Background(), ResolveRequest{ID: lease.LeaseID})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SSH.TargetOS != targetLinux || resolved.SSH.User != "crabbox" || resolved.SSH.Host != "192.0.2.25" {
		t.Fatalf("resolved SSH=%#v", resolved.SSH)
	}
	if err := b.ReleaseLease(context.Background(), ReleaseLeaseRequest{Lease: resolved}); err != nil {
		t.Fatal(err)
	}
	if vmExists {
		t.Fatal("release left VM present")
	}
	claims, err := listLeaseClaims()
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims after release=%#v", claims)
	}
	keyPath, err := testboxKeyPath(lease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("release left key %s: %v", keyPath, err)
	}
	for _, path := range []string{
		filepath.Join(hypervVHDDir(), lease.Server.Name+".vhdx"),
		cloudInitSeedPath(lease.Server.Name),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("release left disk %s: %v", path, err)
		}
	}
}

func joinedCallScripts(calls []core.LocalCommandRequest) string {
	var scripts []string
	for _, call := range calls {
		scripts = append(scripts, call.Args[len(call.Args)-1])
	}
	return strings.Join(scripts, "\n")
}

func powershellComparedName(script string) string {
	return powershellNamedArgument(script, "$_.Name -eq '")
}

func powershellNamedArgument(script, marker string) string {
	start := strings.Index(script, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(script[start:], "'")
	if end < 0 {
		return ""
	}
	return script[start : start+end]
}
