# ncli installer for Windows.
#
#   irm https://ohstr.github.io/ncli/install.ps1 | iex
#
# Installs to a per-user directory (no Administrator prompt) and updates
# the user PATH via the registry, so a new terminal picks it up immediately.
#
# Env overrides: $env:NCLI_VERSION (default: latest), $env:NCLI_INSTALL_DIR.
#requires -version 5

$ErrorActionPreference = 'Stop'

$Repo = 'ohstr/ncli'
$BinName = 'ncli'

function Write-Info($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Warn($msg) { Write-Host "warning: $msg" -ForegroundColor Yellow }
function Write-Err($msg) { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Write-Err "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
}

Write-Info "Detected platform: windows/$arch"

$version = if ($env:NCLI_VERSION) { $env:NCLI_VERSION } else { 'latest' }
$archiveName = "${BinName}_windows_${arch}.zip"

if ($version -eq 'latest') {
    $assetUrl = "https://github.com/$Repo/releases/latest/download/$archiveName"
    $checksumsUrl = "https://github.com/$Repo/releases/latest/download/checksums.txt"
} else {
    $assetUrl = "https://github.com/$Repo/releases/download/v$version/$archiveName"
    $checksumsUrl = "https://github.com/$Repo/releases/download/v$version/checksums.txt"
}

$installDir = if ($env:NCLI_INSTALL_DIR) { $env:NCLI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "$BinName\bin" }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    $zipPath = Join-Path $tmp $archiveName

    Write-Info "Downloading $BinName $version..."
    Invoke-WebRequest -Uri $assetUrl -OutFile $zipPath -UseBasicParsing

    try {
        $checksumsPath = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing
        $line = Select-String -Path $checksumsPath -Pattern ([regex]::Escape($archiveName)) | Select-Object -First 1
        if ($line) {
            $expected = ($line.Line -split '\s+')[0]
            $actual = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
            if ($expected -ne $actual) {
                Write-Err "checksum mismatch for ${archiveName}: expected $expected, got $actual"
            }
            Write-Info "Checksum verified."
        } else {
            Write-Warn "no checksum entry found for $archiveName, skipping verification"
        }
    } catch {
        Write-Warn "could not verify checksum ($($_.Exception.Message))"
    }

    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
    Copy-Item -Path (Join-Path $tmp "$BinName.exe") -Destination (Join-Path $installDir "$BinName.exe") -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Info "Installed $BinName to $installDir\$BinName.exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$installDir;$userPath", 'User')
    $env:Path = "$installDir;$env:Path"
    Write-Info "Added $installDir to your user PATH. Open a new terminal, then run '$BinName version' to verify."
} else {
    Write-Info "Run '$BinName version' to verify the install."
}
