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

func (b *backend) persistWindowsActionsRunnerCredential(ctx context.Context, vmName, user string) error {
	return b.invokeInGuestWithPassword(
		ctx,
		vmName,
		user,
		windowsActionsRunnerCredentialPowerShell(user, core.WindowsActionsRunnerCredentialPath),
		"Windows Actions runner credential persistence",
	)
}

func windowsActionsRunnerCredentialPowerShell(user, targetPath string) string {
	return fmt.Sprintf(`
param([AllowEmptyString()][string]$Password)
$ErrorActionPreference = 'Stop'
if ($null -eq $Password -or $Password.Length -eq 0 -or $Password.Trim().Length -eq 0) {
  throw 'Windows Actions runner password must not be empty or whitespace'
}
$targetPath = '%s'
$selectedUser = '%s'
$selectedUserSid = ([Security.Principal.NTAccount]::new($env:COMPUTERNAME, $selectedUser)).Translate([Security.Principal.SecurityIdentifier])
$allowedSids = @(
  $selectedUserSid,
  [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'),
  [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
)
$directory = Split-Path -Parent $targetPath
[IO.Directory]::CreateDirectory($directory) | Out-Null
$tempPath = Join-Path $directory ('.windows.password.' + [Guid]::NewGuid().ToString('N') + '.tmp')
$tempStream = $null
$replaced = $false
$committed = $false
try {
  $tempStream = [IO.FileStream]::new(
    $tempPath,
    [IO.FileMode]::CreateNew,
    [Security.AccessControl.FileSystemRights]::FullControl,
    [IO.FileShare]::None,
    4096,
    [IO.FileOptions]::WriteThrough
  )
  $acl = [Security.AccessControl.FileSecurity]::new()
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($sid in $allowedSids) {
    $rule = [Security.AccessControl.FileSystemAccessRule]::new(
      $sid,
      [Security.AccessControl.FileSystemRights]::FullControl,
      [Security.AccessControl.InheritanceFlags]::None,
      [Security.AccessControl.PropagationFlags]::None,
      [Security.AccessControl.AccessControlType]::Allow
    )
    [void]$acl.AddAccessRule($rule)
  }
  $tempStream.SetAccessControl($acl)
  $encoding = [Text.UTF8Encoding]::new($false)
  $encodedPassword = $encoding.GetBytes($Password)
  $tempStream.Write($encodedPassword, 0, $encodedPassword.Length)
  $tempStream.Flush($true)
  $tempStream.Dispose()
  $tempStream = $null

  if (-not ('Crabbox.WindowsNativeFile' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

namespace Crabbox {
    public static class WindowsNativeFile {
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern bool MoveFileEx(string existingFileName, string newFileName, int flags);
    }
}
'@
  }
  $moveFileReplaceExisting = 0x1
  $moveFileWriteThrough = 0x8
  if (-not [Crabbox.WindowsNativeFile]::MoveFileEx($tempPath, $targetPath, $moveFileReplaceExisting -bor $moveFileWriteThrough)) {
    throw [ComponentModel.Win32Exception]::new([Runtime.InteropServices.Marshal]::GetLastWin32Error(), 'Atomic credential replacement failed')
  }
  $replaced = $true
  $tempPath = $null

  if (-not [IO.File]::Exists($targetPath)) {
    throw 'Windows Actions runner credential file was not created'
  }
  $expectedBytes = $encoding.GetBytes($Password)
  $actualBytes = [IO.File]::ReadAllBytes($targetPath)
  if ($actualBytes.Length -ne $expectedBytes.Length) {
    throw 'Windows Actions runner credential readback length mismatch'
  }
  for ($index = 0; $index -lt $expectedBytes.Length; $index++) {
    if ($actualBytes[$index] -ne $expectedBytes[$index]) {
      throw 'Windows Actions runner credential readback mismatch'
    }
  }

  $finalAcl = [IO.File]::GetAccessControl($targetPath, [Security.AccessControl.AccessControlSections]::Access)
  if (-not $finalAcl.AreAccessRulesProtected) {
    throw 'Windows Actions runner credential ACL is not protected'
  }
  $rules = @($finalAcl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
  if ($rules.Count -ne $allowedSids.Count) {
    throw 'Windows Actions runner credential ACL has an unexpected rule count'
  }
  $expectedSidValues = @{}
  foreach ($sid in $allowedSids) {
    $expectedSidValues[$sid.Value] = $true
  }
  $actualSidValues = @{}
  foreach ($rule in $rules) {
    $sidValue = $rule.IdentityReference.Value
    if (
      $rule.IsInherited -or
      $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
      $rule.FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl -or
      -not $expectedSidValues.ContainsKey($sidValue) -or
      $actualSidValues.ContainsKey($sidValue)
    ) {
      throw 'Windows Actions runner credential ACL contains an unexpected rule'
    }
    $actualSidValues[$sidValue] = $true
  }
  $committed = $true
} finally {
  if ($tempStream) {
    $tempStream.Dispose()
  }
  if ($replaced -and -not $committed -and [IO.File]::Exists($targetPath)) {
    [IO.File]::Delete($targetPath)
  }
  if ($tempPath -and [IO.File]::Exists($tempPath)) {
    [IO.File]::Delete($tempPath)
  }
}
`, escapePSString(targetPath), escapePSString(user))
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
		`%sInvoke-Command -VMName '%s' -Credential $cred%s -ScriptBlock { %s }`,
		powershellCredentialPrelude(user), escapePSString(vmName), argumentList, scriptBlock,
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
