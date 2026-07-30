package cli

func windowsInteractiveScheduledTaskPowerShell() string {
	return `
function Protect-CrabboxInteractiveDirectory([string]$Path) {
  New-Item -ItemType Directory -Force -Path $Path | Out-Null
  $userSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  & icacls.exe $Path /inheritance:r /grant:r "*${userSID}:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" "*S-1-5-18:(OI)(CI)F" | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "failed to protect interactive task directory"
  }
}
function Remove-CrabboxInteractiveTask([string]$TaskName) {
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
}
function Register-CrabboxInteractiveTask([string]$TaskName, [string]$Execute, [string]$Arguments) {
  Remove-CrabboxInteractiveTask $TaskName
  $action = New-ScheduledTaskAction -Execute $Execute -Argument $Arguments -WorkingDirectory "C:\ProgramData\crabbox"
  $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::FromMinutes(10)) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
  $userID = "$env:COMPUTERNAME\$env:USERNAME"
  $principal = New-ScheduledTaskPrincipal -UserId $userID -LogonType Interactive -RunLevel Highest
  $task = New-ScheduledTask -Action $action -Principal $principal -Settings $settings
  Register-ScheduledTask -TaskName $TaskName -InputObject $task -Force | Out-Null
}
function Start-CrabboxInteractiveTask([string]$TaskName) {
  Start-ScheduledTask -TaskName $TaskName -ErrorAction Stop
}
`
}
