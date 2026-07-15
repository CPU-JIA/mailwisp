$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$artifactRoot = Join-Path $repositoryRoot 'artifacts/release'
$resolvedRepository = [System.IO.Path]::GetFullPath($repositoryRoot)
$resolvedArtifacts = [System.IO.Path]::GetFullPath($artifactRoot)
if (-not $resolvedArtifacts.StartsWith($resolvedRepository + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Release artifact path escaped the repository.'
}

$goVersion = (& go version).Trim()
if ($goVersion -notmatch '^go version go1\.26\.5\s') {
    throw "Go 1.26.5 is required. Actual: $goVersion"
}
$nodeVersion = (& node --version).Trim()
if ($nodeVersion -ne 'v22.20.0') {
    throw "Node.js 22.20.0 is required. Actual: $nodeVersion"
}
$npmVersion = (& npm --version).Trim()
if ($npmVersion -ne '11.15.0') {
    throw "npm 11.15.0 is required. Actual: $npmVersion"
}

Push-Location -LiteralPath (Join-Path $repositoryRoot 'web')
try {
    npm ci
    npm run typecheck
    npm run lint
    npm test
    npm run build
} finally {
    Pop-Location
}

if (Test-Path -LiteralPath $artifactRoot) {
    Remove-Item -LiteralPath $artifactRoot -Recurse -Force
}
$bundleRoot = Join-Path $artifactRoot 'mailwisp-linux-amd64'
$webRoot = Join-Path $bundleRoot 'web'
$deployRoot = Join-Path $bundleRoot 'deploy/reference'
New-Item -ItemType Directory -Force -Path $webRoot | Out-Null
New-Item -ItemType Directory -Force -Path $deployRoot | Out-Null

$previousGoOS = $env:GOOS
$previousGoArch = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
try {
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    go build -trimpath -buildvcs=false -ldflags='-s -w' -o (Join-Path $bundleRoot 'mailwisp') ./cmd/mailwisp
} finally {
    $env:GOOS = $previousGoOS
    $env:GOARCH = $previousGoArch
    $env:CGO_ENABLED = $previousCGO
}

Copy-Item -Path (Join-Path $repositoryRoot 'web/dist/*') -Destination $webRoot -Recurse -Force
foreach ($entry in @('README.md', 'versions.lock', 'certbot', 'nginx', 'postfix', 'systemd')) {
    Copy-Item -LiteralPath (Join-Path $repositoryRoot "deploy/reference/$entry") -Destination $deployRoot -Recurse -Force
}

$checksums = Get-ChildItem -LiteralPath $bundleRoot -File -Recurse | Sort-Object FullName | ForEach-Object {
    $relative = [System.IO.Path]::GetRelativePath($bundleRoot, $_.FullName).Replace('\', '/')
    $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $relative"
}
[System.IO.File]::WriteAllLines((Join-Path $bundleRoot 'SHA256SUMS'), $checksums)

$archive = Join-Path $artifactRoot 'mailwisp-linux-amd64.tar.gz'
tar -czf $archive -C $artifactRoot 'mailwisp-linux-amd64'
Write-Output $archive
