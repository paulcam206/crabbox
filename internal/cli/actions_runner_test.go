package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunActionsRunnerSetupReportsLastStageWithoutPrintingDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	const fixtureValue = "fixture"
	var output bytes.Buffer
	result, err := runActionsRunnerSetup(ctx, &output, func(_, stderr io.Writer) error {
		fmt.Fprintln(stderr, actionsRunnerStagePrefix+"archive-download")
		fmt.Fprintln(stderr, "download failed with "+fixtureValue)
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
	if result.LastStage != "archive-download" {
		t.Fatalf("last stage=%q", result.LastStage)
	}
	if strings.Contains(output.String(), fixtureValue) {
		t.Fatalf("progress output exposed fixture: %q", output.String())
	}
	if got := redactActionsRunnerText(result.Diagnostics, fixtureValue); strings.Contains(got, fixtureValue) || got == result.Diagnostics {
		t.Fatalf("redacted diagnostics=%q", got)
	}
}

func TestRunGitHubRunnerHydrationDeadlineDuringRegistrationPreventsDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	dispatched := false
	_, err := runGitHubRunnerHydration(
		ctx,
		"hydrate.yml",
		"main",
		"hydrate",
		[]string{"crabbox_job=hydrate"},
		io.Discard,
		githubRunnerHydrationHooks{
			Register: func(stageCtx context.Context) error {
				<-stageCtx.Done()
				return stageCtx.Err()
			},
			ClearState: func(context.Context) error {
				t.Fatal("clear ran after registration timeout")
				return nil
			},
			Inputs: func(context.Context) (map[string]bool, bool, error) {
				t.Fatal("input inspection ran after registration timeout")
				return nil, false, nil
			},
			Dispatch: func(context.Context, []string) error {
				dispatched = true
				return nil
			},
			Wait: func(context.Context, string) (actionsHydrationState, error) {
				t.Fatal("marker wait ran after registration timeout")
				return actionsHydrationState{}, nil
			},
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
	if dispatched {
		t.Fatal("workflow dispatched after registration timeout")
	}
}

func TestRunGitHubRunnerHydrationUsesSameDeadlineThroughMarkerWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatched := 0
	gotExpectedJob := ""
	state, err := runGitHubRunnerHydration(
		ctx,
		"hydrate.yml",
		"feature",
		"hydrate",
		[]string{
			"crabbox_id=cbx_test",
			"crabbox_runner_label=runner",
			"crabbox_keep_alive_minutes=90",
			"crabbox_job=hydrate",
		},
		io.Discard,
		githubRunnerHydrationHooks{
			Register:   func(context.Context) error { return nil },
			ClearState: func(context.Context) error { return nil },
			Inputs: func(context.Context) (map[string]bool, bool, error) {
				return map[string]bool{
					"crabbox_id":                 true,
					"crabbox_runner_label":       true,
					"crabbox_keep_alive_minutes": true,
					"crabbox_job":                true,
				}, true, nil
			},
			Dispatch: func(stageCtx context.Context, _ []string) error {
				if _, ok := stageCtx.Deadline(); !ok {
					t.Fatal("dispatch context lost hydration deadline")
				}
				dispatched++
				return nil
			},
			Wait: func(stageCtx context.Context, expectedJob string) (actionsHydrationState, error) {
				if _, ok := stageCtx.Deadline(); !ok {
					t.Fatal("marker wait context lost hydration deadline")
				}
				gotExpectedJob = expectedJob
				return actionsHydrationState{Workspace: `C:\workspace`}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspace != `C:\workspace` || dispatched != 1 || gotExpectedJob != "hydrate" {
		t.Fatalf("state=%+v dispatched=%d expectedJob=%q", state, dispatched, gotExpectedJob)
	}
}

func TestActionsCommandsRejectNonPositiveWaitTimeout(t *testing.T) {
	app := App{Stdout: io.Discard, Stderr: io.Discard}
	for name, run := range map[string]func() error{
		"hydrate": func() error {
			return app.actionsHydrate(context.Background(), []string{"--wait-timeout", "0"})
		},
		"register": func() error {
			return app.actionsRegister(context.Background(), []string{"--wait-timeout", "0"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil || !strings.Contains(err.Error(), "--wait-timeout must be greater than zero") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGitHubActionsRunnerInstallPowerShellScriptParses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell parser is required")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is required")
	}
	scriptPath := filepath.Join(t.TempDir(), "runner-install.ps1")
	if err := os.WriteFile(scriptPath, []byte(githubActionsRunnerInstallPowerShellScript("latest", true)), 0o600); err != nil {
		t.Fatal(err)
	}
	command := `$syntaxItems = $null
$errors = $null
[System.Management.Automation.Language.Parser]::ParseFile($env:CRABBOX_RUNNER_SCRIPT, [ref]$syntaxItems, [ref]$errors) | Out-Null
if ($errors.Count -gt 0) {
  $errors | ForEach-Object { $_.Message }
  exit 1
}`
	cmd := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Env = append(os.Environ(), "CRABBOX_RUNNER_SCRIPT="+scriptPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Windows runner installer does not parse: %v\n%s", err, output)
	}
}

func TestGitHubActionsRunnerConfigPowerShellIgnoresRemovalTimeout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell is required")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is required")
	}
	runnerDir := t.TempDir()
	configPath := filepath.Join(runnerDir, "config.cmd")
	configScript := "@echo off\r\necho removal-started\r\nping 127.0.0.1 -n 8 >nul\r\nexit /b 1\r\n"
	if err := os.WriteFile(configPath, []byte(configScript), 0o600); err != nil {
		t.Fatal(err)
	}
	testScript := `$ErrorActionPreference = "Stop"
$runnerDir = $env:CRABBOX_RUNNER_TEST_DIR
Set-Location -LiteralPath $runnerDir
` + githubActionsRunnerConfigPowerShell() + `
Invoke-CrabboxRunnerConfig -Arguments @("remove") -TimeoutSeconds 1 -LogName "remove.log" -IgnoreFailure
Write-Output "continued-after-timeout"
`
	scriptPath := filepath.Join(t.TempDir(), "runner-config-test.ps1")
	if err := os.WriteFile(scriptPath, []byte(testScript), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.Env = append(os.Environ(), "CRABBOX_RUNNER_TEST_DIR="+runnerDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("removal timeout should be ignored: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "continued-after-timeout") {
		t.Fatalf("script did not continue after removal timeout:\n%s", output)
	}
	log, err := os.ReadFile(filepath.Join(runnerDir, "remove.log"))
	if err != nil {
		t.Fatalf("read removal timeout log: %v", err)
	}
	if !strings.Contains(string(log), "removal-started") {
		t.Fatalf("removal timeout log lost command output: %q", log)
	}
}

func TestActionsRunnerBootstrapContextReservesDiagnosticTime(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelParent()
	bootstrap, cancelBootstrap := actionsRunnerBootstrapContext(parent)
	defer cancelBootstrap()
	parentDeadline, parentOK := parent.Deadline()
	bootstrapDeadline, bootstrapOK := bootstrap.Deadline()
	if !parentOK || !bootstrapOK {
		t.Fatal("expected both contexts to have deadlines")
	}
	if !bootstrapDeadline.Before(parentDeadline) {
		t.Fatalf("bootstrap deadline=%s parent deadline=%s", bootstrapDeadline, parentDeadline)
	}
}

func TestReadGitHubActionsRunnerDiagnosticsSkipsExpiredBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := readGitHubActionsRunnerDiagnosticsBestEffort(ctx, SSHTarget{})
	if !strings.Contains(got, "timeout budget exhausted") {
		t.Fatalf("diagnostics=%q", got)
	}
}

func TestWaitForGitHubActionsRunnerOnlineRetriesUntilOnline(t *testing.T) {
	repo := GitHubRepo{Owner: "example-org", Name: "my-app"}
	calls := 0
	var output bytes.Buffer
	err := waitForGitHubActionsRunnerOnline(
		context.Background(),
		repo,
		"runner-one",
		time.Millisecond,
		func(context.Context) (githubActionsRunnerState, error) {
			calls++
			if calls < 3 {
				return githubActionsRunnerState{Found: true, Status: "offline"}, nil
			}
			return githubActionsRunnerState{Found: true, Status: "online"}, nil
		},
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("lookup calls=%d, want 3", calls)
	}
	if !strings.Contains(output.String(), "status=offline") || !strings.Contains(output.String(), "actions runner online") {
		t.Fatalf("unexpected progress output: %q", output.String())
	}
}

func TestWaitForGitHubActionsRunnerOnlineWaitsWhileBusy(t *testing.T) {
	repo := GitHubRepo{Owner: "example-org", Name: "my-app"}
	calls := 0
	var output bytes.Buffer
	err := waitForGitHubActionsRunnerOnline(
		context.Background(),
		repo,
		"runner-busy",
		time.Millisecond,
		func(context.Context) (githubActionsRunnerState, error) {
			calls++
			return githubActionsRunnerState{
				Found:  true,
				Status: "online",
				Busy:   calls < 3,
			}, nil
		},
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("lookup calls=%d, want 3", calls)
	}
	if !strings.Contains(output.String(), "status=online busy=true") {
		t.Fatalf("busy runner wait was not reported: %q", output.String())
	}
}

func TestWaitForGitHubActionsRunnerOnlineTimesOutWithLastStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := waitForGitHubActionsRunnerOnline(
		ctx,
		GitHubRepo{Owner: "example-org", Name: "my-app"},
		"runner-two",
		time.Millisecond,
		func(context.Context) (githubActionsRunnerState, error) {
			return githubActionsRunnerState{Found: true, Status: "offline"}, nil
		},
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "last status=offline") {
		t.Fatalf("error=%v, want offline timeout", err)
	}
}

func TestParseGitHubActionsRunnerStateSelectsExactName(t *testing.T) {
	got := parseGitHubActionsRunnerState(
		"runner-one\toffline\tfalse\nrunner-two\tonline\ttrue\n",
		"runner-two",
	)
	if !got.Found || got.Status != "online" || !got.Busy {
		t.Fatalf("state=%+v", got)
	}
}

func TestWaitForActionsHydrationIncludesLastProbeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := waitForActionsHydrationWithProbe(
		ctx,
		"cbx_test",
		"hydrate",
		time.Millisecond,
		io.Discard,
		func(context.Context) (actionsHydrationState, error) {
			return actionsHydrationState{}, errors.New("ssh probe failed")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "last SSH probe error: ssh probe failed") {
		t.Fatalf("error=%v", err)
	}
}

func TestWaitForActionsHydrationClearsRecoveredProbeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := waitForActionsHydrationWithProbe(
		ctx,
		"cbx_test",
		"hydrate",
		time.Millisecond,
		io.Discard,
		func(context.Context) (actionsHydrationState, error) {
			calls++
			if calls == 1 {
				return actionsHydrationState{}, errors.New("temporary ssh failure")
			}
			return actionsHydrationState{}, nil
		},
	)
	if err == nil {
		t.Fatal("expected marker timeout")
	}
	if strings.Contains(err.Error(), "temporary ssh failure") {
		t.Fatalf("timeout retained recovered probe error: %v", err)
	}
}
