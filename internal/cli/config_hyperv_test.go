package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyFileHyperVConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := `hyperv:
  secureBoot: linux
  image: D:\images\win11.vhdx
  user: Administrator
  workRoot: D:\crabbox-work
  cpus: 6
  memory: 4096
  switch: Lab Switch
  guestPassword: file-secret
  initPassword: true
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := readFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	if err := applyFileConfig(&cfg, file); err != nil {
		t.Fatal(err)
	}
	if cfg.HyperV.Image != `D:\images\win11.vhdx` || cfg.HyperV.User != "Administrator" ||
		cfg.HyperV.WorkRoot != `D:\crabbox-work` || cfg.HyperV.CPUs != 6 || cfg.HyperV.Memory != 4096 ||
		cfg.HyperV.Switch != "Lab Switch" || cfg.HyperV.GuestPassword != "file-secret" {
		t.Fatalf("hyperv=%#v", cfg.HyperV)
	}
	if cfg.HyperV.SecureBoot != "linux" {
		t.Fatalf("secureBoot=%q", cfg.HyperV.SecureBoot)
	}
	if !cfg.HyperV.InitPassword {
		t.Fatal("hyperv.initPassword: true not applied from config file")
	}
}

// initPassword is a *bool in the file config so an absent key must leave the
// existing value alone while an explicit false must still win.
func TestApplyFileHyperVInitPasswordTriState(t *testing.T) {
	cfg := baseConfig()
	cfg.HyperV.InitPassword = true
	if err := applyFileConfig(&cfg, fileConfig{HyperV: &fileHyperVConfig{User: "keep"}}); err != nil {
		t.Fatal(err)
	}
	if !cfg.HyperV.InitPassword {
		t.Fatal("absent initPassword key must not reset the configured value")
	}
	explicitFalse := false
	if err := applyFileConfig(&cfg, fileConfig{HyperV: &fileHyperVConfig{InitPassword: &explicitFalse}}); err != nil {
		t.Fatal(err)
	}
	if cfg.HyperV.InitPassword {
		t.Fatal("explicit initPassword: false not applied")
	}
}

func TestHyperVEnvConfig(t *testing.T) {
	t.Setenv("CRABBOX_HYPERV_SECURE_BOOT", "off")
	t.Setenv("CRABBOX_HYPERV_IMAGE", `E:\img\base.vhdx`)
	t.Setenv("CRABBOX_HYPERV_USER", "EnvUser")
	t.Setenv("CRABBOX_HYPERV_WORK_ROOT", `E:\work`)
	t.Setenv("CRABBOX_HYPERV_CPUS", "8")
	t.Setenv("CRABBOX_HYPERV_MEMORY", "2048")
	t.Setenv("CRABBOX_HYPERV_SWITCH", "Env Switch")
	t.Setenv("CRABBOX_HYPERV_GUEST_PASSWORD", "env-secret")
	t.Setenv("CRABBOX_HYPERV_INIT_PASSWORD", "true")
	cfg := baseConfig()
	if err := applyEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HyperV.Image != `E:\img\base.vhdx` || cfg.HyperV.User != "EnvUser" ||
		cfg.HyperV.WorkRoot != `E:\work` || cfg.HyperV.CPUs != 8 || cfg.HyperV.Memory != 2048 ||
		cfg.HyperV.Switch != "Env Switch" || cfg.HyperV.GuestPassword != "env-secret" {
		t.Fatalf("hyperv=%#v", cfg.HyperV)
	}
	if cfg.HyperV.SecureBoot != "off" {
		t.Fatalf("secureBoot=%q", cfg.HyperV.SecureBoot)
	}
	if !cfg.HyperV.InitPassword {
		t.Fatal("CRABBOX_HYPERV_INIT_PASSWORD=true not applied")
	}
}

func TestHyperVEnvInitPasswordFalseOverridesTrue(t *testing.T) {
	t.Setenv("CRABBOX_HYPERV_INIT_PASSWORD", "false")
	cfg := baseConfig()
	cfg.HyperV.InitPassword = true
	if err := applyEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HyperV.InitPassword {
		t.Fatal("CRABBOX_HYPERV_INIT_PASSWORD=false must override a configured true")
	}
}

func TestLoadConfigAppliesHyperVWindowsDefaults(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("provider: hyperv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", path)
	t.Setenv("CRABBOX_PROVIDER", "")
	t.Setenv("CRABBOX_TARGET", "")
	t.Setenv("CRABBOX_TARGET_OS", "")
	t.Setenv("CRABBOX_WINDOWS_MODE", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetOS != targetWindows || cfg.WindowsMode != windowsModeNormal {
		t.Fatalf("target=%q windowsMode=%q, want windows/normal", cfg.TargetOS, cfg.WindowsMode)
	}
	if cfg.SSHUser != cfg.HyperV.User || cfg.SSHPort != "22" || cfg.WorkRoot != cfg.HyperV.WorkRoot {
		t.Fatalf("Hyper-V SSH defaults not applied: user=%q port=%q root=%q", cfg.SSHUser, cfg.SSHPort, cfg.WorkRoot)
	}
}

func TestLoadConfigPreservesExplicitHyperVTargetForCLIOverride(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("provider: hyperv\ntarget: linux\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", path)
	t.Setenv("CRABBOX_PROVIDER", "")
	t.Setenv("CRABBOX_TARGET", "")
	t.Setenv("CRABBOX_TARGET_OS", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetOS != targetLinux || !IsTargetExplicit(&cfg) {
		t.Fatalf("explicit target was rewritten: target=%q explicit=%v", cfg.TargetOS, IsTargetExplicit(&cfg))
	}
}

func TestLoadConfigAppliesHyperVLinuxDefaults(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("provider: hyperv\ntarget: linux\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", path)
	t.Setenv("CRABBOX_PROVIDER", "")
	t.Setenv("CRABBOX_TARGET", "")
	t.Setenv("CRABBOX_TARGET_OS", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetOS != targetLinux || cfg.WorkRoot != defaultPOSIXWorkRoot || cfg.HyperV.WorkRoot != defaultPOSIXWorkRoot {
		t.Fatalf("target=%q workRoot=%q hyperv.workRoot=%q, want Linux POSIX defaults", cfg.TargetOS, cfg.WorkRoot, cfg.HyperV.WorkRoot)
	}
	if cfg.SSHUser != "crabbox" || cfg.HyperV.SecureBoot != "auto" {
		t.Fatalf("user=%q secureBoot=%q", cfg.SSHUser, cfg.HyperV.SecureBoot)
	}
}

func TestLoadConfigPreservesExplicitHyperVLinuxWorkRoot(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "provider: hyperv\ntarget: linux\nhyperv:\n  workRoot: /srv/crabbox\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", path)
	t.Setenv("CRABBOX_PROVIDER", "")
	t.Setenv("CRABBOX_TARGET", "")
	t.Setenv("CRABBOX_TARGET_OS", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkRoot != "/srv/crabbox" || cfg.HyperV.WorkRoot != "/srv/crabbox" {
		t.Fatalf("workRoot=%q hyperv.workRoot=%q", cfg.WorkRoot, cfg.HyperV.WorkRoot)
	}
}

func TestLoadConfigPreservesExplicitHyperVLinuxDefaultWorkRoot(t *testing.T) {
	clearConfigEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "provider: hyperv\ntarget: linux\nworkRoot: /srv/global\nhyperv:\n  workRoot: /work/crabbox\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", path)
	t.Setenv("CRABBOX_PROVIDER", "")
	t.Setenv("CRABBOX_TARGET", "")
	t.Setenv("CRABBOX_TARGET_OS", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkRoot != defaultPOSIXWorkRoot || cfg.HyperV.WorkRoot != defaultPOSIXWorkRoot {
		t.Fatalf("workRoot=%q hyperv.workRoot=%q", cfg.WorkRoot, cfg.HyperV.WorkRoot)
	}
}
