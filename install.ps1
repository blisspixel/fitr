# fitr installer.
#
#   irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex
#
# Downloads one static .exe. No interpreter, no package manager, no venv.
# Set FITR_VERSION to pin, FITR_BIN to relocate. FITR_NO_VERIFY=1 skips the
# checksum check explicitly. FITR_NO_PATH=1 leaves the persistent user PATH
# unchanged while still making fitr available in the current PowerShell host.
& {
$ErrorActionPreference = "Stop"

$Repo = "blisspixel/fitr"
$Version = $env:FITR_VERSION
if (-not $Version) { $Version = "latest" }
$ReleaseBaseUrl = $env:FITR_RELEASE_BASE_URL

function Stop-Fitr {
    param([string]$Message, [string]$Note = "", [string]$Hint = "")
    [Console]::Error.WriteLine("error: $Message")
    if ($Note) { [Console]::Error.WriteLine(" note: $Note") }
    if ($Hint) { [Console]::Error.WriteLine(" hint: $Hint") }
    throw $Message
}

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

$archName = "amd64"
$proc = $env:PROCESSOR_ARCHITECTURE
if ($proc -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
    $archName = "arm64"
} elseif ($proc -eq "x86" -and -not [Environment]::Is64BitOperatingSystem) {
    Stop-Fitr "unsupported architecture x86" "fitr ships amd64 and arm64 builds" ""
}

$BinDir = $env:FITR_BIN
if (-not $BinDir) {
    $BinDir = Join-Path $env:LOCALAPPDATA "fitr"
}

$asset = "fitr-windows-$archName.exe"
if ($ReleaseBaseUrl) {
    $ReleaseBaseUrl = $ReleaseBaseUrl.TrimEnd("/")
    $url = "$ReleaseBaseUrl/$asset"
    $sumUrl = "$ReleaseBaseUrl/SHA256SUMS"
} elseif ($Version -eq "latest") {
    $url = "https://github.com/$Repo/releases/latest/download/$asset"
    $sumUrl = "https://github.com/$Repo/releases/latest/download/SHA256SUMS"
} else {
    if ($Version -notlike "v*") { $Version = "v$Version" }
    $url = "https://github.com/$Repo/releases/download/$Version/$asset"
    $sumUrl = "https://github.com/$Repo/releases/download/$Version/SHA256SUMS"
}

Write-Host "  installing fitr (windows/$archName) -> $BinDir"

New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$dest = Join-Path $BinDir "fitr.exe"
$tmp = Join-Path $BinDir (".fitr-install-" + [Guid]::NewGuid().ToString("N") + ".exe")
$gotBinary = $false

try {
    Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
    $item = Get-Item $tmp
    if ($item.Length -ge 1000000) { $gotBinary = $true }
} catch {}

if ($gotBinary) {
    if ($env:FITR_NO_VERIFY -ne "1") {
        $got = (Get-FileHash $tmp -Algorithm SHA256).Hash.ToLower()
        try {
            $sumContent = (Invoke-WebRequest -Uri $sumUrl -UseBasicParsing).Content
        } catch {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Stop-Fitr "cannot verify the download" "could not fetch $sumUrl" "check the release, or set FITR_NO_VERIFY=1 to accept the risk explicitly"
        }
        try {
            if ($sumContent -is [byte[]]) {
                $utf8 = [Text.UTF8Encoding]::new($false, $true)
                $sums = $utf8.GetString($sumContent)
            } else {
                $sums = [string]$sumContent
            }
        } catch {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Stop-Fitr "invalid checksum manifest" "SHA256SUMS is not valid UTF-8 text" "verify the release assets and re-run the installer"
        }
        $entries = @([regex]::Matches(
            [string]$sums,
            '(?im)^(?<digest>[0-9a-f]{64})\s+[*]?(?<file>[^\r\n]+?)\s*$'
        ) | Where-Object { $_.Groups['file'].Value.Trim() -eq $asset })
        if ($entries.Count -ne 1) {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Stop-Fitr "invalid checksum manifest" "need exactly one entry for $asset" "verify the release assets and re-run the installer"
        }
        $expected = $entries[0].Groups['digest'].Value.ToLower()
        if ($got -cne $expected) {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Stop-Fitr "checksum mismatch for $asset" "expected $expected; got $got" "the download may be corrupt; re-run the installer"
        }
        Write-Host "  verified: sha256 $got"
    } else {
        Write-Host " note: checksum verification disabled by FITR_NO_VERIFY=1"
    }
    Move-Item -Force $tmp $dest
} else {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    if ($env:FITR_REQUIRE_BINARY -eq "1") {
        Stop-Fitr "could not fetch the required release binary" $url "verify the candidate assets and checksum manifest"
    }
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        Stop-Fitr "download failed" $url "no GitHub release yet and no Go on PATH; install Go 1.25+ or clone and 'go install ./cmd/fitr'"
    }
    Write-Host "  no release binary; building with Go -> $BinDir"
    $prevGobin = $env:GOBIN
    $env:GOBIN = $BinDir
    try {
        & go install "github.com/$Repo/cmd/fitr@$Version"
        if ($LASTEXITCODE -ne 0) { throw "go install exited $LASTEXITCODE" }
    } catch {
        $env:GOBIN = $prevGobin
        Stop-Fitr "go install failed" $_.Exception.Message "needs Go 1.25+; or git clone https://github.com/$Repo and 'go install ./cmd/fitr'"
    }
    $env:GOBIN = $prevGobin
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
$onPath = $false
foreach ($part in $userPath.Split(";")) {
    if ($part -and (([string]$part).TrimEnd("\") -eq $BinDir.TrimEnd("\"))) {
        $onPath = $true
        break
    }
}
if (-not $onPath -and $env:FITR_NO_PATH -ne "1") {
    if ($userPath) {
        [Environment]::SetEnvironmentVariable("Path", "$BinDir;$userPath", "User")
    } else {
        [Environment]::SetEnvironmentVariable("Path", $BinDir, "User")
    }
    Write-Host "  added $BinDir to your user PATH (new terminals will see it)"
} elseif (-not $onPath) {
    Write-Host "  left user PATH unchanged (FITR_NO_PATH=1)"
}
$env:Path = "$BinDir;$env:Path"

Write-Host "  installed: $dest"
Write-Host ""
Write-Host "  next:  fitr                        # hardware and reachable runtime"
Write-Host "         fitr advise <model>         # does this quant fit"
Write-Host "         fitr run <model>            # measure it on this machine"
}
