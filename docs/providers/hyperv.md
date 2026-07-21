# Hyper-V

Provider id: `hyperv`
Kind: SSH lease
Targets: Linux, Windows (native)
Family: `local-vm`

## Overview

The Hyper-V provider creates and manages Linux or Windows virtual machines on a
local Windows host using Microsoft Hyper-V. VMs are provisioned as Generation 2
VMs from a target-appropriate VHDX template, connected to a configurable virtual
switch (default: "Default Switch"), and accessed over SSH. Windows normal mode
is the default target when `--target` is omitted.

| Capability | Linux | Windows normal |
| --- | --- | --- |
| SSH, Crabbox sync, cleanup | Yes | Yes |
| Pause and resume | Yes | Yes |
| Desktop and browser | Yes, through cloud-init | Yes, through PowerShell Direct |
| Workspace checkpoint, fork, restore, provider snapshot | No | Yes |
| Code, Tailscale, cache volume | No | No |

Hyper-V must be enabled on the host (`Enable-WindowsOptionalFeature -Online
-FeatureName Microsoft-Hyper-V-All`). The provider is Windows-only and will
reject configuration on non-Windows hosts.

## Requirements

- Windows 10 Pro/Enterprise/Education or Windows Server with Hyper-V enabled
- PowerShell 5.1 or later (ships with Windows)
- A Generation 2 / UEFI VHDX template for the selected target
- For Linux, a generalized Debian or Ubuntu cloud VHDX with:
  - cloud-init NoCloud support
  - current Hyper-V integration services, including a running
    `hv_kvp_daemon` KVP service so Hyper-V can report guest IPs
  - DHCP networking
  - guest internet access for first-boot apt packages
- For Windows, a VHDX with:
  - A local administrator account selected with `--hyperv-user`, with its
    password explicitly provided through `CRABBOX_HYPERV_GUEST_PASSWORD`
  - Network configured for DHCP on the Hyper-V virtual switch
  - Guest internet access to GitHub when OpenSSH or git must be installed on
    first use
- The Hyper-V and Windows storage PowerShell modules

Linux support is initially limited to apt-based Debian and Ubuntu cloud images.
Crabbox does not convert QCOW2 or RAW images. The Linux template must already be
a generalized VHDX.

OpenSSH and git do **not** need to be pre-installed. When missing, the provider
installs pinned, SHA-256-verified Win32-OpenSSH (matching the guest architecture)
and MinGit packages. Existing installations are reused. The OpenSSH bootstrap
does not require Windows Update or Features on Demand. ISO images are not
supported; use an installed VHDX with a known administrator password.

## Desktop and browser capabilities

`--desktop` installs the pinned, SHA-256-verified TightVNC server after the
OpenSSH and git bootstrap completes. TightVNC is configured without a firewall
exception and restricted to loopback; connect through Crabbox's SSH-backed VNC
or WebVNC commands. The guest account password is passed to PowerShell Direct
through the host process environment and a remoting argument, never on the
host command line. TightVNC receives a separate generated password stored at
`C:\ProgramData\crabbox\vnc.password`.

The desktop setup enables auto-logon and performs one expected Windows reboot.
Acquisition waits for bounded PowerShell Direct readiness, reruns the marked
idempotent setup after reboot, then waits for both SSH and loopback VNC
readiness before recording `desktop=true`.

`--browser` is probe-only. After final SSH readiness, Crabbox accepts an
existing Microsoft Edge or Google Chrome installation. It does not install a
browser, and acquisition fails with a capability error when neither browser is
present. The `browser=true` label is recorded only after the probe succeeds.

### Preparing a Windows template

The only thing a base Windows VHDX needs is a reachable administrator account.
For example, from an elevated prompt inside the guest before capturing it:

```powershell
net user Administrator '<password>'   # or your admin account
net user Administrator /active:yes
```

Then point `--hyperv-image` at the VHDX and set `--hyperv-user Administrator`
and `CRABBOX_HYPERV_GUEST_PASSWORD=<password>`. The provider handles OpenSSH.

### Password-less templates (Windows dev-environment images)

Microsoft's downloadable Windows dev-environment VHDXs auto-log-on as `User`
with **no password**, and PowerShell Direct refuses empty credentials — so a
stock image fails the bootstrap as-is. Pass `--hyperv-init-password` to use one
unmodified:

```sh
set CRABBOX_HYPERV_GUEST_PASSWORD=<password>
crabbox warmup --provider hyperv --hyperv-image C:\Images\WinDev2407Eval.vhdx ^
  --hyperv-user User --hyperv-init-password
```

Before first boot the provider mounts the per-lease differencing disk, loads
its offline registry hive, and writes a `RunOnce` command that sets the guest
account's password to `CRABBOX_HYPERV_GUEST_PASSWORD` at the template's
auto-logon. Only the lease disk is modified; the template VHDX stays untouched.

