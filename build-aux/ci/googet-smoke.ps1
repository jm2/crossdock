# SPDX-License-Identifier: GPL-3.0-or-later
#
# The landing page's GooGet snippet, run for real against a goo repo served
# by `crossdock serve`, on a Windows CI runner without GooGet (it installs
# GooGet under %ProgramData%, so not on your own machine). It:
#
#   1. builds a small .goo with tar.exe and publishes it
#   2. runs the snippet in Windows PowerShell as pasted commands, under
#      Windows client's default execution policy (Restricted): googet is
#      missing, so its if block installs GooGet from Google's repo; the
#      page comes over plain HTTP, so the snippet sets allowunsafeurl in
#      googet.conf; and the rest adds the crossdock repo
#   3. installs the package from the repo and checks its file
#   4. runs the if block again, which must leave GooGet alone
#
#   $env:CROSSDOCK = '.\crossdock.exe'; build-aux/ci/googet-smoke.ps1
#
# Failures are printed as GitHub Actions error annotations.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$crossdock = if ($env:CROSSDOCK) { $env:CROSSDOCK } else { 'crossdock' }
$work = Join-Path ([IO.Path]::GetTempPath()) "googet-smoke-$PID"
$base = 'http://127.0.0.1:8088'
# paste runs code the way pasting it into Windows PowerShell does: as
# commands, not a script file, under Restricted, which refuses script files
# and script modules. It returns powershell.exe's exit code.
function paste([string]$code) {
  powershell.exe -NoProfile -ExecutionPolicy Restricted -EncodedCommand ([Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($code))) | Out-Host
  $LASTEXITCODE
}
$root = Join-Path $env:ProgramData GooGet
function Fail([string]$msg) {
  Write-Output "::error title=googet smoke::$msg"
  exit 1
}

if (Get-Command googet -ErrorAction SilentlyContinue) { Fail 'googet is already installed; this needs a machine without it' }
[Environment]::OSVersion.VersionString

# 1. A package that installs one file. pwsh writes UTF-8 without a BOM.
$stage = New-Item -ItemType Directory "$work\stage"
New-Item -ItemType Directory "$work\in" | Out-Null
Set-Content "$stage\hello.txt" 'crossdock smoke'
@{
  Name = 'crossdock-smoke'; Version = '1.0.0@1'; Arch = 'noarch'; Description = 'crossdock CI'
  Files = @{ 'hello.txt' = '<ProgramData>/crossdock-smoke/hello.txt' }
} | ConvertTo-Json | Set-Content "$stage\crossdock-smoke.pkgspec"
tar.exe -czf "$work\in\crossdock-smoke.noarch.1.0.0@1.goo" -C $stage hello.txt crossdock-smoke.pkgspec
if ($LASTEXITCODE) { Fail 'tar.exe could not build the .goo' }

$w = $work -replace '\\', '/'
Set-Content "$work\crossdock.yaml" @"
output: $w/out
repos:
  - name: smoke
    format: goo
    source: $w/in
"@
& $crossdock publish -q -allow-unsigned -c "$work\crossdock.yaml"
if ($LASTEXITCODE) { Fail 'crossdock publish failed' }
$serve = Start-Process $crossdock -ArgumentList 'serve', '-c', "$work\crossdock.yaml", '-listen', '127.0.0.1:8088' `
  -PassThru -NoNewWindow -RedirectStandardOutput "$work\access.log" -RedirectStandardError "$work\serve.log"
try {
  foreach ($i in 1..50) {
    try { Invoke-WebRequest "$base/_crossdock/healthz" -UseBasicParsing | Out-Null; break } catch { Start-Sleep -Milliseconds 100 }
  }

  # 2. The snippet, as the landing page shows it: like a browser, drop
  # HTML comments (the page's http-only markers) before decoding.
  $page = (Invoke-WebRequest "$base/" -UseBasicParsing).Content
  $snippet = [regex]::Matches($page, '(?s)<pre>(.*?)</pre>') |
    ForEach-Object { [Net.WebUtility]::HtmlDecode(($_.Groups[1].Value -replace '(?s)<!--.*?-->', '')) } |
    Where-Object { $_ -match '(?m)^googet addrepo smoke ' } | Select-Object -First 1
  if (-not $snippet) { Fail 'landing page lacks the GooGet snippet' }
  $snippet
  $installBlock = $snippet.Substring(0, $snippet.IndexOf("`n}") + 2)
  if (-not $snippet.Contains("'allowunsafeurl: true'")) { Fail 'the plain-HTTP page lacks the allowunsafeurl lines' }
  if (paste $snippet) { Fail "the landing page's GooGet snippet failed" }
  # This session predates the install: take what it set machine-wide.
  $env:GooGetRoot = [Environment]::GetEnvironmentVariable('GooGetRoot', 'Machine')
  if ($env:GooGetRoot -ne $root) { Fail "GooGet's install set GooGetRoot to '$env:GooGetRoot', not $root" }
  $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not (Get-Command googet -ErrorAction SilentlyContinue)) { Fail "the snippet didn't put googet on the machine's PATH" }
  $installed = googet installed | Out-String
  $installed
  if ($installed -notmatch 'googet\.x86_64') { Fail "googet doesn't list itself as installed" }
  "installed GooGet with the landing page's snippet"
  if (-not ((Test-Path "$root\googet.conf") -and (Select-String -Quiet '^allowunsafeurl:\s*true\s*$' "$root\googet.conf"))) {
    Fail "the snippet didn't set allowunsafeurl in googet.conf"
  }
  'the snippet set allowunsafeurl: true in googet.conf'

  # 3. A package from the crossdock repo.
  googet -noconfirm install crossdock-smoke
  if ($LASTEXITCODE) { Fail 'googet install crossdock-smoke failed' }
  $hello = Join-Path $env:ProgramData 'crossdock-smoke\hello.txt'
  if (-not (Test-Path $hello) -or (Get-Content $hello) -ne 'crossdock smoke') { Fail "crossdock-smoke didn't install $hello" }
  "installed crossdock-smoke from the crossdock repo"

  # 4. With GooGet there, the if block does nothing.
  $exe = Get-Item "$root\googet.exe"
  $before = $exe.LastWriteTimeUtc
  if (paste $installBlock) { Fail 'the if block failed with GooGet installed' }
  if ((Get-Item "$root\googet.exe").LastWriteTimeUtc -ne $before -or (Test-Path (Join-Path $env:TEMP googet.goo))) {
    Fail 'the if block reinstalled GooGet'
  }
  'the if block left the installed GooGet alone'
} finally {
  Stop-Process -Id $serve.Id -ErrorAction SilentlyContinue
  if (Test-Path "$work\serve.log") { Get-Content "$work\serve.log" | Select-Object -Last 20 }
}
