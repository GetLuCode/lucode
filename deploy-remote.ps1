<#
.SYNOPSIS
  Builds the lucode hub and lucode-runner for Linux from the working tree, deploys them to a remote
  machine and restarts them exactly as they were running.

.DESCRIPTION
  .\deploy-remote.ps1                 build both, copy both, restart what is running there
  .\deploy-remote.ps1 -BuildOnly      just build (tmp\remote\), copy nothing
  .\deploy-remote.ps1 -NoRestart      copy only: the new binaries stay next to the old ones as <name>.new
  .\deploy-remote.ps1 -Web            also rebuild the web UI first (npm run build -> web\dist)
  .\deploy-remote.ps1 -Force          restart the runner even if claude processes are running there
  .\deploy-remote.ps1 -RunnerOnly | -HubOnly

  Restart: the script does not know how the hub or the runner are started, it asks the running process.
  It reads the executable, folder, log file, arguments and environment of the process that is running,
  stops it, puts the new binary in the same path (the old one stays as <name>.prev) and starts it again
  with the same arguments. Nothing about NATS or tokens goes through this script. If the new process
  does not come up, the old binary is put back and started again, and the script fails.
  If nothing is running there, the binary is copied and not started: only you know how to start it.

  The hub is restarted first, then the runner (which reconnects to it). Restarting the runner kills the
  claude processes it runs, so it refuses while any is running unless -Force; in that case nothing at all
  is touched.

  Environment variables:
      REMOTE_HOST      required to deploy: user@host of the remote machine (ssh/scp target)
      REMOTE_DIR       optional, default lucode-runner: where the runner's file is copied (relative to the remote home)
      REMOTE_HUB_DIR   optional, default lucode-hub: where the hub's file is copied (relative to the remote home)
      REMOTE_HUB_BIN   optional, default lucode-hub: the hub's file name, which is also its process name
    -BuildOnly needs none of them. A running process is replaced where it runs, whatever these say.

  cmd\lucode\embedded\lucodehook is what the Windows hub (air) embeds: it is saved and restored, so a
  Linux build never leaves a Linux hook behind.
