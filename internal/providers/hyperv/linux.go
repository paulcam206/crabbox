package hyperv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	core "github.com/openclaw/crabbox/internal/cli"
)

func (b *backend) acquireLinux(ctx context.Context, req AcquireRequest) (LeaseTarget, error) {
	if hypervHostOS != "windows" {
		return LeaseTarget{}, exit(2, "provider=%s requires a Windows host with Hyper-V enabled", providerName)
	}
	cfg := b.configForRun()
	if cfg.HyperV.Image == "" {
		return LeaseTarget{}, exit(2, "provider=%s requires --hyperv-image (path to a generalized Debian or Ubuntu cloud VHDX with cloud-init)", providerName)
	}
	if !strings.HasSuffix(strings.ToLower(cfg.HyperV.Image), ".vhdx") {
		return LeaseTarget{}, exit(2, "provider=%s target=linux requires a generalized cloud VHDX; QCOW2, RAW, ISO, and other image formats are not supported", providerName)
	}
	if cfg.HyperV.InitPassword {
		return LeaseTarget{}, exit(2, "--hyperv-init-password is supported only for target=windows")
	}
	if !validLinuxSSHUser(cfg.HyperV.User) {
		return LeaseTarget{}, exit(2, "provider=%s target=linux --hyperv-user must be 1-32 characters, start with a lowercase letter or underscore, and contain only lowercase letters, digits, underscore, or hyphen", providerName)
	}
	if strings.TrimSpace(req.Repo.Root) == "" {
		return LeaseTarget{}, exit(2, "provider=%s requires a repository root so the VM claim can be persisted before bootstrap", providerName)
	}

	leaseID := newLeaseID()
	instances, err := b.listInstances(ctx)
	if err != nil {
		return LeaseTarget{}, err
	}
	claims, err := providerClaims()
	if err != nil {
		return LeaseTarget{}, err
	}
	servers := make([]Server, 0, len(instances))
	for _, inst := range instances {
		servers = append(servers, b.serverFromInstance(inst, claims[inst.Name], cfg))
	}
	slug, err := allocateDirectLeaseSlug(leaseID, req.RequestedSlug, servers)
	if err != nil {
		return LeaseTarget{}, err
	}
	keyPath, publicKey, err := ensureTestboxKeyForConfig(cfg, leaseID)
	if err != nil {
		return LeaseTarget{}, err
	}
	cleanupKey := true
	defer func() {
		if cleanupKey {
			removeStoredTestboxKey(leaseID)
		}
	}()
	cfg.SSHKey = keyPath
	name := leaseProviderName(leaseID, slug)
	labels := directLeaseLabels(cfg, leaseID, slug, providerName, "", req.Keep, time.Now().UTC())
	labels["instance"] = name
	labels["image"] = cfg.HyperV.Image
	labels["ssh_user"] = cfg.HyperV.User
	labels["ssh_port"] = sshPort
	labels["work_root"] = cfg.HyperV.WorkRoot
	labels["state"] = "provisioning"
	claim := core.LeaseClaim{LeaseID: leaseID, Slug: slug, Provider: providerName, ProviderScope: instanceScope(name), Labels: labels}
	fmt.Fprintf(b.rt.Stderr, "provisioning provider=%s target=linux lease=%s slug=%s image=%s cpus=%d memory=%dMB switch=%s keep=%v\n",
		providerName, leaseID, slug, cfg.HyperV.Image, cfg.HyperV.CPUs, cfg.HyperV.Memory, cfg.HyperV.Switch, req.Keep)

	provisional := LeaseTarget{
		Server:  b.serverFromInstance(hypervVM{Name: name, State: 2}, claim, cfg),
		LeaseID: leaseID,
	}
	if err := persistLease(leaseID, slug, name, cfg, req, provisional); err != nil {
		return LeaseTarget{}, fmt.Errorf("persist hyperv lease before bootstrap: %w", err)
	}
	cleanupKey = false
	cleanupFailedLease := func() error {
		if req.Keep {
			return nil
		}
		if err := b.removeVM(context.Background(), name); err != nil {
			return fmt.Errorf("remove failed hyperv lease %s: %w", leaseID, err)
		}
		pruneLeaseState(leaseID)
		return nil
	}

	seedPath := cloudInitSeedPath(name)
	userData := core.CloudInitUserData(cfg, publicKey)
	if err := b.createNoCloudSeed(ctx, seedPath, userData, cloudInitMetaData(name)); err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	if err := b.createLinuxVM(ctx, cfg, name, seedPath); err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	ip, err := b.waitForIP(ctx, name, 5*time.Minute)
	if err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	lease, err := b.prepareLease(ctx, cfg, hypervVM{Name: name, State: 2}, ip, claim, true)
	if err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	if err := b.detachAndRemoveNoCloudSeed(ctx, name); err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	if err := persistLease(leaseID, slug, name, cfg, req, lease); err != nil {
		return LeaseTarget{}, errors.Join(err, cleanupFailedLease())
	}
	cleanupKey = false
	fmt.Fprintf(b.rt.Stderr, "provisioned lease=%s instance=%s target=linux state=ready\n", leaseID, name)
	return lease, nil
}

