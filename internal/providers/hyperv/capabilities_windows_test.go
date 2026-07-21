package hyperv

import (
	"context"
	"errors"
	"io"
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
