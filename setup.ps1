$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
Push-Location $root
try {
    $binary = Join-Path $root 'tdm-light.exe'
    if (-not (Test-Path -LiteralPath $binary)) {
        & go build -o $binary .
        if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
    }
    & $binary --setup
    if ($LASTEXITCODE -ne 0) { throw 'Setup was not completed.' }
} finally {
    Pop-Location
}