func validLinuxSSHUser(user string) bool {
	if user != strings.TrimSpace(user) || len(user) == 0 || len(user) > 32 {
		return false
	}
	for i, r := range user {
		if i == 0 {
			if (r >= 'a' && r <= 'z') || r == '_' {
				continue
			}
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (b *backend) createLinuxVM(ctx context.Context, cfg Config, name, seedPath string) error {
	vhdDir := hypervVHDDir()
	if err := os.MkdirAll(vhdDir, 0o755); err != nil {
		return exit(2, "create VHD directory %s: %v", vhdDir, err)
	}
	vmDir := hypervVMDir()
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return exit(2, "create VM directory %s: %v", vmDir, err)
	}
	vhdPath := filepath.Join(vhdDir, name+".vhdx")

	switchCheck := fmt.Sprintf(
		`if (-not (Get-VMSwitch -Name '%s' -ErrorAction SilentlyContinue)) { throw 'Hyper-V switch not found: %s' }`,
		escapePSString(cfg.HyperV.Switch), escapePSString(cfg.HyperV.Switch),
	)
	result, err := b.powershell(ctx, switchCheck)
	if err != nil {
		return commandError("switch validation", result, err)
	}

	diffScript := fmt.Sprintf(
		`New-VHD -Path '%s' -ParentPath '%s' -Differencing -ErrorAction Stop | Out-Null`,
		escapePSString(vhdPath), escapePSString(cfg.HyperV.Image),
	)
	result, err = b.powershell(ctx, diffScript)
	if err != nil {
		os.Remove(vhdPath) //nolint:errcheck
		return commandError("create differencing disk", result, err)
	}

	memBytes := int64(cfg.HyperV.Memory) * 1024 * 1024
	createScript := fmt.Sprintf(
		`New-VM -Name '%s' -MemoryStartupBytes %d -Generation 2 -VHDPath '%s' -Path '%s'`,
		escapePSString(name), memBytes, escapePSString(vhdPath), escapePSString(vmDir),
	)
	result, err = b.powershell(ctx, createScript)
	if err != nil {
		os.Remove(vhdPath) //nolint:errcheck
		return commandError("New-VM", result, err)
	}

	cpuScript := fmt.Sprintf(`Set-VM -Name '%s' -ProcessorCount %d -AutomaticCheckpointsEnabled $false`, escapePSString(name), cfg.HyperV.CPUs)
	result, err = b.powershell(ctx, cpuScript)
	if err != nil {
		return commandError("Set-VM", result, err)
	}
	if err := b.configureVMFirmware(ctx, cfg, name); err != nil {
		return err
	}

	seedScript := fmt.Sprintf(
		`Add-VMHardDiskDrive -VMName '%s' -ControllerType SCSI -ControllerNumber 0 -ControllerLocation 1 -Path '%s'`,
		escapePSString(name), escapePSString(seedPath),
	)
	result, err = b.powershell(ctx, seedScript)
	if err != nil {
		return commandError("attach NoCloud seed disk", result, err)
	}
	if err := b.connectVMNetwork(ctx, name, cfg.HyperV.Switch); err != nil {
		return err
	}

	startScript := fmt.Sprintf(`Start-VM -Name '%s'`, escapePSString(name))
	result, err = b.powershell(ctx, startScript)
	if err != nil {
		return commandError("Start-VM", result, err)
	}
	return nil
}