#>
[CmdletBinding()]
param(
    [switch]$Web,
    [switch]$BuildOnly,
    [switch]$NoRestart,
    [switch]$Force,
    [switch]$RunnerOnly,
    [switch]$HubOnly
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-Setting($name, $default) { $v = [Environment]::GetEnvironmentVariable($name); if ($v) { $v } else { $default } }
$RemoteHost = Get-Setting 'REMOTE_HOST' ''
$RemoteDir = Get-Setting 'REMOTE_DIR' 'lucode-runner'
$RemoteHubDir = Get-Setting 'REMOTE_HUB_DIR' 'lucode-hub'
$RemoteHubBin = Get-Setting 'REMOTE_HUB_BIN' 'lucode-hub'

if ($RunnerOnly -and $HubOnly) { throw '-RunnerOnly and -HubOnly exclude each other' }
$wantRunner = -not $HubOnly
$wantHub = -not $RunnerOnly

# fail before building when the deploy cannot happen
if (-not $BuildOnly -and -not $RemoteHost) {
    throw 'set REMOTE_HOST (user@host of the remote machine), e.g. $env:REMOTE_HOST = "user@host"'
}

Set-Location $PSScriptRoot
$out = Join-Path 'tmp' 'remote'
New-Item -ItemType Directory -Force $out, 'cmd/lucode/embedded', 'cmd/lucode-runner/embedded' | Out-Null

# A native command that fails must stop the script: $ErrorActionPreference does not cover them.
function Invoke-Native {
    param([string]$What, [scriptblock]$Cmd)
    & $Cmd
    if ($LASTEXITCODE -ne 0) { throw "$What failed (exit $LASTEXITCODE)" }
}

function Invoke-GoLinux {
    param([string[]]$GoArgs)
    $saved = @{}
    foreach ($k in 'CGO_ENABLED', 'GOOS', 'GOARCH') { $saved[$k] = [Environment]::GetEnvironmentVariable($k) }
    try {
        $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'amd64'
        Invoke-Native "go $($GoArgs -join ' ')" { go @GoArgs }
    }
    finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
}

if ($Web) {
    Write-Host '==> web (npm run build)'
    Push-Location web
    try { Invoke-Native 'npm run build' { npm run build } } finally { Pop-Location }
}
if (-not (Test-Path 'web/dist')) { throw 'web/dist is missing: run with -Web' }

Write-Host '==> lucodehook (linux)'
$hookLinux = Join-Path $out 'lucodehook-linux'
Invoke-GoLinux @('build', '-ldflags', '-s -w', '-o', $hookLinux, './cmd/lucodehook')

if ($wantRunner) {
    Write-Host '==> lucode-runner (linux)'
    Copy-Item -Force $hookLinux 'cmd/lucode-runner/embedded/lucodehook-linux'
    Invoke-GoLinux @('build', '-ldflags', '-s -w', '-o', (Join-Path $out 'lucode-runner'), './cmd/lucode-runner')
}

if ($wantHub) {
    Write-Host '==> lucode hub (linux)'
    $hook = 'cmd/lucode/embedded/lucodehook'
    $backup = $null
    if (Test-Path $hook) { $backup = New-TemporaryFile; Copy-Item -Force $hook $backup }
    try {
        Copy-Item -Force $hookLinux $hook
        Invoke-GoLinux @('build', '-ldflags', '-s -w', '-o', (Join-Path $out 'lucode'), './cmd/lucode')
    }
    finally {
        if ($backup) { Copy-Item -Force $backup $hook; Remove-Item -Force $backup } else { Remove-Item -Force -ErrorAction SilentlyContinue $hook }
    }
}

Write-Host 'built:'
Get-ChildItem $out | Where-Object { $_.Name -in 'lucode', 'lucode-runner' } |
    ForEach-Object { '{0,12:N0}  {1}' -f $_.Length, $_.FullName }
if ($BuildOnly) { return }

function Invoke-Ssh { ssh -o ConnectTimeout=8 $RemoteHost @args }

Invoke-Ssh true
if ($LASTEXITCODE -ne 0) { throw "cannot reach $RemoteHost" }

function Send-File($local, $remote) {
    Invoke-Native "scp $local" { scp -q $local "${RemoteHost}:$remote" }
}

if ($wantHub) {
    Write-Host "==> copy hub -> ${RemoteHost}:$RemoteHubDir/$RemoteHubBin.new"
    Invoke-Native 'mkdir' { Invoke-Ssh "mkdir -p $RemoteHubDir" }
    Send-File (Join-Path $out 'lucode') "$RemoteHubDir/$RemoteHubBin.new"
}
if ($wantRunner) {
    Write-Host "==> copy runner -> ${RemoteHost}:$RemoteDir/lucode-runner.new"
    Invoke-Native 'mkdir' { Invoke-Ssh "mkdir -p $RemoteDir" }
    Send-File (Join-Path $out 'lucode-runner') "$RemoteDir/lucode-runner.new"
}
if ($NoRestart) {
    Write-Host 'copied as <name>.new; nothing was restarted'
    return
}

Write-Host '==> restart what is running there'
# The remote half is bash. It reads the running processes (/proc) and restarts them as they were.
$remote = @'
set -u
HUB_DIR="__HUB_DIR__"; HUB_BIN="__HUB_BIN__"; RUNNER_DIR="__RUNNER_DIR__"
DO_HUB=__DO_HUB__; DO_RUNNER=__DO_RUNNER__; FORCE=__FORCE__
cd ~
say() { echo "[remote] $*"; }

# What is running under that process name: PID, EXE, CWD, LOG, ENVF and the ARGS array.
grab() {
  PID=$(pgrep -x "$1" | head -n1 || true); ARGS=(); EXE=""; CWD=""; LOG=""; ENVF=""
  [ -n "$PID" ] || return 1
  EXE=$(readlink "/proc/$PID/exe"); CWD=$(readlink "/proc/$PID/cwd"); LOG=$(readlink "/proc/$PID/fd/1" || true)
  case "$LOG" in /*) [ -f "$LOG" ] || LOG="" ;; *) LOG="" ;; esac
  while IFS= read -r -d '' a; do ARGS+=("$a"); done < "/proc/$PID/cmdline"
  ENVF=$(mktemp); tr '\0' '\n' < "/proc/$PID/environ" > "$ENVF"
  return 0
}

stop_pid() {
  kill "$1" 2>/dev/null || true
  for _ in $(seq 1 20); do kill -0 "$1" 2>/dev/null || return 0; sleep 1; done
  return 1
}

# launch CWD LOG ENVF EXE ARGS...: the same environment and arguments, appending to the same log.
launch() {
  local cwd=$1 log=$2 envf=$3 exe=$4; shift 4
  ( cd "$cwd"
    while IFS= read -r l; do case "$l" in [A-Za-z_]*=*) export "$l" ;; esac; done < "$envf"
    nohup "$exe" "$@" >> "$log" 2>&1 < /dev/null & )
}

# replace NAME DIR: the new file goes to where the running one is (or DIR/NAME when none runs); the old stays as .prev
install_new() {
  local target=$1 new=$2
  { [ -f "$target" ] && cp -f "$target" "$target.prev" || true; }
  mv -f "$new" "$target" && chmod +x "$target"
}
restore_old() { [ -f "$1.prev" ] && mv -f "$1.prev" "$1"; }

# redeploy NAME NEWFILE DEFAULT_TARGET WAIT [READY_TEXT]
redeploy() {
  local name=$1 new=$2 default=$3 wait=$4 ready=${5:-}
  if ! grab "$name"; then
    install_new "$default" "$new"
    say "$name is not running there: copied to $default, not started"
    return 0
  fi
  local exe=$EXE cwd=$CWD envf=$ENVF pid=$PID size=0 log=$LOG
  [ -n "$log" ] || log="$cwd/$name.log"
  local -a args=("${ARGS[@]:1}")
  say "restarting $name (pid $pid)"
  stop_pid "$pid" || { say "$name did not stop in 20 s: left as it was"; return 1; }
  install_new "$exe" "$new"
  size=$(stat -c %s "$log" 2>/dev/null || echo 0)
  launch "$cwd" "$log" "$envf" "$exe" "${args[@]}"
  sleep "$wait"
  if pgrep -x "$name" >/dev/null && { [ -z "$ready" ] || tail -c +$((size + 1)) "$log" | grep -q "$ready"; }; then
    say "$name running (pid $(pgrep -x "$name" | head -n1))"
    return 0
  fi
  say "$name did not come up; last lines of its log:"
  tail -n 12 "$log" | sed -E 's#(ws://)[^@/ ]*@#\1***@#g'
  pkill -x "$name" 2>/dev/null || true
  sleep 2
  if restore_old "$exe"; then
    size=$(stat -c %s "$log" 2>/dev/null || echo 0)
    launch "$cwd" "$log" "$envf" "$exe" "${args[@]}"
    sleep "$wait"
    pgrep -x "$name" >/dev/null && say "the previous $name is running again" || say "the previous $name did not start either: look at $log"
  fi
  return 1
}

if [ "$DO_RUNNER" = 1 ] && [ "$FORCE" = 0 ]; then
  busy=$(pgrep -c -x claude || true)
  if [ "${busy:-0}" != 0 ]; then
    say "$busy claude process(es) are running: restarting the runner would kill them. Nothing was touched; use -Force."
    exit 3
  fi
fi

rc=0
if [ "$DO_HUB" = 1 ]; then redeploy "$HUB_BIN" "$HUB_DIR/$HUB_BIN.new" "$HOME/$HUB_DIR/$HUB_BIN" 8 || rc=1; fi
if [ "$DO_RUNNER" = 1 ] && [ "$rc" = 0 ]; then redeploy lucode-runner "$RUNNER_DIR/lucode-runner.new" "$HOME/$RUNNER_DIR/lucode-runner" 14 "\[runner\] ready" || rc=1; fi
exit $rc
'@
$remote = $remote.Replace('__HUB_DIR__', $RemoteHubDir).Replace('__HUB_BIN__', $RemoteHubBin).Replace('__RUNNER_DIR__', $RemoteDir)
$remote = $remote.Replace('__DO_HUB__', [int]$wantHub).Replace('__DO_RUNNER__', [int]$wantRunner).Replace('__FORCE__', [int][bool]$Force)
$remote = $remote.Replace("`r`n", "`n")
# base64, not stdin: PowerShell ends a piped string with CRLF, which broke the last line of a script
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($remote))
Invoke-Ssh "echo $b64 | base64 -d | bash -s"
switch ($LASTEXITCODE) {
    0 { Write-Host 'done' }
    3 { Write-Error 'not deployed: claude processes are running on the remote (see above). The new binaries are copied as <name>.new.'; exit 1 }
    default { throw 'the restart failed (see above); the previous version was put back where possible' }
}
