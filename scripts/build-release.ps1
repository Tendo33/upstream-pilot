[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$outputDir = Join-Path $root "dist"
$output = Join-Path $outputDir "s2am-go-linux-amd64"
$checksum = "$output.sha256"
$module = "github.com/langrenjh-alt/S2AM-GO"

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go 1.24 or newer is required"
}
if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    throw "Node.js and npm are required to build the embedded web UI"
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)] [string] $Command,
        [Parameter(ValueFromRemainingArguments = $true)] [string[]] $Arguments
    )
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command exited with code $LASTEXITCODE"
    }
}

$version = $env:VERSION
if ([string]::IsNullOrWhiteSpace($version)) {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $version = (& git -C $root describe --tags --always --dirty 2>$null)
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($version)) { $version = "dev" }
    } else {
        $version = "dev"
    }
}
$commit = $env:COMMIT
if ([string]::IsNullOrWhiteSpace($commit)) {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $commit = (& git -C $root rev-parse --short=12 HEAD 2>$null)
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commit)) { $commit = "unknown" }
    } else {
        $commit = "unknown"
    }
}
if ([string]::IsNullOrWhiteSpace($env:SOURCE_DATE_EPOCH)) {
    $buildTime = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
} else {
    $buildTime = [DateTimeOffset]::FromUnixTimeSeconds([long]$env:SOURCE_DATE_EPOCH).UtcDateTime.ToString("yyyy-MM-ddTHH:mm:ssZ")
}

Write-Host "==> Installing locked frontend dependencies"
Invoke-Checked -Command "npm" -Arguments @("--prefix", (Join-Path $root "web"), "ci")
Write-Host "==> Building embedded frontend"
Invoke-Checked -Command "npm" -Arguments @("--prefix", (Join-Path $root "web"), "run", "build")
Write-Host "==> Running Go tests"
Push-Location $root
try {
    Invoke-Checked -Command "go" -Arguments @("test", "./...")
} finally {
    Pop-Location
}

New-Item -ItemType Directory -Force $outputDir | Out-Null
Remove-Item -Force -ErrorAction SilentlyContinue $output, $checksum
$ldflags = "-s -w -buildid= -X $module/internal/version.Version=$version -X $module/internal/version.Commit=$commit -X $module/internal/version.BuildTime=$buildTime"
$savedTarget = @{
    CGO_ENABLED = $env:CGO_ENABLED
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
}

Write-Host "==> Building s2am-go-linux-amd64 ($version, $commit)"
try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    Push-Location $root
    try {
        Invoke-Checked -Command "go" -Arguments @("build", "-trimpath", "-ldflags", $ldflags, "-o", $output, "./cmd/s2am-go")
    } finally {
        Pop-Location
    }
} finally {
    foreach ($name in $savedTarget.Keys) {
        $value = $savedTarget[$name]
        if ($null -eq $value) {
            Remove-Item "Env:$name" -ErrorAction SilentlyContinue
        } else {
            Set-Item "Env:$name" $value
        }
    }
}

$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $output).Hash.ToLowerInvariant()
"$hash  s2am-go-linux-amd64`n" | Set-Content -NoNewline -Encoding ascii -LiteralPath $checksum
Write-Host "==> Release ready"
Write-Host $output
Write-Host $checksum
