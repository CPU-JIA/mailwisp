[CmdletBinding()]
param(
    [string]$Version,
    [string]$SyftCommand = 'syft'
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-Native {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][scriptblock]$Command)
    $previous = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        & $Command
        $exitCode = $LASTEXITCODE
    } finally { $PSNativeCommandUseErrorActionPreference = $previous }
    if ($exitCode -ne 0) { throw "$Name failed with exit code $exitCode." }
}

function Invoke-NativeText {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][scriptblock]$Command)
    $previous = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        $output = & $Command | Out-String
        $exitCode = $LASTEXITCODE
    } finally { $PSNativeCommandUseErrorActionPreference = $previous }
    if ($exitCode -ne 0) { throw "$Name failed with exit code $exitCode." }
    return $output.Trim()
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-artifacts.ps1')
$artifactRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release')
$buildOutputPath = Join-Path $artifactRoot 'build-output.json'
if (-not (Test-Path -LiteralPath $buildOutputPath -PathType Leaf)) { throw 'Run scripts/build-release.ps1 before generating SBOMs.' }
$build = Get-Content -Raw -LiteralPath $buildOutputPath | ConvertFrom-Json -DateKind String
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = $build.version }
if ($Version -ne $build.version) { throw "SBOM version $Version does not match build version $($build.version)." }
if ($build.git_dirty -or $build.git_commit -notmatch '^[a-f0-9]{40}$') {
    throw 'Source SBOM generation requires a clean build with a complete Git commit identity.'
}
$headCommit = Invoke-NativeText -Name 'Git HEAD identity' -Command { git -C $repositoryRoot rev-parse HEAD }
$worktreeStatus = Invoke-NativeText -Name 'Git worktree status' -Command { git -C $repositoryRoot status --porcelain=v1 --untracked-files=all }
if ($headCommit -ne $build.git_commit -or -not [string]::IsNullOrWhiteSpace($worktreeStatus)) {
    throw 'Source SBOM generation requires HEAD and the clean worktree to match the release build commit.'
}

$syft = Get-Command $SyftCommand -ErrorAction SilentlyContinue
$versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
$expectedSyftVersion = $versionLock.MAILWISP_SYFT
if ($expectedSyftVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'MAILWISP_SYFT is missing or invalid in versions.lock.' }
if (-not $syft) { throw "Syft $expectedSyftVersion is required to generate release SBOMs." }
$syftVersionRaw = & $syft.Source version -o json | Out-String
if ($LASTEXITCODE -ne 0) { throw 'Unable to read Syft version.' }
$syftVersion = $syftVersionRaw | ConvertFrom-Json -DateKind String
if ($syftVersion.version -ne $expectedSyftVersion) { throw "Syft $expectedSyftVersion is required. Actual: $($syftVersion.version)" }

$sbomRoot = Join-Path $artifactRoot 'sbom'
Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $sbomRoot
[System.IO.Directory]::CreateDirectory($sbomRoot) | Out-Null

$documents = [System.Collections.Generic.List[object]]::new()
$bundleRoot = Join-Path $artifactRoot $build.bundle_directory
$binaryPath = Join-Path $bundleRoot 'mailwisp'
if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) { throw "Release binary is missing: $binaryPath" }
$binarySBOMPath = Join-Path $sbomRoot "mailwisp-$Version-binary.spdx.json"
Invoke-Native -Name 'binary SPDX SBOM generation' -Command {
    & $syft.Source scan "file:$binaryPath" `
        --source-name mailwisp `
        --source-version $Version `
        -o "spdx-json=$binarySBOMPath"
}
$documents.Add([ordered]@{ kind = 'binary'; subject = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash.ToLowerInvariant(); path = $binarySBOMPath })

$sourcePath = Join-Path $sbomRoot "mailwisp-$Version-source.spdx.json"
$sourceWorkspace = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $artifactRoot 'source-snapshot')
Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $sourceWorkspace
$sourceTree = Join-Path $sourceWorkspace 'tree'
$sourceArchive = Join-Path $sourceWorkspace 'source.tar'
[System.IO.Directory]::CreateDirectory($sourceTree) | Out-Null
try {
    Invoke-Native -Name 'committed source snapshot creation' -Command {
        git -C $repositoryRoot archive --format=tar "--output=$sourceArchive" $build.git_commit
    }
    Invoke-Native -Name 'committed source snapshot extraction' -Command { tar -xf $sourceArchive -C $sourceTree }
    Invoke-Native -Name 'source SPDX SBOM generation' -Command {
        & $syft.Source scan "dir:$sourceTree" `
            --source-name MailWisp `
            --source-version $Version `
            -o "spdx-json=$sourcePath"
    }
} finally {
    Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $sourceWorkspace
}
$documents.Add([ordered]@{ kind = 'source'; subject = $build.git_commit; path = $sourcePath })

foreach ($property in $build.images.PSObject.Properties) {
    $name = $property.Name
    $image = $property.Value
    $output = Join-Path $sbomRoot "mailwisp-$Version-$name-image.spdx.json"
    Invoke-Native -Name "$name image SPDX SBOM generation" -Command {
        & $syft.Source scan "docker:$($image.id)" -o "spdx-json=$output"
    }
    $documents.Add([ordered]@{ kind = 'image'; name = $name; subject = $image.id; reference = $image.reference; path = $output })
}

$indexDocuments = foreach ($document in $documents) {
    $json = Get-Content -Raw -LiteralPath $document.path | ConvertFrom-Json -DateKind String
    if ($json.spdxVersion -ne 'SPDX-2.3') { throw "$($document.path) is not SPDX 2.3 JSON." }
    $packageCount = @($json.packages).Count
    if ($packageCount -eq 0) { throw "$($document.path) does not describe any packages." }
    [ordered]@{
        kind = $document.kind
        name = $document.name
        subject = $document.subject
        reference = $document.reference
        file = [System.IO.Path]::GetFileName($document.path)
        sha256 = (Get-FileHash -LiteralPath $document.path -Algorithm SHA256).Hash.ToLowerInvariant()
        packages = $packageCount
    }
}

[ordered]@{
    schema_version = 1
    product = 'MailWisp'
    version = $Version
    git_commit = $build.git_commit
    syft_version = $syftVersion.version
    format = 'SPDX-2.3 JSON'
    documents = @($indexDocuments)
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $sbomRoot 'sbom-index.json') -Encoding utf8NoBOM

Write-Output $sbomRoot
