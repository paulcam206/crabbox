package hyperv

import (
	"context"
	"fmt"
	"strings"
)

const (
	secureBootTemplateWindows = "MicrosoftWindows"
	secureBootTemplateLinux   = "MicrosoftUEFICertificateAuthority"
)

type firmwareSettings struct {
	enabled  bool
	template string
}

func normalizeSecureBootMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return secureBootAuto
	}
	return value
}

func secureBootSettings(targetOS, mode string) (firmwareSettings, error) {
	mode = normalizeSecureBootMode(mode)
	switch mode {
	case secureBootOff:
		return firmwareSettings{}, nil
	case secureBootWindows:
		return firmwareSettings{enabled: true, template: secureBootTemplateWindows}, nil
	case secureBootLinux:
		return firmwareSettings{enabled: true, template: secureBootTemplateLinux}, nil
	case secureBootAuto:
		switch targetOS {
		case "", targetWindows:
			return firmwareSettings{enabled: true, template: secureBootTemplateWindows}, nil
		case targetLinux:
			return firmwareSettings{enabled: true, template: secureBootTemplateLinux}, nil
		}
	default:
		return firmwareSettings{}, exit(2, "invalid --hyperv-secure-boot=%q; use auto, windows, linux, or off", mode)
	}
	return firmwareSettings{}, exit(2, "provider=%s cannot select secure boot firmware for target=%s", providerName, targetOS)
}

func (b *backend) configureVMFirmware(ctx context.Context, cfg Config, name string) error {
	settings, err := secureBootSettings(cfg.TargetOS, cfg.HyperV.SecureBoot)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(
		`Set-VMFirmware -VMName '%s' -EnableSecureBoot Off`,
		escapePSString(name),
	)
	if settings.enabled {
		script = fmt.Sprintf(
			`Set-VMFirmware -VMName '%s' -EnableSecureBoot On -SecureBootTemplate '%s'`,
			escapePSString(name), escapePSString(settings.template),
		)
	}
	result, err := b.powershell(ctx, script)
	if err != nil {
		return commandError("Set-VMFirmware", result, err)
	}
	return nil
}
