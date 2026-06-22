<#
  silly-review installer / updater for Windows (PowerShell 5+).

    irm https://raw.githubusercontent.com/dearlordlt/silly-review/main/setup.ps1 | iex

  Needs git. If a new-enough Go isn't found it downloads the official toolchain
  to a private dir (no admin). silly-review also needs the `claude` CLI at runtime.

  Env overrides: $env:INSTALL_DIR, $env:BRANCH, $env:REPO_URL
#>
$ErrorActionPreference = 'Stop'
# On PowerShell 7.4+ a non-zero native exit would otherwise throw before our
# explicit $LASTEXITCODE checks (git clone / go build) run — disable that so the
# friendly error messages below are what the user sees. (No-op on PS 5.1.)
$PSNativeCommandUseErrorActionPreference = $false

$RepoUrl    = if ($env:REPO_URL)    { $env:REPO_URL }    else { 'https://github.com/dearlordlt/silly-review' }
$Branch     = if ($env:BRANCH)      { $env:BRANCH }      else { 'main' }
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'silly-review\bin' }
$DataDir    = Join-Path $env:LOCALAPPDATA 'silly-review'
$Bin        = 'silly-review.exe'

function Info($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Warn($m) { Write-Host "warning: $m" -ForegroundColor Yellow }
function Have($c) { [bool](Get-Command $c -ErrorAction SilentlyContinue) }

if (-not (Have git)) { throw "git is required — install from https://git-scm.com/download/win and re-run." }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("silly-review-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
  Info "Cloning $RepoUrl ($Branch)…"
  git clone --quiet --depth 1 --branch $Branch $RepoUrl (Join-Path $tmp 'src')
  if ($LASTEXITCODE -ne 0) { throw "clone failed — check the repo URL and branch." }

  # Required Go floor from the cloned go.mod (e.g. "go 1.24.2").
  $minGo = ((Get-Content (Join-Path $tmp 'src\go.mod') | Where-Object { $_ -match '^go ' } | Select-Object -First 1) -replace '^go\s+', '').Trim()
  $mp = $minGo.Split('.')
  $minMajor = [int]$mp[0]; $minMinor = [int]$mp[1]
  $minPatch = if ($mp.Count -ge 3) { [int](($mp[2] -replace '\D.*$', '')) } else { 0 }

  # True if $goExe is >= the required floor (major.minor.patch). The patch
  # matters under GOTOOLCHAIN=local; prerelease (go1.25rc1) reads as 1.25.0.
  function GoOk($goExe) {
    try { $v = (& $goExe version) 2>$null } catch { return $false }
    if ("$v" -match 'go(\d+)\.(\d+)(?:\.(\d+))?') {
      $maj = [int]$Matches[1]; $min = [int]$Matches[2]
      $pat = if ($Matches[3]) { [int]$Matches[3] } else { 0 }
      if ($maj -ne $minMajor) { return ($maj -gt $minMajor) }
      if ($min -ne $minMinor) { return ($min -gt $minMinor) }
      return ($pat -ge $minPatch)
    }
    return $false
  }

  # Resolve a usable Go: PATH, then our private copy, then download.
  $go = $null
  if ((Have go) -and (GoOk 'go')) {
    $go = 'go'
  }
  elseif ((Test-Path "$DataDir\go\bin\go.exe") -and (GoOk "$DataDir\go\bin\go.exe")) {
    $go = "$DataDir\go\bin\go.exe"; Info "Using the Go previously installed at $DataDir\go."
  }
  else {
    # Prefer ARCHITEW6432 so a 32-bit (WOW64) PowerShell host reports the real
    # machine arch; map explicitly and fail on anything unsupported.
    $rawArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    $arch = switch ($rawArch) {
      'AMD64' { 'amd64' }
      'ARM64' { 'arm64' }
      'x86' { '386' }
      default { throw "unsupported CPU architecture '$rawArch' for automatic Go install — see https://go.dev/dl/" }
    }
    Info "Go $minMajor.$minMinor+ not found — downloading the official toolchain to $DataDir\go (no admin)…"
    $ver = (((Invoke-WebRequest -UseBasicParsing 'https://go.dev/VERSION?m=text').Content) -split "`n")[0].Trim()
    if ($ver -notmatch '^go') { throw "couldn't determine the latest Go version." }
    $zip = Join-Path $tmp 'go.zip'
    Invoke-WebRequest -UseBasicParsing "https://go.dev/dl/$ver.windows-$arch.zip" -OutFile $zip
    if (Test-Path "$DataDir\go") { Remove-Item -Recurse -Force "$DataDir\go" }
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
    Expand-Archive -Path $zip -DestinationPath $DataDir -Force  # creates $DataDir\go
    $go = "$DataDir\go\bin\go.exe"
    if (-not (GoOk $go)) { throw "Go install failed." }
    Info "Go ready at $DataDir\go (used only to build silly-review)."
  }

  Info "Building $Bin…"
  Push-Location (Join-Path $tmp 'src')
  try { $env:GOTOOLCHAIN = 'local'; & $go build -o (Join-Path $tmp $Bin) .; $code = $LASTEXITCODE }
  finally { Pop-Location }
  if ($code -ne 0) { throw "build failed." }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  $dst = Join-Path $InstallDir $Bin
  $oldVer = ''
  if (Test-Path $dst) { try { $oldVer = ((& $dst --version) -split '\s+')[-1] } catch { } }

  # Windows locks a running .exe but allows renaming it. Move it aside under a
  # UNIQUE name — a fixed ".old" could still be held by a prior running instance
  # and block the move — then drop the new one in.
  if (Test-Path $dst) {
    $aside = "$dst.old-" + [guid]::NewGuid().ToString('N')
    Move-Item -Force $dst $aside
    Remove-Item -Force $aside -ErrorAction SilentlyContinue # no-op if still running; swept later
  }
  Copy-Item -Force (Join-Path $tmp $Bin) $dst
  # Sweep asides freed since a previous run.
  Get-ChildItem -LiteralPath $InstallDir -Filter "$Bin.old-*" -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item -Force $_.FullName -ErrorAction SilentlyContinue }

  $newVer = ''
  try { $newVer = ((& $dst --version) -split '\s+')[-1] } catch { }
  if (-not $newVer) { $newVer = '(unknown)' }
  if (-not $oldVer) { Info "Installed silly-review $newVer -> $dst" }
  elseif ($oldVer -eq $newVer) { Info "Already up to date (silly-review $newVer)." }
  else { Info "Updated silly-review $oldVer -> $newVer" }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not $userPath) { $userPath = '' } # fresh profiles have no user PATH
  if (($userPath -split ';') -notcontains $InstallDir) {
    $newPath = if ($userPath) { $userPath.TrimEnd(';') + ';' + $InstallDir } else { $InstallDir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Warn "Added $InstallDir to your user PATH — open a new terminal for it to take effect."
  }

  if (-not (Have claude)) {
    Warn "the 'claude' CLI was not found. silly-review needs it (and you must be signed in)."
    Warn "Install Claude Code from https://claude.com/claude-code, then run 'claude' once to log in."
  }
  Info "Done. cd into a git repo (or a folder of repos) and run: silly-review"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