Notes:

- Requires an explicit `CRABBOX_HYPERV_GUEST_PASSWORD` (the provider refuses
  to stamp its default password onto a guest).
- Neither the password nor the user name can contain `"` or `%` (both pass
  through `cmd.exe` at logon).
- This only works for templates that auto-log-on an administrator account
  (`RunOnce` fires at logon). Templates without auto-logon need a known
  password baked in, as above.
- The password is briefly visible inside the guest (the `RunOnce` registry
  value, then the `net.exe` command line at first logon). The guest belongs to
  the lease, and the differencing disk holding it is deleted on release.

### Preparing a Linux template

Use a generalized Debian or Ubuntu cloud VHDX that boots as a Generation 2 VM
with DHCP, cloud-init, and the Hyper-V KVP integration daemon enabled. On
Debian this is commonly supplied by `hyperv-daemons`; Ubuntu cloud images
commonly use the matching `linux-cloud-tools-virtual` and
`linux-cloud-tools-common` packages. Package names vary by image and kernel, so
verify that `hv_kvp_daemon` is running before generalizing the VHDX. Do not bake
a Crabbox password into the image. At acquire time Crabbox creates the selected
`--hyperv-user`, injects a per-lease SSH public key through cloud-init, and
disables SSH password authentication.

The VM starts with networking connected because cloud-init installs required
apt packages on first boot. The selected virtual switch must therefore provide
DHCP and internet access until `crabbox-ready` succeeds.

## Configuration

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--hyperv-image` | (none) | Path to the guest VHDX template (required) |
| `--hyperv-user` | `crabbox` | Guest account for SSH; letters, digits, `.`, `_`, and `-` only |
| `--hyperv-work-root` | target-specific | `C:\crabbox` for Windows or `/work/crabbox` for Linux |
| `--hyperv-cpu` | `4` | Number of virtual CPUs |
| `--hyperv-memory` | `8192` | Memory in MB |
| `--hyperv-switch` | `Default Switch` | Hyper-V virtual switch name |
| `--hyperv-secure-boot` | `auto` | `auto`, `windows`, `linux`, or `off` |
| `--hyperv-init-password` | `false` | Windows only: set the guest password at first boot via the lease disk |

`auto` selects the Microsoft Windows secure boot template for Windows targets
and the Microsoft UEFI Certificate Authority template for Linux targets.

### Config file

```yaml
hyperv:
  image: C:\Images\windows-crabbox.vhdx
  user: crabbox
  workRoot: C:\crabbox
  cpus: 4
  memory: 8192
  switch: Default Switch
  secureBoot: auto
  initPassword: false
