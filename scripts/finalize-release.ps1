[CmdletBinding()]
param(
    [string]$Version,
    [switch]$AllowDirty
)

$ErrorActionPreference = 'Stop'

$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-artifacts.ps1')
$artifactRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release')
$publishRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $artifactRoot 'publish')
$buildOutputPath = Join-Path $artifactRoot 'build-output.json'
if (-not (Test-Path -LiteralPath $buildOutputPath -PathType Leaf)) { throw 'Release build output is missing.' }
$build = Get-Content -Raw -LiteralPath $buildOutputPath | ConvertFrom-Json -DateKind String
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = $build.version }
if ($Version -ne $build.version) { throw "Finalize version $Version does not match build version $($build.version)." }
if ($build.git_dirty -and -not $AllowDirty) { throw 'Final release evidence cannot be produced from a dirty Git worktree.' }
if ($build.image_timestamp_rewrite -ne $true) { throw 'Release images were not exported with deterministic timestamp rewriting.' }

$archive = Join-Path $artifactRoot "mailwisp-$Version-linux-amd64.tar.gz"
$sbomIndexPath = Join-Path $artifactRoot 'sbom/sbom-index.json'
$securityIndexPath = Join-Path $artifactRoot 'security/security-index.json'
foreach ($required in @($archive, $sbomIndexPath, $securityIndexPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Release evidence input is missing: $required" }
}
$sbom = Get-Content -Raw -LiteralPath $sbomIndexPath | ConvertFrom-Json -DateKind String
$security = Get-Content -Raw -LiteralPath $securityIndexPath | ConvertFrom-Json -DateKind String
if ($sbom.version -ne $Version -or $sbom.git_commit -ne $build.git_commit -or $security.version -ne $Version -or $security.git_commit -ne $build.git_commit) {
    throw 'Release build, SBOM and security evidence identities do not match.'
}
if ($security.blocking_findings -ne 0) { throw 'Release security evidence contains blocking findings.' }
if (@($sbom.documents).Count -ne 6 -or @($security.reports).Count -ne 5) {
    throw 'Release SBOM or security evidence does not cover the complete subject set.'
}
if ($security.vulnerability_database.schema_version -lt 1 -or [string]::IsNullOrWhiteSpace($security.vulnerability_database.updated_at)) {
    throw 'Release security evidence does not identify the Trivy vulnerability database.'
}

$verificationEvidence = [ordered]@{ release_bundle = 'pending'; production_e2e = 'pending' }
$evidenceDirectory = Join-Path $artifactRoot 'evidence'
$releaseVerificationSource = Join-Path $repositoryRoot 'artifacts/release-verification/result.json'
$productionE2ESource = Join-Path $repositoryRoot 'artifacts/release-e2e/result.json'
if ((Test-Path -LiteralPath $releaseVerificationSource -PathType Leaf) -or (Test-Path -LiteralPath $productionE2ESource -PathType Leaf)) {
    if (-not (Test-Path -LiteralPath $releaseVerificationSource -PathType Leaf) -or -not (Test-Path -LiteralPath $productionE2ESource -PathType Leaf)) {
        throw 'Release verification evidence is incomplete.'
    }
    $releaseVerification = Get-Content -Raw -LiteralPath $releaseVerificationSource | ConvertFrom-Json -DateKind String
    $productionE2E = Get-Content -Raw -LiteralPath $productionE2ESource | ConvertFrom-Json -DateKind String
    if ($releaseVerification.status -ne 'passed' -or $releaseVerification.version -ne $Version -or $releaseVerification.git_commit -ne $build.git_commit -or
        $releaseVerification.checks.source_builds_remaining -ne 0 -or $releaseVerification.checks.production_e2e -ne $true -or
        $productionE2E.status -ne 'passed') {
        throw 'Release verification evidence did not prove the expected prebuilt Production E2E contract.'
    }
    Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $evidenceDirectory
    [System.IO.Directory]::CreateDirectory($evidenceDirectory) | Out-Null
    Copy-Item -LiteralPath $releaseVerificationSource -Destination (Join-Path $evidenceDirectory 'release-verification.json')
    Copy-Item -LiteralPath $productionE2ESource -Destination (Join-Path $evidenceDirectory 'production-e2e.json')
    $verificationEvidence.release_bundle = 'passed'
    $verificationEvidence.production_e2e = 'passed'
}

$archiveHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
$evidencePath = Join-Path $artifactRoot 'release-evidence.json'
[ordered]@{
    schema_version = 1
    product = 'MailWisp'
    version = $Version
    git_commit = $build.git_commit
    git_dirty = [bool]$build.git_dirty
    build_date = $build.build_date
    platform = 'linux/amd64'
    toolchain = [ordered]@{
        docker_engine_version = $build.docker_engine_version
        docker_compose_version = $build.docker_compose_version
        docker_buildx_version = $build.docker_buildx_version
        docker_buildkit_version = $build.docker_buildkit_version
        docker_buildkit_image = $build.docker_buildkit_image
        build_cache = $build.build_cache
        image_timestamp_rewrite = $build.image_timestamp_rewrite
    }
    archive = [ordered]@{ file = [System.IO.Path]::GetFileName($archive); sha256 = $archiveHash; size_bytes = (Get-Item -LiteralPath $archive).Length }
    sbom = [ordered]@{ format = $sbom.format; syft_version = $sbom.syft_version; documents = @($sbom.documents).Count }
    security = [ordered]@{
        trivy_version = $security.trivy_version
        blocking_findings = $security.blocking_findings
        ignore_unfixed = $security.policy.ignore_unfixed
        accepted_risks = @($security.accepted_risks).Count
        vulnerability_database = $security.vulnerability_database
        check_bundle = $security.check_bundle
    }
    verification = $verificationEvidence
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $evidencePath -Encoding utf8NoBOM

$assets = @($archive, $evidencePath) +
    @(Get-ChildItem -LiteralPath (Join-Path $artifactRoot 'sbom') -File | Select-Object -ExpandProperty FullName) +
    @(Get-ChildItem -LiteralPath (Join-Path $artifactRoot 'security') -File | Select-Object -ExpandProperty FullName) +
    @(if (Test-Path -LiteralPath $evidenceDirectory) { Get-ChildItem -LiteralPath $evidenceDirectory -File | Select-Object -ExpandProperty FullName })

Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $publishRoot
[System.IO.Directory]::CreateDirectory($publishRoot) | Out-Null
$assetNames = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
foreach ($asset in $assets) {
    $name = [System.IO.Path]::GetFileName($asset)
    if ([string]::IsNullOrWhiteSpace($name) -or $name -eq 'SHA256SUMS' -or -not $assetNames.Add($name)) {
        throw "Release publish asset name is empty, reserved or duplicated: $name"
    }
    Copy-Item -LiteralPath $asset -Destination (Join-Path $publishRoot $name)
}
$publishedAssets = @(Get-ChildItem -LiteralPath $publishRoot -File)
if ($publishedAssets.Count -ne $assets.Count) { throw 'Release publish directory does not contain the complete flattened asset set.' }
$checksumLines = $publishedAssets | Sort-Object Name | ForEach-Object {
    $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $($_.Name)"
}
$checksumPath = Join-Path $publishRoot 'SHA256SUMS'
[System.IO.File]::WriteAllLines($checksumPath, $checksumLines, [System.Text.UTF8Encoding]::new($false))

Write-Output $checksumPath
