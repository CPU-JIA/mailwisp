[CmdletBinding()]
param(
    [string]$Version,
    [switch]$RunE2E,
    [switch]$AllowDirty
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

function Resolve-SafeRelativePath {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$Relative)
    if ([string]::IsNullOrWhiteSpace($Relative) -or [System.IO.Path]::IsPathRooted($Relative) -or
        $Relative.Contains('\') -or $Relative.Contains(':') -or $Relative.Contains('//')) {
        throw "Unsafe release path: $Relative"
    }
    $segments = $Relative.Split('/', [StringSplitOptions]::RemoveEmptyEntries)
    if ($segments.Count -eq 0 -or $segments -contains '..' -or $segments -contains '.') { throw "Unsafe release path: $Relative" }
    $resolvedRoot = [System.IO.Path]::GetFullPath($Root)
    $resolved = [System.IO.Path]::GetFullPath((Join-Path $resolvedRoot ($segments -join [System.IO.Path]::DirectorySeparatorChar)))
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    $prefix = $resolvedRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($prefix, $comparison)) { throw "Release path escaped extraction root: $Relative" }
    return $resolved
}

function Assert-ChecksumManifest {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][string]$Manifest,
        [switch]$RequireComplete
    )
    $pathComparer = if ($IsWindows) { [StringComparer]::OrdinalIgnoreCase } else { [StringComparer]::Ordinal }
    $pathComparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    $seen = [System.Collections.Generic.HashSet[string]]::new($pathComparer)
    $count = 0
    foreach ($line in Get-Content -LiteralPath $Manifest) {
        if ($line -notmatch '^([a-f0-9]{64})  (.+)$') { throw "Invalid checksum line in $Manifest" }
        $expected = $Matches[1]
        $relative = $Matches[2]
        if (-not $seen.Add($relative)) { throw "Duplicate checksum subject: $relative" }
        $path = Resolve-SafeRelativePath -Root $Root -Relative $relative
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Checksum subject is missing: $relative" }
        $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) { throw "Checksum mismatch: $relative" }
        $count++
    }
    if ($count -eq 0) { throw "Checksum manifest is empty: $Manifest" }
    if ($RequireComplete) {
        $resolvedManifest = [System.IO.Path]::GetFullPath($Manifest)
        $expectedFiles = @(Get-ChildItem -LiteralPath $Root -File -Recurse | Where-Object {
            -not [System.IO.Path]::GetFullPath($_.FullName).Equals($resolvedManifest, $pathComparison)
        })
        if ($expectedFiles.Count -ne $seen.Count) {
            throw "Checksum manifest does not cover every release file: listed=$($seen.Count), files=$($expectedFiles.Count)."
        }
        foreach ($file in $expectedFiles) {
            $relative = [System.IO.Path]::GetRelativePath($Root, $file.FullName).Replace('\', '/')
            if (-not $seen.Contains($relative)) { throw "Release file is not covered by checksums: $relative" }
        }
    }
    return $count
}

