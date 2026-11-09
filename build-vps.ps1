$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$archive = Join-Path $root 'tdm-light-vps-ubuntu.tar.gz'
$staging = Join-Path ([IO.Path]::GetTempPath()) ('tdm-vps-' + [guid]::NewGuid())
$bundle = Join-Path $staging 'vps-linux'

$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED

Push-Location -LiteralPath $root
try {
    New-Item -ItemType Directory -Path $bundle | Out-Null
    foreach ($file in @('install.sh', 'tdm-light.service', 'README.md', 'Dockerfile', '.dockerignore')) {
        Copy-Item -LiteralPath (Join-Path $root "vps-linux/$file") -Destination (Join-Path $bundle $file)
    }
    foreach ($file in @('config.env.example', 'config.json.example', '.env.example', 'docker-compose.yml', 'docker-compose.orchestrator.yml', 'ORCHESTRATOR.md')) {
        Copy-Item -LiteralPath (Join-Path $root $file) -Destination (Join-Path $bundle $file)
    }

    $env:GOOS = 'linux'
    $env:CGO_ENABLED = '0'
    foreach ($arch in @('amd64', 'arm64')) {
        $env:GOARCH = $arch
        Write-Host "Building Linux/$arch..."
        & go build -trimpath -ldflags '-s -w' -o (Join-Path $bundle "tdm-light-linux-$arch") .
        if ($LASTEXITCODE -ne 0) { throw "Build failed for Linux/$arch." }
    }

    $stagedArchive = Join-Path $staging 'tdm-light-vps-ubuntu.tar.gz'
    & tar -czf $stagedArchive -C $staging vps-linux
    if ($LASTEXITCODE -ne 0) { throw 'Could not create VPS archive.' }
    Copy-Item -LiteralPath $stagedArchive -Destination $archive -Force
} finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
    Pop-Location
    if (Test-Path -LiteralPath $staging) {
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
        $resolved = (Resolve-Path -LiteralPath $staging).Path
        $item = Get-Item -LiteralPath $resolved -Force
        if ($resolved -ne [IO.Path]::GetFullPath($staging) -or
            -not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
            ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Unexpected staging path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

Write-Host "Archive ready: $archive"
