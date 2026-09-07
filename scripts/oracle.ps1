<#
.SYNOPSIS
  Run YARG against a song library and compare its verdict with our scanner's.

.DESCRIPTION
  THE STANDARD THIS PROJECT HOLDS: every song YARG rejects should be one our
  scanner independently flags. Until now that comparison was a documented hand
  procedure - back up settings.json, repoint SongFolders, delete the cache,
  launch, wait, read badsongs.txt, eyeball it. Every one of those steps is a
  place to make the mistake this project has already made once, which is to read
  a STALE badsongs.txt as though it were this run's verdict.

  So this script refuses to report a result it did not watch happen:

    * It records the time before launching and requires the song cache to have
      been written AFTER it. Existence is not evidence of a write - in any run
      after the first, last run's files are already sitting at those paths.
    * It treats a MISSING badsongs.txt as a real and valid answer - "YARG
      rejected nothing" - rather than as a failure. That distinction matters:
      the happy case produces no file at all.
    * It exits non-zero when YARG rejected a song our scanner passed, because
      that is the standard being violated, and a script whose failure only
      reaches a log does not exist.

  It restores settings.json in a finally block, so a crash or a Ctrl-C still
  puts the operator's YARG back the way it was.

.PARAMETER Library
  Folder of songs to test. Passed to YARG as its only SongFolders entry.

.PARAMETER WaitSeconds
  How long to wait for YARG to finish scanning. A large library needs longer;
  the script polls and stops as soon as the cache settles.
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string] $Library,
  [int] $WaitSeconds = 180,
  [string] $ServerExe = "",
  [string] $YargExe = ""
)

$ErrorActionPreference = 'Stop'

function Fail($msg) { Write-Host "ORACLE FAILED: $msg" -ForegroundColor Red; exit 1 }

if (-not (Test-Path -LiteralPath $Library)) { Fail "library not found: $Library" }
$Library = (Resolve-Path -LiteralPath $Library).Path

if (-not $YargExe) { $YargExe = Join-Path $env:LOCALAPPDATA 'Programs\YARG\YARG.exe' }
if (-not (Test-Path -LiteralPath $YargExe)) { Fail "YARG not found at $YargExe (pass -YargExe)" }

$rel = Join-Path $env:USERPROFILE 'AppData\LocalLow\YARC\YARG\release'
$settings = Join-Path $rel 'settings.json'
$cache    = Join-Path $rel 'songcache.bin'
$bad      = Join-Path $rel 'badsongs.txt'
if (-not (Test-Path -LiteralPath $settings)) { Fail "settings.json not found at $settings; run YARG once first" }

# Our own scanner. Built fresh so the comparison is against the working tree
# rather than whatever binary happens to be lying around.
if (-not $ServerExe) {
  $ServerExe = Join-Path $env:TEMP 'oracle-yss.exe'
  $repo = Split-Path -Parent $PSScriptRoot
  Push-Location $repo
  try {
    & go build -o $ServerExe ./cmd/yarg-song-server
    if ($LASTEXITCODE -ne 0) { Fail "could not build the scanner" }
  } finally { Pop-Location }
}

$backup = "$settings.oracle-bak"
Copy-Item -LiteralPath $settings -Destination $backup -Force

