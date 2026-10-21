<#
Installs or updates creatio-mcp-go on Windows and registers it in Claude Code and Codex.

  irm https://raw.githubusercontent.com/Alexandr-Kravchuk/creatio-mcp-go/main/scripts/install.ps1 | iex
  & ([scriptblock]::Create((irm https://raw.githubusercontent.com/Alexandr-Kravchuk/creatio-mcp-go/main/scripts/install.ps1))) -Version v0.2.0

Parameters (each also has an environment variable, for the `irm | iex` form):
  -Version <tag>   release to install (CREATIO_MCP_GO_VERSION); default: the newest release,
                   pre-releases included
  -Dir <path>      install directory (CREATIO_MCP_GO_INSTALL_DIR);
                   default: %LOCALAPPDATA%\creatio-mcp-go\bin
  -Name <name>     MCP server name in claude/codex (CREATIO_MCP_GO_NAME); default: creatio-go
  -NoRegister      do not touch the claude/codex configuration (CREATIO_MCP_GO_NO_REGISTER=1)

Re-running updates the binary. A running creatio-mcp-go.exe cannot be overwritten, but it can be
renamed: the old file is moved aside to creatio-mcp-go.exe.old-<time> and the new one takes its
name, so a running server keeps working and the next launch takes the new build. Leftover .old files
are deleted on the next run once nothing uses them. Nothing is replaced unless the archive matches
the release's SHA256SUMS. The directory is added to the user PATH when it is missing
(CREATIO_MCP_GO_NO_PATH_UPDATE=1 leaves the user PATH alone).

CREATIO_MCP_GO_RELEASES_API and CREATIO_MCP_GO_DOWNLOAD_BASE point at another release source
(used by scripts/test_install_ps1.py).
#>
param(
    [string]$Version = $env:CREATIO_MCP_GO_VERSION,
    [string]$Dir = $env:CREATIO_MCP_GO_INSTALL_DIR,
    [string]$Name = $env:CREATIO_MCP_GO_NAME,
    [switch]$NoRegister
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Fail([string]$Message) {
    # `exit` from `irm | iex` would close the user's window, so the script throws instead.
    throw "creatio-mcp-go install: $Message"
}

$repo = 'Alexandr-Kravchuk/creatio-mcp-go'
$releasesApi = if ($env:CREATIO_MCP_GO_RELEASES_API) { $env:CREATIO_MCP_GO_RELEASES_API } else { "https://api.github.com/repos/$repo/releases?per_page=10" }
$downloadBase = if ($env:CREATIO_MCP_GO_DOWNLOAD_BASE) { $env:CREATIO_MCP_GO_DOWNLOAD_BASE } else { "https://github.com/$repo/releases/download" }
if (-not $Name) { $Name = 'creatio-go' }
if ($env:CREATIO_MCP_GO_NO_REGISTER -in @('1', 'true', 'yes')) { $NoRegister = $true }

$onWindows = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
# The test on macOS/Linux sets this to exercise the download, check and swap logic.
$testMode = $env:CREATIO_MCP_GO_INSTALL_TEST -eq '1'
if (-not $onWindows -and -not $testMode) {
    Fail 'this script is for Windows; on macOS and Linux use scripts/install.sh'
}
if ($onWindows) {
    $cpu = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $cpu = $env:PROCESSOR_ARCHITEW6432 }
    if ($cpu -eq 'ARM64') {
        Write-Host 'Note: there is no windows_arm64 build; installing windows_amd64, which Windows 11 runs under emulation.'
    } elseif ($cpu -ne 'AMD64') {
        Fail "unsupported CPU architecture: $cpu. The Windows release exists for amd64 only."
    }
}

if (-not $Dir) {
    if (-not $env:LOCALAPPDATA) { Fail 'LOCALAPPDATA is not set; pass -Dir' }
    $Dir = Join-Path $env:LOCALAPPDATA 'creatio-mcp-go\bin'
}

if ($PSVersionTable.PSVersion.Major -lt 6) {
    # Windows PowerShell 5.1 may default to TLS 1.0, which GitHub refuses.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

if (-not $Version) {
    # The releases list is newest first and includes pre-releases; /releases/latest skips them.
    try {
        $response = Invoke-RestMethod -UseBasicParsing -Uri $releasesApi
    } catch {
        Fail "could not read the release list from $releasesApi (network, or GitHub API rate limit?): $($_.Exception.Message)"
    }
    # A response without a JSON content type comes back as text; PowerShell 5.1 returns a JSON array
    # as one object, so the pipe below unrolls it into releases.
    if ($response -is [string]) { $response = $response | ConvertFrom-Json }
    $newest = $response | ForEach-Object { $_ } | Where-Object { $_.tag_name -and -not $_.draft } | Select-Object -First 1
    if (-not $newest) { Fail "no release found at $releasesApi" }
    $Version = [string]$newest.tag_name
}
if (-not $Version.StartsWith('v')) { $Version = "v$Version" }

$asset = "creatio-mcp-go_$($Version.Substring(1))_windows_amd64"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("creatio-mcp-go-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
$staged = $null
try {
    Write-Host "Installing creatio-mcp-go $Version (windows/amd64)"
    $zip = Join-Path $tmp "$asset.zip"
    $sums = Join-Path $tmp 'SHA256SUMS'
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$Version/$asset.zip" -OutFile $zip
    } catch {
        Fail "download failed: $downloadBase/$Version/$asset.zip (wrong version, or network?): $($_.Exception.Message)"
    }
    try {
        Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$Version/SHA256SUMS" -OutFile $sums
    } catch {
        Fail "download failed: $downloadBase/$Version/SHA256SUMS: $($_.Exception.Message)"
    }

    $expected = $null
    foreach ($line in Get-Content -LiteralPath $sums) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq "$asset.zip") { $expected = $parts[0]; break }
    }
    if (-not $expected) { Fail "SHA256SUMS of $Version has no line for $asset.zip" }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash
    if ($actual -ne $expected) {
        # -ne on strings ignores case, so the upper-case Get-FileHash matches lower-case SHA256SUMS.
        Fail "checksum mismatch for $asset.zip`: expected $expected, got $($actual.ToLowerInvariant()). Nothing was installed."
    }

    $unpacked = Join-Path $tmp 'unpacked'
    Expand-Archive -LiteralPath $zip -DestinationPath $unpacked
    $source = Join-Path $unpacked "$asset\creatio-mcp-go.exe"
    if (-not (Test-Path -LiteralPath $source)) { Fail "the archive does not contain $asset\creatio-mcp-go.exe" }

    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    $Dir = (Resolve-Path -LiteralPath $Dir).ProviderPath
    $target = Join-Path $Dir 'creatio-mcp-go.exe'

    # Old copies moved aside by earlier runs; one that a running server still uses stays until next time.
    Get-ChildItem -LiteralPath $Dir -Filter 'creatio-mcp-go.exe.old-*' -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }

    $staged = Join-Path $Dir ("creatio-mcp-go.exe.new-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    Copy-Item -LiteralPath $source -Destination $staged
    if ($onWindows) { Unblock-File -LiteralPath $staged -ErrorAction SilentlyContinue }

    $aside = $null
    if (Test-Path -LiteralPath $target) {
        $aside = "$target.old-" + (Get-Date -Format 'yyyyMMddHHmmss')
        Move-Item -LiteralPath $target -Destination $aside
    }
    try {
        Move-Item -LiteralPath $staged -Destination $target
        $staged = $null
    } catch {
        if ($aside) { Move-Item -LiteralPath $aside -Destination $target -ErrorAction SilentlyContinue }
        Fail "cannot replace $target`: $($_.Exception.Message)"
    }
    if ($aside) { Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue }
} finally {
    if ($staged) { Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$installedVersion = $null
try {
    $installedVersion = (& $target --version 2>$null | Select-Object -First 1)
} catch {
    $installedVersion = $null
}
if (-not $installedVersion) { $installedVersion = $Version }
Write-Host "Installed $target ($installedVersion)"

if ($onWindows -and $env:CREATIO_MCP_GO_NO_PATH_UPDATE -notin @('1', 'true', 'yes')) {
    # Read the raw value so %VARIABLE% entries stay unexpanded when it is written back.
    $key = Get-Item -LiteralPath 'HKCU:\Environment'
    $userPath = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    $entries = @($userPath -split ';' | Where-Object { $_ })
    if (-not ($entries | Where-Object { $_.TrimEnd('\') -ieq $Dir.TrimEnd('\') })) {
        $newPath = (@($Dir) + $entries) -join ';'
        Set-ItemProperty -LiteralPath 'HKCU:\Environment' -Name Path -Value $newPath -Type ExpandString
        # Tell running programs (Explorer, new terminals) that the environment changed.
        [Environment]::SetEnvironmentVariable('CREATIO_MCP_GO_PATH_REFRESH', '1', 'User')
        [Environment]::SetEnvironmentVariable('CREATIO_MCP_GO_PATH_REFRESH', $null, 'User')
        Write-Host "Added $Dir to the user PATH (new terminals see it)."
    }
}
if (-not (($env:Path -split [IO.Path]::PathSeparator) | Where-Object { $_.TrimEnd('\', '/') -eq $Dir.TrimEnd('\', '/') })) {
    $env:Path = $Dir + [IO.Path]::PathSeparator + $env:Path
}

function Invoke-Client([string]$Client, [string[]]$Arguments) {
    # Native commands write to stderr on "not found"; that must not stop the script.
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $Client @Arguments 2>$null
        return [pscustomobject]@{ Ok = ($LASTEXITCODE -eq 0); Output = (@($output) -join "`n") }
    } catch {
        return [pscustomobject]@{ Ok = $false; Output = '' }
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Get-RegisteredCommand([string]$Output, [string]$Label) {
    foreach ($line in ($Output -split "`n")) {
        if ($line -match "^\s*$Label\s*:\s*(.+?)\s*$") { return $Matches[1] }
    }
    return ''
}

function Register-Claude {
    $current = Invoke-Client 'claude' @('mcp', 'get', $Name)
    $command = if ($current.Ok) { Get-RegisteredCommand $current.Output 'Command' } else { '' }
    if ($current.Ok -and $command -eq $target -and $current.Output -match 'Scope:.*User') {
        Write-Host "Claude Code: $Name is already registered with $target (user scope); left as is"
        return
    }
    if ($current.Ok) {
        Write-Host "Claude Code: $Name points at $(if ($command) { $command } else { 'another command' }); registering it again with $target"
        Write-Host '  (any --env values of the old entry are not carried over; add them again if you used them)'
        Invoke-Client 'claude' @('mcp', 'remove', $Name, '--scope', 'user') | Out-Null
    }
    if ((Invoke-Client 'claude' @('mcp', 'add', '--scope', 'user', $Name, '--', $target)).Ok) {
        Write-Host "Claude Code: registered $Name -> $target (user scope)"
    } else {
        Write-Warning "Claude Code: registration failed; run: claude mcp add --scope user $Name -- `"$target`""
    }
}

function Register-Codex {
    $current = Invoke-Client 'codex' @('mcp', 'get', $Name)
    $command = if ($current.Ok) { Get-RegisteredCommand $current.Output 'command' } else { '' }
    if ($current.Ok -and $command -eq $target) {
        Write-Host "Codex: $Name is already registered with $target; left as is"
        return
    }
    if ($current.Ok) {
        Write-Host "Codex: $Name points at $(if ($command) { $command } else { 'another command' }); registering it again with $target"
        Write-Host '  (any env values of the old entry are not carried over; add them again if you used them)'
        Invoke-Client 'codex' @('mcp', 'remove', $Name) | Out-Null
    }
    if ((Invoke-Client 'codex' @('mcp', 'add', $Name, '--', $target)).Ok) {
        Write-Host "Codex: registered $Name -> $target"
    } else {
        Write-Warning "Codex: registration failed; run: codex mcp add $Name -- `"$target`""
    }
}

if ($NoRegister) {
    Write-Host 'Registration skipped (-NoRegister).'
} else {
    $found = $false
    if (Get-Command claude -ErrorAction SilentlyContinue) { $found = $true; Register-Claude }
    if (Get-Command codex -ErrorAction SilentlyContinue) { $found = $true; Register-Codex }
    if (-not $found) {
        Write-Host 'Neither claude nor codex was found; register the server yourself (docs/install.md, sections 3 and 4).'
    }
}
Write-Host "Done: creatio-mcp-go $installedVersion"