```

For Windows, keep `CRABBOX_HYPERV_GUEST_PASSWORD` in the environment or trusted
user config, not repository config. There is no default guest password. Linux
does not use this password and rejects `hyperv.initPassword`.

### Environment variables

| Variable | Description |
| --- | --- |
| `CRABBOX_HYPERV_IMAGE` | VHDX template path |
| `CRABBOX_HYPERV_USER` | SSH user inside the VM |
| `CRABBOX_HYPERV_WORK_ROOT` | Work root inside the VM |
| `CRABBOX_HYPERV_CPUS` | CPU count |
| `CRABBOX_HYPERV_MEMORY` | Memory in MB |
| `CRABBOX_HYPERV_SWITCH` | Virtual switch name |
| `CRABBOX_HYPERV_SECURE_BOOT` | Secure boot mode: `auto`, `windows`, `linux`, or `off` |
| `CRABBOX_HYPERV_GUEST_PASSWORD` | Windows guest password for PowerShell Direct bootstrap |
| `CRABBOX_HYPERV_INIT_PASSWORD` | Windows only: set the guest password at first boot (`true`/`false`) |

## Bootstrap contract

All targets start with a per-lease differencing root VHDX over the configured
template and a Generation 2 VM.

### Linux

During Linux `Acquire`, the provider:

1. Generates cloud-init from the shared `CloudInitUserData` implementation,
   including the per-lease SSH key and requested desktop/browser bootstrap.
2. Creates a small dynamic VHDX, formats it as FAT with label `cidata`, and
   writes UTF-8 no-BOM `user-data` and `meta-data`.
3. Creates the VM, attaches the seed as a secondary SCSI disk, applies the
   selected secure boot firmware, connects networking, and starts the VM.
4. Discovers the DHCP address through `Get-VMNetworkAdapter`.
5. Waits for key-only SSH and `/usr/local/bin/crabbox-ready`.
6. Detaches and deletes the seed VHDX after readiness.

Linux guest commands are never run through PowerShell Direct.

### Windows

During Windows `Acquire`, the provider:

1. Creates the per-lease differencing disk backed by the template.
2. With `--hyperv-init-password`, mounts the lease disk offline and writes a
   first-boot `RunOnce` that sets the guest password (password-less templates)
3. Creates and starts the VM with its network adapter disconnected
4. Waits for PowerShell Direct readiness within a bounded boot timeout
5. Uses PowerShell Direct to stop/disable sshd, add an inbound TCP/22 block
   rule, replace authorized keys, discard template `Match` authentication
   blocks, and restrict SSH to the selected user
6. Connects the network adapter, installs the pinned Win32-OpenSSH MSI from
   GitHub if `sshd` is absent, and keeps sshd stopped behind the quarantine rule
7. Reapplies the final key-only config, validates `sshd_config`, regenerates
   per-lease SSH host keys, starts sshd, and removes the quarantine rule last
8. Installs git (MinGit) if absent — required for Crabbox sync
9. With `--desktop`, installs/configures loopback-only TightVNC, handles its
   expected one-time reboot through bounded PowerShell Direct retries, and
   reruns the marked setup idempotently
10. Waits for SSH readiness on the injected key and, for `--desktop`, loopback
    VNC readiness
11. With `--browser`, probes for Edge or Chrome without installing either
12. Records successful desktop/browser lease labels only after all requested
    readiness checks pass

PowerShell Direct calls use the guest administrator password. Readiness and
later guest operations have bounded retries, preventing a stalled call from
hanging the lease.

## Workspace checkpoints

Workspace checkpoint, fork, restore, and provider snapshot capabilities are
available for Windows normal leases only. They use Hyper-V production
checkpoints and exported VM artifacts. Linux checkpoint specialization is not
implemented yet, so Linux targets do not advertise these capabilities and the
native checkpoint capability probe rejects them.

## Pause and resume

`crabbox pause --provider hyperv <lease>` uses Hyper-V saved state so the VM
releases host CPU and memory while preserving guest state. A running VM is
saved with `Save-VM`; a Hyper-V `Paused` VM is also converted to saved state
because Hyper-V pause alone retains the VM's assigned memory. Pausing an
already saved lease is a no-op.

`crabbox resume --provider hyperv <lease>` starts a saved VM with `Start-VM`.
For a VM left in Hyper-V's native `Paused` state, Crabbox uses `Resume-VM`.
After either transition, Crabbox waits for DHCP and SSH readiness, then
atomically refreshes the local lease claim with the current IP address. Pause
and resume require an exact local claim for the VM; stopped or missing VMs are
reported as errors.

Set `CRABBOX_HYPERV_GUEST_PASSWORD` or `hyperv.guestPassword` in trusted user
config to match the administrator password in your VHDX template. The provider
requires an explicit value and disables SSH password authentication after key
installation.

## Lifecycle

1. **Acquire**: Creates a per-lease differencing root disk and Generation 2 VM,
   applies target-aware secure boot, then follows the Linux cloud-init or
   Windows PowerShell Direct bootstrap described above.
2. **Resolve**: Finds a running crabbox VM by lease ID, slug, or instance name.
   Queries live VM state and IP from Hyper-V.
3. **List**: Lists all VMs with the `crabbox-` name prefix.
4. **Release**: Stops the VM (`Stop-VM -Force`) and removes it
   (`Remove-VM -Force`), then cleans up provider-owned root, checkpoint, and
   NoCloud seed VHDX files.
5. **Cleanup**: Scans for stale `crabbox-` prefixed VMs and removes only VMs
   bound to an exact expired local claim.

## Notes

- All VMs are named with a `crabbox-` prefix, but the prefix is not ownership
  proof. Cleanup and release require an exact local claim bound to the VM name;
  recovered VMs must first be adopted through an explicit `--reclaim` reuse.
- The selected SSH account must be a local account name containing only letters,
  digits, `.`, `_`, or `-`. Domain/UPN names and SSH pattern characters are
  rejected so `AllowUsers` cannot broaden access.
- VHD files are stored in `%USERPROFILE%\Hyper-V\Virtual Hard Disks\` by
  default. Only deterministically named provider-owned root, checkpoint, and
  seed disks are cleaned up; unrelated attached disks are preserved.
- The Windows ready check verifies `git`, `tar`, and the work root. The Linux
  ready check executes `/usr/local/bin/crabbox-ready`.
- There is no `tart exec` or `prlctl exec` equivalent for Hyper-V; all guest
  interaction after bootstrap happens over SSH.

## Examples

```powershell
crabbox warmup --provider hyperv --target windows `
  --hyperv-image C:\Images\win-server.vhdx
crabbox run --provider hyperv --target windows -- powershell -Command "Get-Process"
```

```powershell
crabbox warmup --provider hyperv --target linux `
  --hyperv-image C:\Images\ubuntu-cloud.vhdx `
  --hyperv-secure-boot auto
crabbox run --provider hyperv --target linux -- uname -a
```

```sh
crabbox ssh --provider hyperv
crabbox stop --provider hyperv --id blue-lobster
crabbox cleanup --provider hyperv
```
