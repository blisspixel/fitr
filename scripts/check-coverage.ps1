param(
    [ValidateRange(0, 100)]
    [double]$Threshold = 80
)

$ErrorActionPreference = "Stop"
$coverageDir = Join-Path ([IO.Path]::GetTempPath()) ("fitr-coverage-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $coverageDir | Out-Null

try {
    & go test -count=1 -cover -coverpkg=./... ./... -args "-test.gocoverdir=$coverageDir"
    if ($LASTEXITCODE -ne 0) { throw "coverage tests failed" }

    $profile = Join-Path $coverageDir "coverage.out"
    & go tool covdata textfmt "-i=$coverageDir" "-o=$profile"
    if ($LASTEXITCODE -ne 0) { throw "coverage merge failed" }

    [long]$total = 0
    [long]$covered = 0
    Get-Content -LiteralPath $profile | Select-Object -Skip 1 | ForEach-Object {
        $fields = $_ -split '\s+'
        if ($fields.Count -ge 3) {
            $statements = [long]$fields[1]
            $total += $statements
            if ([long]$fields[2] -gt 0) { $covered += $statements }
        }
    }
    if ($total -eq 0) { throw "coverage profile contained no statements" }
    $percentage = 100.0 * $covered / $total
    Write-Host ("total coverage: {0:N2}% ({1}/{2} statements); required: {3:N2}%" -f $percentage, $covered, $total, $Threshold)
    if ($percentage + 0.0000001 -lt $Threshold) {
        throw ("coverage {0:N2}% is below {1:N2}%" -f $percentage, $Threshold)
    }
} finally {
    $resolvedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    $resolvedCoverage = [IO.Path]::GetFullPath($coverageDir)
    if ($resolvedCoverage.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase) -and
        [IO.Path]::GetFileName($resolvedCoverage).StartsWith("fitr-coverage-", [StringComparison]::Ordinal)) {
        Remove-Item -LiteralPath $resolvedCoverage -Recurse -Force -ErrorAction SilentlyContinue
    }
}