function Assert-BuildOutput {
    param(
        [Parameter(Mandatory)]$Build,
        [Parameter(Mandatory)][string]$ExpectedVersion,
        [Parameter(Mandatory)]$VersionLock
    )

    $versionMatch = [regex]::Match(
        $ExpectedVersion,
        '^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?<prerelease>[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$'
    )
    if (-not $versionMatch.Success -or $ExpectedVersion.Length -gt 128) { throw 'Release build output contains an invalid Semantic Version.' }
    if ($versionMatch.Groups['prerelease'].Success) {
        foreach ($identifier in $versionMatch.Groups['prerelease'].Value.Split('.')) {
            if ($identifier -match '^0[0-9]+$') { throw 'Release build output contains an invalid numeric prerelease identifier.' }
        }
    }
    if ($Build.schema_version -ne 1 -or $Build.product -ne 'MailWisp' -or $Build.version -ne $ExpectedVersion -or $Build.git_commit -notmatch '^[a-f0-9]{40}$' -or
        $Build.build_date -notmatch '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' -or
        $Build.platform -ne 'linux/amd64' -or $Build.git_dirty -isnot [bool]) {
        throw 'Release build output identity schema is invalid.'
    }
    $bundleName = "mailwisp-$ExpectedVersion-linux-amd64"
    if ($Build.bundle_name -ne $bundleName -or $Build.bundle_directory -ne $bundleName -or
        $Build.archive -ne "$bundleName.tar.gz" -or
        $Build.image_archive -ne "$bundleName/images/mailwisp-images-linux-amd64.tar") {
        throw 'Release build output contains an unexpected artifact path.'
    }
    if ($Build.docker_compose_version -ne $VersionLock.MAILWISP_DOCKER_COMPOSE -or
        $Build.docker_buildx_version -ne $VersionLock.MAILWISP_BUILDX -or
        $Build.docker_buildkit_version -ne $VersionLock.MAILWISP_BUILDKIT_VERSION -or
        $Build.docker_buildkit_image -ne $VersionLock.MAILWISP_BUILDKIT_IMAGE -or
        $Build.build_cache -ne 'disabled-isolated-builder' -or
        $Build.image_timestamp_rewrite -ne $true -or
        [string]::IsNullOrWhiteSpace($Build.docker_engine_version)) {
        throw 'Release build output does not match the locked container toolchain.'
    }
    if ($null -eq $Build.images) { throw 'Release build output does not contain image identities.' }
    $expectedImages = @('app', 'edge', 'maintenance', 'postfix')
    $actualImages = @($Build.images.PSObject.Properties | ForEach-Object Name | Sort-Object)
    if (($actualImages -join ',') -ne ($expectedImages -join ',')) { throw 'Release build output contains an unexpected image set.' }
    foreach ($name in $expectedImages) {
        $image = $Build.images.$name
        if ($image.reference -ne "mailwisp/${name}:$ExpectedVersion" -or $image.id -notmatch '^sha256:[a-f0-9]{64}$' -or
            $image.platform -ne 'linux/amd64') {
            throw "Release build output contains an invalid $name image identity."
        }
    }
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-artifacts.ps1')
$artifactRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release')
$publishRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $artifactRoot 'publish')
$verificationRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release-verification')
$resultPath = Join-Path $verificationRoot 'result.json'
$buildOutputPath = Join-Path $artifactRoot 'build-output.json'
if (-not (Test-Path -LiteralPath $buildOutputPath -PathType Leaf)) { throw 'Release build output is missing.' }
$build = Get-Content -Raw -LiteralPath $buildOutputPath | ConvertFrom-Json -DateKind String
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = $build.version }
$versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
Assert-BuildOutput -Build $build -ExpectedVersion $Version -VersionLock $versionLock
if ($build.git_dirty -and -not $AllowDirty) { throw 'Release verification requires a clean Git build.' }

Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $verificationRoot
[System.IO.Directory]::CreateDirectory($verificationRoot) | Out-Null
$extractRoot = Join-Path $verificationRoot 'extracted'
[System.IO.Directory]::CreateDirectory($extractRoot) | Out-Null

$archive = Resolve-SafeRelativePath -Root $publishRoot -Relative ([string]$build.archive)
$outerChecksums = Join-Path $publishRoot 'SHA256SUMS'
foreach ($required in @($archive, $outerChecksums)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Release verification input is missing: $required" }
}

