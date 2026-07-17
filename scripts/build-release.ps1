[CmdletBinding()]
param(
    [string]$Version,

    [ValidatePattern('^[a-f0-9]{40}$')]
    [string]$Commit,

    [switch]$AllowDirty
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-Native {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][scriptblock]$Command
    )

    $previous = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        & $Command
        $exitCode = $LASTEXITCODE
    } finally {
        $PSNativeCommandUseErrorActionPreference = $previous
    }
    if ($exitCode -ne 0) { throw "$Name failed with exit code $exitCode." }
}

function Invoke-NativeText {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][scriptblock]$Command
    )

    $previous = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        $output = & $Command | Out-String
        $exitCode = $LASTEXITCODE
    } finally {
        $PSNativeCommandUseErrorActionPreference = $previous
    }
    if ($exitCode -ne 0) { throw "$Name failed with exit code $exitCode." }
    return $output.Trim()
}

function Assert-SemanticVersion {
    param([Parameter(Mandatory)][string]$Value)

    if ($Value.Length -gt 128) {
        throw "Version exceeds the Docker tag limit of 128 characters: $Value"
    }

    $match = [regex]::Match(
        $Value,
        '^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?<prerelease>[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$'
    )
    if (-not $match.Success) {
        throw "Version is not a Docker-compatible Semantic Version: $Value"
    }

    if ($match.Groups['prerelease'].Success) {
        foreach ($identifier in $match.Groups['prerelease'].Value.Split('.')) {
            if ($identifier -match '^0[0-9]+$') {
                throw "Numeric prerelease identifiers must not contain leading zeroes: $Value"
            }
        }
    }
}

function Copy-ReleaseEntry {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][string]$DestinationRoot,
        [Parameter(Mandatory)][string]$Entry
    )

    $source = Join-Path $SourceRoot $Entry
    if (-not (Test-Path -LiteralPath $source)) { throw "Release input is missing: $source" }
    Copy-Item -LiteralPath $source -Destination $DestinationRoot -Recurse -Force
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-artifacts.ps1')
$artifactRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release')

