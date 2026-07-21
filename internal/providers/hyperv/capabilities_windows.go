package hyperv

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	core "github.com/openclaw/crabbox/internal/cli"
)

func windowsCapabilityConfig(cfg Config, options core.LeaseOptions) Config {
	cfg.Desktop = cfg.Desktop || options.Desktop
	cfg.Browser = cfg.Browser || options.Browser
	return cfg
}

func provisionalWindowsCapabilityLabels(cfg Config, leaseID, slug string, keep bool, now time.Time) map[string]string {
	provisional := cfg
	provisional.Desktop = false
	provisional.Browser = false
	return directLeaseLabels(provisional, leaseID, slug, providerName, "", keep, now)
}

func markWindowsCapabilitiesReady(labels map[string]string, cfg Config) {
	if cfg.Desktop {
		labels["desktop"] = "true"
		labels["desktop_env"] = "xfce"
	}
	if cfg.Browser {
		labels["browser"] = "true"
	}
}

func (b *backend) finalizeWindowsCapabilities(ctx context.Context, cfg Config, vmName string, lease *LeaseTarget) error {
	if cfg.Desktop {
		if err := b.waitWindowsVNC(ctx, &lease.SSH, b.rt.Stderr, 5*time.Minute); err != nil {
			return err
		}
	}
	if cfg.Browser {
		if err := b.probeWindowsBrowser(ctx, vmName, cfg.HyperV.User); err != nil {
			return err
		}
	}
	markWindowsCapabilitiesReady(lease.Server.Labels, cfg)
	return nil
}

func (b *backend) bootstrapWindowsDesktop(ctx context.Context, vmName, user string) error {
	bootstrapCtx, cancel := context.WithTimeout(ctx, b.guestReadyBudget+(2*b.guestInvokeTimeout))
	defer cancel()

	script := core.ManagedWindowsDesktopBootstrapPowerShell(user)
	if _, err := b.invokeInGuestWithPasswordOnce(bootstrapCtx, vmName, user, script, "Windows desktop bootstrap"); err != nil {
		fmt.Fprintf(b.rt.Stderr, "Windows desktop bootstrap interrupted while the guest may be rebooting: %v\n", err)
	}
	if err := b.waitGuestReady(bootstrapCtx, vmName, user); err != nil {
		return fmt.Errorf("guest did not return after Windows desktop bootstrap reboot: %w", err)
	}
	if err := b.invokeInGuestWithPassword(bootstrapCtx, vmName, user, script, "Windows desktop bootstrap retry"); err != nil {
		return fmt.Errorf("Windows desktop bootstrap did not complete after reboot: %w", err)
	}
	return nil
}

func (b *backend) probeWindowsBrowser(ctx context.Context, vmName, user string) error {
	result, err := b.invokeInGuestOnce(ctx, vmName, user, core.WindowsBrowserProbePowerShell(), "Windows browser probe")
	if err != nil {
		return exit(2, "browser=true requested but no supported Edge or Chrome browser was found in the Hyper-V guest: %v", err)
	}
	env := parseCapabilityEnv(result.Stdout)
	if env["BROWSER"] == "" {
		return exit(2, "browser=true requested but no supported Edge or Chrome browser was found in the Hyper-V guest")
	}
	return nil
}

func (b *backend) invokeInGuestOnce(ctx context.Context, vmName, user, scriptBlock, label string) (LocalCommandResult, error) {
	return b.invokeInGuestCommandOnce(ctx, vmName, user, scriptBlock, label, false)
}

func (b *backend) invokeInGuestWithPasswordOnce(ctx context.Context, vmName, user, scriptBlock, label string) (LocalCommandResult, error) {
	return b.invokeInGuestCommandOnce(ctx, vmName, user, scriptBlock, label, true)
}

func (b *backend) invokeInGuestCommandOnce(ctx context.Context, vmName, user, scriptBlock, label string, passGuestPassword bool) (LocalCommandResult, error) {
	argumentList := ""
	if passGuestPassword {
		argumentList = " -ArgumentList $env:_CRABBOX_GP"
	}
	script := fmt.Sprintf(
		`$cred = New-Object PSCredential('%s', (ConvertTo-SecureString $env:_CRABBOX_GP -AsPlainText -Force)); `+
			`Invoke-Command -VMName '%s' -Credential $cred%s -ScriptBlock { %s }`,
		escapePSString(user), escapePSString(vmName), argumentList, scriptBlock,
	)
	env := append(os.Environ(), "_CRABBOX_GP="+b.guestPassword())
	result, err := b.invokeGuestScript(ctx, script, env, b.guestInvokeTimeout)
	if err != nil {
		return result, commandError(label, result, err)
	}
	return result, nil
}

func (b *backend) invokeInGuestWithPassword(ctx context.Context, vmName, user, scriptBlock, label string) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * b.guestRetryBackoff):
			}
			fmt.Fprintf(b.rt.Stderr, "retrying %s (%d/5)...\n", label, attempt+1)
		}
		_, err := b.invokeInGuestWithPasswordOnce(ctx, vmName, user, scriptBlock, label)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
	}
	return lastErr
}

func parseCapabilityEnv(input string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(input, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" {
			env[key] = strings.TrimSpace(value)
		}
	}
	return env
}
