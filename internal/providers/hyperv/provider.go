package hyperv

import (
	"flag"

	core "github.com/openclaw/crabbox/internal/cli"
)

func init() {
	core.RegisterProvider(Provider{})
}

type Provider struct{}

func (Provider) Name() string { return providerName }

func (Provider) Aliases() []string { return nil }

func (Provider) Spec() core.ProviderSpec {
	return core.ProviderSpec{
		Name:     providerName,
		Family:   "local-vm",
		Kind:     core.ProviderKindSSHLease,
		Targets:  providerTargets(),
		Features: providerFeatures(),
		TargetFeatures: map[string]core.FeatureSet{
			core.TargetLinux: append(providerCommonFeatures(), core.FeatureCode),
		},
		Coordinator: core.CoordinatorNever,
	}
}

func providerTargets() []core.TargetSpec {
	return []core.TargetSpec{
		{OS: core.TargetLinux},
		{OS: core.TargetWindows, WindowsMode: core.WindowsModeNormal},
	}
}

func providerCommonFeatures() core.FeatureSet {
	return core.FeatureSet{
		core.FeatureSSH,
		core.FeatureCrabboxSync,
		core.FeatureDesktop,
		core.FeatureBrowser,
		core.FeatureCleanup,
		core.FeaturePauseResume,
	}
}

func providerFeatures() core.FeatureSet {
	return append(providerCommonFeatures(),
		core.FeatureCheckpoint,
		core.FeatureFork,
		core.FeatureRestore,
		core.FeatureSnapshot,
	)
}

func (Provider) RegisterFlags(fs *flag.FlagSet, defaults core.Config) any {
	return registerFlags(fs, defaults)
}

func (Provider) ApplyFlags(cfg *core.Config, fs *flag.FlagSet, values any) error {
	return applyFlags(cfg, fs, values)
}

func (p Provider) Configure(cfg core.Config, rt core.Runtime) (core.Backend, error) {
	targetOS := cfg.TargetOS
	if targetOS == "" {
		targetOS = core.TargetWindows
	}
	if targetOS != core.TargetLinux && targetOS != core.TargetWindows {
		return nil, core.Exit(2, "provider=%s supports target=linux or target=windows", providerName)
	}
	if targetOS == core.TargetWindows && cfg.WindowsMode != "" && cfg.WindowsMode != core.WindowsModeNormal {
		return nil, core.Exit(2, "provider=%s supports windows.mode=normal only", providerName)
	}
	if targetOS == core.TargetLinux && cfg.HyperV.InitPassword {
		return nil, core.Exit(2, "--hyperv-init-password is supported only for target=windows")
	}
	if _, err := secureBootSettings(targetOS, cfg.HyperV.SecureBoot); err != nil {
		return nil, err
	}
	if cfg.Tailscale.Enabled || string(cfg.Network) == "tailscale" {
		return nil, core.Exit(2, "--tailscale is not supported for provider=%s; use a remote SSH provider when tailnet reachability is required", providerName)
	}
	return newBackend(p.Spec(), cfg, rt), nil
}

func (p Provider) ConfigureDoctor(cfg core.Config, rt core.Runtime) (core.DoctorBackend, error) {
	backend, err := p.Configure(cfg, rt)
	if err != nil {
		return nil, err
	}
	doctor, ok := backend.(core.DoctorBackend)
	if !ok {
		return nil, core.Exit(2, "%s doctor backend unavailable", providerName)
	}
	return doctor, nil
}