try {
  # Point YARG at the library under test.
  $json = Get-Content -LiteralPath $settings -Raw | ConvertFrom-Json
  $json.SongFolders = @($Library)
  $json | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $settings -Encoding utf8

  Remove-Item -LiteralPath $cache -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $bad   -ErrorAction SilentlyContinue

  $launchAt = Get-Date
  Write-Host "launching YARG against $Library"
  $proc = Start-Process -FilePath $YargExe -PassThru

  # Wait for the CACHE, not for badsongs.txt. A library YARG is perfectly happy
  # with produces no badsongs.txt at all, so waiting on that file would hang on
  # exactly the outcome we most want to be able to report.
  $deadline = (Get-Date).AddSeconds($WaitSeconds)
  $lastSize = -1; $stableFor = 0
  while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds 3
    if (-not (Test-Path -LiteralPath $cache)) { continue }
    $fi = Get-Item -LiteralPath $cache
    if ($fi.LastWriteTime -le $launchAt) { continue }   # last run's file
    if ($fi.Length -eq $lastSize) { $stableFor += 3; if ($stableFor -ge 9) { break } }
    else { $lastSize = $fi.Length; $stableFor = 0 }
  }

  if (-not (Test-Path -LiteralPath $cache)) { Fail "YARG wrote no song cache; it did not scan" }
  $cacheInfo = Get-Item -LiteralPath $cache
  if ($cacheInfo.LastWriteTime -le $launchAt) {
    Fail "the song cache is older than this run ($($cacheInfo.LastWriteTime)); YARG did not rescan"
  }
  Write-Host ("song cache written: {0:N0} bytes at {1}" -f $cacheInfo.Length, $cacheInfo.LastWriteTime)
} finally {
  if ($proc -and -not $proc.HasExited) { $proc.CloseMainWindow() | Out-Null; Start-Sleep 2 }
  if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
  Copy-Item -LiteralPath $backup -Destination $settings -Force
  Remove-Item -LiteralPath $backup -ErrorAction SilentlyContinue
  Write-Host "settings.json restored"
}

# --- YARG's verdict -----------------------------------------------------------
# badsongs.txt is "Total Errors: N", then blank-line separated <path>/<reason>
# pairs. No file means it refused nothing, which is a result and not an error.
$rejects = @{}
if (Test-Path -LiteralPath $bad) {
  $bi = Get-Item -LiteralPath $bad
  if ($bi.LastWriteTime -le $launchAt) {
    Fail "badsongs.txt is older than this run; it is last run's verdict, not this one's"
  }
  $lines = Get-Content -LiteralPath $bad
  for ($i = 0; $i -lt $lines.Count; $i++) {
    $l = $lines[$i].Trim()
    if ($l -match '^[A-Za-z]:\\' -and $i + 1 -lt $lines.Count) {
      $rejects[(Split-Path $l -Leaf)] = $lines[$i + 1].Trim()
    }
  }
}
Write-Host "YARG refused $($rejects.Count) song(s)"

# --- our verdict --------------------------------------------------------------
$raw = & $ServerExe scan $Library 2>&1 | Out-String
$flagged = @{}
$total = 0
foreach ($m in [regex]::Matches($raw, '(?ms)^\{.*?^\}')) {
  try { $o = $m.Value | ConvertFrom-Json } catch { continue }
  $total++
  if ($o.issues -and $o.issues.Count -gt 0) {
    $flagged[(Split-Path $o.source_path -Leaf)] = ($o.issues | ForEach-Object { $_.code }) -join ','
  }
}
Write-Host "our scanner indexed $total song(s), flagged $($flagged.Count)"

# --- the comparison -----------------------------------------------------------
$missed = @()
foreach ($k in $rejects.Keys) {
  $hit = $flagged.Keys | Where-Object { $k -like "$_*" -or $_ -like "$k*" -or $_ -eq $k }
  if (-not $hit) { $missed += "$k  <- YARG: $($rejects[$k])" }
}
$extra = @($flagged.Keys | Where-Object { -not ($rejects.Keys -contains $_) })

Write-Host ""
Write-Host "=== agreement ==="
Write-Host ("  songs in library      : {0}" -f $total)
Write-Host ("  YARG refused          : {0}" -f $rejects.Count)
Write-Host ("  we flagged            : {0}" -f $flagged.Count)
Write-Host ("  YARG refused, we PASSED: {0}" -f $missed.Count)
Write-Host ("  we flagged, YARG took  : {0}" -f $extra.Count)

if ($extra.Count -gt 0) {
  # Not a failure. This project already has cases where the scanner is right to
  # flag something YARG will still load - a warning is allowed to be stricter
  # than a refusal.
  Write-Host ""
  Write-Host "we flagged these; YARG accepted them anyway (allowed, not a failure):"
  $extra | Select-Object -First 20 | ForEach-Object { Write-Host "  $_  ($($flagged[$_]))" }
}

if ($missed.Count -gt 0) {
  Write-Host ""
  Write-Host "THE STANDARD IS VIOLATED - YARG refused these and we did not flag them:" -ForegroundColor Red
  $missed | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
  exit 1
}

Write-Host ""
Write-Host "STANDARD HELD: every song YARG refused was one we independently flagged." -ForegroundColor Green
exit 0
