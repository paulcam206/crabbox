package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

func runWindowsPowerShellScriptToWriter(ctx context.Context, target SSHTarget, script string, stdout io.Writer) error {
	command := `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$source=[Console]::In.ReadToEnd();try{& ([ScriptBlock]::Create($source));exit 0}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}"`
	var stderr bytes.Buffer
	if err := runSSHInput(ctx, target, command, strings.NewReader(script), stdout, &stderr); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