$startedAt = [DateTimeOffset]::UtcNow
$stage = 'outer checksums'
$failure = $null
$outerCount = 0
$innerCount = 0
$renderedServices = 0
$e2ePassed = $false
$managedEnvironment = @(
    'MAILWISP_ENV_FILE', 'MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE', 'MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE',
    'MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE', 'MAILWISP_WEB_DOMAIN', 'MAILWISP_SMTP_HOST',
    'MAILWISP_MAIL_DOMAIN', 'MAILWISP_CERT_NAME', 'MAILWISP_IMAGE_TAG'
)
$originalEnvironment = @{}
foreach ($name in $managedEnvironment) { $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name) }
try {
    $outerCount = Assert-ChecksumManifest -Root $publishRoot -Manifest $outerChecksums -RequireComplete
    if (-not @(Get-Content -LiteralPath $outerChecksums | Where-Object { $_ -match "^[a-f0-9]{64}  $([regex]::Escape($build.archive))$" }).Count) {
        throw 'The release archive is not an outer checksum subject.'
    }

    $stage = 'archive path validation'
    $archiveEntries = Invoke-NativeText -Name 'release archive listing' -Command { tar -tzf $archive }
    $entries = @($archiveEntries -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($entries.Count -eq 0) { throw 'Release archive is empty.' }
    $entryComparer = if ($IsWindows) { [StringComparer]::OrdinalIgnoreCase } else { [StringComparer]::Ordinal }
    $entryNames = [System.Collections.Generic.HashSet[string]]::new($entryComparer)
    foreach ($entry in $entries) {
        $trimmed = $entry.TrimEnd('/')
        if ([string]::IsNullOrWhiteSpace($trimmed)) { continue }
        if ($trimmed -ne $build.bundle_directory -and -not $trimmed.StartsWith("$($build.bundle_directory)/", [StringComparison]::Ordinal)) {
            throw "Release archive contains an entry outside the expected bundle root: $trimmed"
        }
        if (-not $entryNames.Add($trimmed)) { throw "Release archive contains a duplicate entry: $trimmed" }
        [void](Resolve-SafeRelativePath -Root $extractRoot -Relative $trimmed)
    }
    $archiveDetails = Invoke-NativeText -Name 'release archive metadata listing' -Command { tar -tvzf $archive }
    foreach ($line in @($archiveDetails -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        if ($line[0] -notin @('-', 'd')) {
            throw 'Release archive contains a symbolic link, hard link or unsupported entry type.'
        }
    }

    $stage = 'archive extraction'
    Invoke-Native -Name 'release archive extraction' -Command { tar -xzf $archive -C $extractRoot }
    $bundleRoot = Resolve-SafeRelativePath -Root $extractRoot -Relative ([string]$build.bundle_directory)
    if (-not (Test-Path -LiteralPath $bundleRoot -PathType Container)) { throw 'Release archive did not contain the expected bundle root.' }
    $links = @(Get-ChildItem -LiteralPath $bundleRoot -Force -Recurse | Where-Object {
        ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or -not [string]::IsNullOrWhiteSpace($_.LinkType)
    })
    if ($links.Count -ne 0) { throw 'Release bundle contains a symbolic link or reparse point.' }

    $stage = 'inner checksums'
    $innerCount = Assert-ChecksumManifest -Root $bundleRoot -Manifest (Join-Path $bundleRoot 'SHA256SUMS') -RequireComplete
    $manifest = Get-Content -Raw -LiteralPath (Join-Path $bundleRoot 'release.json') | ConvertFrom-Json -DateKind String
    if ($manifest.version -ne $Version -or $manifest.git_commit -ne $build.git_commit -or $manifest.platform -ne 'linux/amd64' -or
        $manifest.git_dirty -ne $build.git_dirty -or $manifest.docker_buildx_version -ne $build.docker_buildx_version -or
        $manifest.docker_buildkit_version -ne $build.docker_buildkit_version -or $manifest.docker_buildkit_image -ne $build.docker_buildkit_image -or
        $manifest.build_cache -ne $build.build_cache -or $manifest.image_timestamp_rewrite -ne $true) {
        throw 'Release manifest identity does not match build output.'
    }

    $stage = 'release image replacement safety'
    foreach ($property in $build.images.PSObject.Properties) {
        $image = $property.Value
        $existing = & docker image inspect $image.reference --format '{{json .}}' 2>$null | Out-String
        if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace($existing)) {
            $inspection = $existing | ConvertFrom-Json -DateKind String
            if ($inspection.Config.Labels.'org.opencontainers.image.version' -ne $Version -or $inspection.Config.Labels.'org.opencontainers.image.revision' -ne $build.git_commit) {
                throw "Refusing to replace an image tag not owned by this release: $($image.reference)"
            }
            Invoke-Native -Name "remove pre-load $($property.Name) image" -Command { docker image rm $image.reference | Out-Null }
        }
    }

    $stage = 'release image load'
    Invoke-Native -Name 'load release image archive' -Command {
        docker load --input (Join-Path $bundleRoot 'images/mailwisp-images-linux-amd64.tar')
    }
    foreach ($property in $build.images.PSObject.Properties) {
        $image = $property.Value
        $inspection = Invoke-NativeText -Name "$($property.Name) loaded image inspection" -Command {
            docker image inspect $image.reference --format '{{json .}}'
        } | ConvertFrom-Json -DateKind String
        if ($inspection.Id -ne $image.id -or $inspection.Os -ne 'linux' -or $inspection.Architecture -ne 'amd64') {
            throw "Loaded image identity mismatch: $($image.reference)"
        }
        if ($inspection.Config.Labels.'org.opencontainers.image.version' -ne $Version -or
            $inspection.Config.Labels.'org.opencontainers.image.revision' -ne $build.git_commit -or
            $inspection.Config.Labels.'org.opencontainers.image.created' -ne $build.build_date) {
            throw "Loaded image OCI identity mismatch: $($image.reference)"
        }
    }

    $stage = 'release binary identity'
    $versionJSON = Invoke-NativeText -Name 'release binary version' -Command {
        docker run --rm --platform linux/amd64 --network none --read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges `
            --mount "type=bind,source=$bundleRoot,target=/release,readonly" `
            --entrypoint /release/mailwisp $build.images.app.reference version --json
    } | ConvertFrom-Json -DateKind String
    if ($versionJSON.name -ne 'MailWisp' -or $versionJSON.version -ne $Version -or $versionJSON.commit -ne $build.git_commit -or $versionJSON.build_date -ne $build.build_date) {
        throw 'Release binary reports an unexpected build identity.'
    }

    $stage = 'release Compose render'
    $fixtureRoot = Join-Path $verificationRoot 'compose-fixture'
    [System.IO.Directory]::CreateDirectory($fixtureRoot) | Out-Null
    $mailwispEnvironment = Join-Path $fixtureRoot 'mailwisp.env'
    $secret = Join-Path $fixtureRoot 'secret.txt'
    [System.IO.File]::WriteAllText($mailwispEnvironment, "MAILWISP_PUBLIC_DOMAINS=example.com`nMAILWISP_LMTP_HOSTNAME=mx.example.com`n")
    [System.IO.File]::WriteAllText($secret, "release-verification-placeholder`n")
    $env:MAILWISP_ENV_FILE = $mailwispEnvironment
    $env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE = $secret
    $env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE = $secret
    $env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE = $secret
    $env:MAILWISP_WEB_DOMAIN = 'mail.example.com'
    $env:MAILWISP_SMTP_HOST = 'mx.example.com'
    $env:MAILWISP_MAIL_DOMAIN = 'example.com'
    $env:MAILWISP_CERT_NAME = 'mail.example.com'
    $env:MAILWISP_IMAGE_TAG = $Version
    $composeBase = Join-Path $bundleRoot 'deploy/compose/compose.yaml'
    $composeRelease = Join-Path $bundleRoot 'deploy/compose/release.compose.yaml'
    $rendered = Invoke-NativeText -Name 'prebuilt release Compose render' -Command {
        docker compose --profile tools --profile maintenance -f $composeBase -f $composeRelease config --format json
    } | ConvertFrom-Json -DateKind String
    $renderedServices = @($rendered.services.PSObject.Properties).Count
    foreach ($service in @('migrate', 'app', 'maintenance', 'edge', 'postfix')) {
        if ($null -ne $rendered.services.$service.build) { throw "Release Compose retained a source build for $service." }
        if ($rendered.services.$service.pull_policy -ne 'never') { throw "Release Compose did not disable remote pulls for $service." }
        if ($rendered.services.$service.image -notmatch ":$([regex]::Escape($Version))$") { throw "Release Compose did not pin $service to version $Version." }
    }
    if ($renderedServices -lt 7) { throw 'Release Compose rendered an incomplete service set.' }

    if ($RunE2E) {
        $stage = 'prebuilt release production E2E'
        & (Join-Path $repositoryRoot 'scripts/e2e-compose.ps1') `
            -ComposeFiles @($composeBase, $composeRelease) `
            -ImageTag $Version `
            -OutputDirectory 'artifacts/release-e2e' `
            -SkipBuild
        if ($LASTEXITCODE -ne 0) { throw 'Prebuilt release Production E2E failed.' }
        $e2eResult = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'artifacts/release-e2e/result.json') | ConvertFrom-Json -DateKind String
        if ($e2eResult.status -ne 'passed') { throw 'Prebuilt release Production E2E did not report passed.' }
        $e2ePassed = $true
    }
} catch {
    $failure = $_
} finally {
    foreach ($name in $managedEnvironment) { [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name]) }
}

$duration = ([DateTimeOffset]::UtcNow - $startedAt).TotalSeconds
if ($null -eq $failure) {
    [ordered]@{
        schema_version = 1
        observed_at = [DateTimeOffset]::UtcNow.ToString('o')
        duration_seconds = $duration
        status = 'passed'
        version = $Version
        git_commit = $build.git_commit
        archive_sha256 = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        checks = [ordered]@{
            outer_checksum_subjects = $outerCount
            inner_checksum_subjects = $innerCount
            image_identities = @($build.images.PSObject.Properties).Count
            compose_services = $renderedServices
            source_builds_remaining = 0
            production_e2e = if ($RunE2E) { $e2ePassed } else { $null }
        }
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $resultPath -Encoding utf8NoBOM
    Write-Output $resultPath
    return
}

[ordered]@{
    schema_version = 1
    observed_at = [DateTimeOffset]::UtcNow.ToString('o')
    duration_seconds = $duration
    status = 'failed'
    stage = $stage
    error = $failure.Exception.Message
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $resultPath -Encoding utf8NoBOM
throw $failure