$releaseBuilderName = $null
$releaseBuilderCreated = $false
$releaseFailure = $null
$builderCleanupFailure = $null
Push-Location -LiteralPath $repositoryRoot
try {
    $headCommit = Invoke-NativeText -Name 'read Git HEAD' -Command { git rev-parse HEAD }
    if ($headCommit -notmatch '^[a-f0-9]{40}$') { throw "Git returned an invalid HEAD: $headCommit" }
    if ([string]::IsNullOrWhiteSpace($Commit)) { $Commit = $headCommit }
    if ($Commit -ne $headCommit) { throw "Requested commit $Commit does not match checked out HEAD $headCommit." }

    $shortCommit = $Commit.Substring(0, 12)
    if ([string]::IsNullOrWhiteSpace($Version)) { $Version = "0.0.0-dev.$shortCommit" }
    Assert-SemanticVersion -Value $Version

    $dirty = -not [string]::IsNullOrWhiteSpace((git status --porcelain=v1 --untracked-files=normal | Out-String).Trim())
    if ($LASTEXITCODE -ne 0) { throw 'Read Git working tree status failed.' }
    if ($dirty -and -not $AllowDirty) {
        throw 'Release build requires a clean Git working tree. Use -AllowDirty only for local development evidence.'
    }

    $buildDate = Invoke-NativeText -Name 'read Git commit timestamp' -Command { git show -s --format=%cI $Commit }
    try { $buildDate = [DateTimeOffset]::Parse($buildDate).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ') } catch {
        throw "Git returned an invalid commit timestamp: $buildDate"
    }
    $sourceDateEpoch = Invoke-NativeText -Name 'read Git commit epoch' -Command { git show -s --format=%ct $Commit }
    if ($sourceDateEpoch -notmatch '^[0-9]{10,}$') { throw "Git returned an invalid commit epoch: $sourceDateEpoch" }

    $goVersion = (& go version).Trim()
    if ($goVersion -notmatch '^go version go1\.26\.5\s') { throw "Go 1.26.5 is required. Actual: $goVersion" }
    $nodeVersion = (& node --version).Trim()
    if ($nodeVersion -ne 'v22.20.0') { throw "Node.js 22.20.0 is required. Actual: $nodeVersion" }
    $npmVersion = (& npm --version).Trim()
    if ($npmVersion -ne '11.15.0') { throw "npm 11.15.0 is required. Actual: $npmVersion" }
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Docker is required to build the canonical release images.' }
    $dockerEngineVersion = Invoke-NativeText -Name 'Docker availability' -Command { docker info --format '{{.ServerVersion}}' }

    $versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
    $composeVersion = (Invoke-NativeText -Name 'Docker Compose version' -Command { docker compose version --short })
    if ($composeVersion -ne $versionLock.MAILWISP_DOCKER_COMPOSE) {
        throw "Docker Compose $composeVersion does not match locked version $($versionLock.MAILWISP_DOCKER_COMPOSE)."
    }
    $expectedBuildxVersion = $versionLock.MAILWISP_BUILDX
    if ($expectedBuildxVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'MAILWISP_BUILDX is missing or invalid in versions.lock.' }
    $buildxVersionOutput = Invoke-NativeText -Name 'Docker Buildx version' -Command { docker buildx version }
    if ($buildxVersionOutput -notmatch "^github\.com/docker/buildx v$([regex]::Escape($expectedBuildxVersion)) [0-9a-f]+$") {
        throw "Docker Buildx $expectedBuildxVersion is required. Actual: $buildxVersionOutput"
    }
    $expectedBuildKitVersion = $versionLock.MAILWISP_BUILDKIT_VERSION
    if ($expectedBuildKitVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'MAILWISP_BUILDKIT_VERSION is missing or invalid in versions.lock.' }
    $buildKitImage = $versionLock.MAILWISP_BUILDKIT_IMAGE
    if ($buildKitImage -notmatch '^moby/buildkit:v[0-9]+\.[0-9]+\.[0-9]+@sha256:[a-f0-9]{64}$') {
        throw 'MAILWISP_BUILDKIT_IMAGE is missing or not pinned by digest in versions.lock.'
    }

    Push-Location -LiteralPath (Join-Path $repositoryRoot 'web')
    try {
        Invoke-Native -Name 'frontend npm ci' -Command { npm ci }
        Invoke-Native -Name 'frontend typecheck' -Command { npm run typecheck }
        Invoke-Native -Name 'frontend lint' -Command { npm run lint }
        Invoke-Native -Name 'frontend unit tests' -Command { npm test }
        Invoke-Native -Name 'frontend production build' -Command { npm run build }
    } finally {
        Pop-Location
    }

    foreach ($staleRoot in @(
        $artifactRoot,
        (Join-Path $repositoryRoot 'artifacts/release-verification'),
        (Join-Path $repositoryRoot 'artifacts/release-e2e')
    )) {
        Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $staleRoot
    }
    [System.IO.Directory]::CreateDirectory($artifactRoot) | Out-Null

    $bundleName = "mailwisp-$Version-linux-amd64"
    $bundleRoot = Join-Path $artifactRoot $bundleName
    $webRoot = Join-Path $bundleRoot 'web'
    $deployRoot = Join-Path $bundleRoot 'deploy'
    $hostDeployRoot = Join-Path $deployRoot 'reference'
    $composeDeployRoot = Join-Path $deployRoot 'compose'
    $imageRoot = Join-Path $bundleRoot 'images'
    foreach ($directory in @($bundleRoot, $webRoot, $hostDeployRoot, $composeDeployRoot, $imageRoot)) {
        [System.IO.Directory]::CreateDirectory($directory) | Out-Null
    }

    $ldflags = "-s -w -X mailwisp/internal/buildinfo.version=$Version -X mailwisp/internal/buildinfo.commit=$Commit -X mailwisp/internal/buildinfo.buildDate=$buildDate"
    $previousGoOS = $env:GOOS
    $previousGoArch = $env:GOARCH
    $previousCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        $goBuildArguments = @(
            'build', '-trimpath', '-buildvcs=false', "-ldflags=$ldflags",
            '-o', (Join-Path $bundleRoot 'mailwisp'), './cmd/mailwisp'
        )
        Invoke-Native -Name 'Linux amd64 binary build' -Command {
            go @goBuildArguments
        }
    } finally {
        $env:GOOS = $previousGoOS
        $env:GOARCH = $previousGoArch
        $env:CGO_ENABLED = $previousCGO
    }

    Copy-Item -Path (Join-Path $repositoryRoot 'web/dist/*') -Destination $webRoot -Recurse -Force
    Copy-ReleaseEntry -SourceRoot (Join-Path $repositoryRoot 'deploy/reference') -DestinationRoot $hostDeployRoot -Entry 'README.md'
    foreach ($entry in @('versions.lock', 'certbot', 'nginx', 'postfix', 'systemd')) {
        Copy-ReleaseEntry -SourceRoot (Join-Path $repositoryRoot 'deploy/reference') -DestinationRoot $hostDeployRoot -Entry $entry
    }
    foreach ($entry in @(
        'README.md', 'OPERATIONS.md', 'prometheus-alerts.example.yml', 'versions.lock', 'Dockerfile',
        'compose.yaml', 'release.compose.yaml', 'backup-verifier.compose.yaml', 'backup-verifier.release.compose.yaml',
        '.env.example', 'mailwisp.env.example', 'nginx', 'postfix'
    )) {
        Copy-ReleaseEntry -SourceRoot (Join-Path $repositoryRoot 'deploy/compose') -DestinationRoot $composeDeployRoot -Entry $entry
    }
    [System.IO.Directory]::CreateDirectory((Join-Path $composeDeployRoot 'secrets')) | Out-Null
    Copy-Item -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/secrets/README.md') -Destination (Join-Path $composeDeployRoot 'secrets')
    Copy-Item -LiteralPath (Join-Path $repositoryRoot 'docs/release-bundle.md') -Destination (Join-Path $bundleRoot 'README.md')

    $bundleEnvironmentPath = Join-Path $composeDeployRoot '.env.example'
    $bundleEnvironment = [System.IO.File]::ReadAllText($bundleEnvironmentPath)
    $bundleEnvironment = [regex]::Replace($bundleEnvironment, '(?m)^MAILWISP_IMAGE_TAG=.*$', "MAILWISP_IMAGE_TAG=$Version")
    if (-not $bundleEnvironment.StartsWith('COMPOSE_FILE=', [StringComparison]::Ordinal)) {
        $bundleEnvironment = "COMPOSE_FILE=compose.yaml:release.compose.yaml`n" + $bundleEnvironment
    }
    [System.IO.File]::WriteAllText($bundleEnvironmentPath, $bundleEnvironment.Replace("`r`n", "`n"), [System.Text.UTF8Encoding]::new($false))

    $imageReferences = [ordered]@{
        app = "mailwisp/app:$Version"
        maintenance = "mailwisp/maintenance:$Version"
        edge = "mailwisp/edge:$Version"
        postfix = "mailwisp/postfix:$Version"
    }
    $releaseBuilderName = 'mailwisp-release-' + [guid]::NewGuid().ToString('N').Substring(0, 16)
    Invoke-Native -Name 'isolated release builder creation' -Command {
        docker buildx create --name $releaseBuilderName --driver docker-container --driver-opt "image=$buildKitImage" | Out-Null
    }
    $releaseBuilderCreated = $true
    $builderInspection = Invoke-NativeText -Name 'isolated release builder bootstrap' -Command {
        docker buildx inspect $releaseBuilderName --bootstrap
    }
    if ($builderInspection -notmatch "(?m)^BuildKit version:\s+v$([regex]::Escape($expectedBuildKitVersion))$") {
        throw "BuildKit $expectedBuildKitVersion is required by the isolated release builder."
    }
    $commonBuildArguments = @(
        '--builder', $releaseBuilderName, '--platform', 'linux/amd64', '--pull', '--no-cache', '--load', '--provenance=false',
        '--build-arg', "MAILWISP_VERSION=$Version",
        '--build-arg', "MAILWISP_COMMIT=$Commit",
        '--build-arg', "MAILWISP_BUILD_DATE=$buildDate",
        '--build-arg', "SOURCE_DATE_EPOCH=$sourceDateEpoch"
    )
    foreach ($target in @('app', 'maintenance', 'edge')) {
        $dockerArguments = @('buildx', 'build') + $commonBuildArguments + @(
            '--target', $target,
            '--tag', $imageReferences[$target],
            '--file', (Join-Path $repositoryRoot 'deploy/compose/Dockerfile'),
            $repositoryRoot
        )
        Invoke-Native -Name "$target release image build" -Command { docker @dockerArguments }
    }
    $postfixArguments = @('buildx', 'build') + $commonBuildArguments + @(
        '--tag', $imageReferences.postfix,
        '--file', (Join-Path $repositoryRoot 'deploy/compose/postfix/Dockerfile'),
        $repositoryRoot
    )
    Invoke-Native -Name 'postfix release image build' -Command { docker @postfixArguments }

    $images = [ordered]@{}
    foreach ($name in $imageReferences.Keys) {
        $reference = $imageReferences[$name]
        $inspection = Invoke-NativeText -Name "$name release image inspection" -Command {
            docker image inspect $reference --format '{{json .}}'
        } | ConvertFrom-Json -DateKind String
        if ($inspection.Architecture -ne 'amd64' -or $inspection.Os -ne 'linux') {
            throw "$reference has unexpected platform $($inspection.Os)/$($inspection.Architecture)."
        }
        if ($inspection.Config.Labels.'org.opencontainers.image.version' -ne $Version -or
            $inspection.Config.Labels.'org.opencontainers.image.revision' -ne $Commit -or
            $inspection.Config.Labels.'org.opencontainers.image.created' -ne $buildDate) {
            throw "$reference OCI release labels do not match the requested build identity."
        }
        $images[$name] = [ordered]@{ reference = $reference; id = $inspection.Id; platform = 'linux/amd64' }
    }

    $imageArchive = Join-Path $imageRoot 'mailwisp-images-linux-amd64.tar'
    Invoke-Native -Name 'release image archive' -Command {
        docker save --output $imageArchive @($imageReferences.Values)
    }

    $releaseManifest = [ordered]@{
        schema_version = 1
        product = 'MailWisp'
        version = $Version
        git_commit = $Commit
        git_dirty = $dirty
        build_date = $buildDate
        platform = 'linux/amd64'
        docker_compose_version = $composeVersion
        docker_engine_version = $dockerEngineVersion
        docker_buildx_version = $expectedBuildxVersion
        docker_buildkit_version = $expectedBuildKitVersion
        docker_buildkit_image = $buildKitImage
        build_cache = 'disabled-isolated-builder'
        images = $images
    }
    $releaseManifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $bundleRoot 'release.json') -Encoding utf8NoBOM

    $checksums = Get-ChildItem -LiteralPath $bundleRoot -File -Recurse |
        Sort-Object { [System.IO.Path]::GetRelativePath($bundleRoot, $_.FullName).Replace('\', '/') } |
        ForEach-Object {
            $relative = [System.IO.Path]::GetRelativePath($bundleRoot, $_.FullName).Replace('\', '/')
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            "$hash  $relative"
        }
    [System.IO.File]::WriteAllLines((Join-Path $bundleRoot 'SHA256SUMS'), $checksums, [System.Text.UTF8Encoding]::new($false))

    $archive = Join-Path $artifactRoot "$bundleName.tar.gz"
    if ($IsLinux) {
        Invoke-Native -Name 'normalized release archive creation' -Command {
            tar --sort=name "--mtime=@$sourceDateEpoch" --owner=0 --group=0 --numeric-owner --use-compress-program='gzip -n' -cf $archive -C $artifactRoot $bundleName
        }
    } else {
        Invoke-Native -Name 'release archive creation' -Command { tar -czf $archive -C $artifactRoot $bundleName }
    }

    [ordered]@{
        schema_version = 1
        product = 'MailWisp'
        version = $Version
        git_commit = $Commit
        git_dirty = $dirty
        build_date = $buildDate
        platform = 'linux/amd64'
        bundle_name = $bundleName
        bundle_directory = $bundleName
        archive = [System.IO.Path]::GetFileName($archive)
        image_archive = "$bundleName/images/mailwisp-images-linux-amd64.tar"
        docker_engine_version = $dockerEngineVersion
        docker_compose_version = $composeVersion
        docker_buildx_version = $expectedBuildxVersion
        docker_buildkit_version = $expectedBuildKitVersion
        docker_buildkit_image = $buildKitImage
        build_cache = 'disabled-isolated-builder'
        images = $images
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $artifactRoot 'build-output.json') -Encoding utf8NoBOM

    Write-Output $archive
} catch {
    $releaseFailure = $_
} finally {
    if ($releaseBuilderCreated) {
        try {
            Invoke-Native -Name 'isolated release builder removal' -Command {
                docker buildx rm $releaseBuilderName | Out-Null
            }
        } catch {
            $builderCleanupFailure = $_
        }
    }
    Pop-Location
}
if ($null -ne $releaseFailure) {
    if ($null -ne $builderCleanupFailure) {
        throw [System.Exception]::new(
            "Release build failed and isolated builder cleanup also failed: $($builderCleanupFailure.Exception.Message)",
            $releaseFailure.Exception
        )
    }
    throw $releaseFailure
}
if ($null -ne $builderCleanupFailure) { throw $builderCleanupFailure }
