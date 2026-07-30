package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// windowsInlineScriptLimit bounds how much PowerShell Crabbox will hand to a
// Windows guest through SSH stdin.
//
// Windows OpenSSH intermittently wedges its per-connection event loop while
// forwarding a large stdin payload to the remote child. When it happens the
// guest-side script never returns from reading stdin, the client stops
// receiving keepalive replies, and ssh aborts with
// "Timeout, server <host> not responding" even though the guest is healthy.
// Small payloads are reliable, so anything larger is copied and executed as a
// file instead.
const windowsInlineScriptLimit = 4096

// windowsScriptNeedsFileTransport reports whether a script is large enough that
// SSH stdin delivery is unsafe and it must be copied to the guest instead.
func windowsScriptNeedsFileTransport(script string) bool {
	return len(script) > windowsInlineScriptLimit
}

func runWindowsPowerShellScriptToWriter(ctx context.Context, target SSHTarget, script string, stdout io.Writer) error {
	return runWindowsPowerShellScript(ctx, target, script, stdout, nil)
}

func runWindowsPowerShellScript(ctx context.Context, target SSHTarget, script string, stdout, stderr io.Writer) error {
	if windowsScriptNeedsFileTransport(script) {
		return runWindowsPowerShellScriptFile(ctx, target, script, stdout, stderr)
	}
	command := `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$source=[Console]::In.ReadToEnd();try{& ([ScriptBlock]::Create($source));exit 0}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}"`
	var buffered bytes.Buffer
	errWriter := stderr
	if errWriter == nil {
		errWriter = &buffered
	}
	if err := runSSHInput(ctx, target, command, strings.NewReader(script), stdout, errWriter); err != nil {
		if detail := strings.TrimSpace(buffered.String()); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

// runWindowsPowerShellScriptFile copies the script to the guest and runs it
// with a short argument-based command, so no large payload crosses SSH stdin.
func runWindowsPowerShellScriptFile(ctx context.Context, target SSHTarget, script string, stdout, stderr io.Writer) error {
	localDir, err := os.MkdirTemp("", "crabbox-winps")
	if err != nil {
		return exit(2, "create temp script dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(localDir) }()

	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	localScript := filepath.Join(localDir, "script.ps1")
	// Windows PowerShell 5.1 only treats a -File script as UTF-8 when it starts
	// with a byte-order mark; without one, non-ASCII arguments are mangled.
	if err := os.WriteFile(localScript, append([]byte{0xEF, 0xBB, 0xBF}, []byte(script)...), 0o600); err != nil {
		return exit(2, "write Windows script: %v", err)
	}

	remoteDir := `C:\ProgramData\crabbox`
	remoteScript := "C:/ProgramData/crabbox/ps-" + token + ".ps1"
	remoteScriptPS := strings.ReplaceAll(remoteScript, "/", `\`)

	prepare := `powershell.exe -NoLogo -NoProfile -NonInteractive -Command "New-Item -ItemType Directory -Force -Path ` + psQuote(remoteDir) + ` | Out-Null"`
	if err := runSSHInput(ctx, target, prepare, nil, io.Discard, io.Discard); err != nil {
		return err
	}
	if err := copyLocalFileToTarget(ctx, target, localScript, remoteScript); err != nil {
		return err
	}

	command := `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "try{& ` +
		psQuote(remoteScriptPS) + `;$code=$LASTEXITCODE}finally{Remove-Item -Force -LiteralPath ` +
		psQuote(remoteScriptPS) + ` -ErrorAction SilentlyContinue};if($null -eq $code){$code=0};exit $code"`

	var buffered bytes.Buffer
	errWriter := stderr
	if errWriter == nil {
		errWriter = &buffered
	}
	if err := runSSHInput(ctx, target, command, nil, stdout, errWriter); err != nil {
		if detail := strings.TrimSpace(buffered.String()); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
