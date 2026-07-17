[CmdletBinding()]
param(
    [string]$Version,
    [string]$TrivyCommand = 'trivy'
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

$repositoryRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-artifacts.ps1')
$artifactRoot = Assert-MailWispArtifactPath -RepositoryRoot $repositoryRoot -Path (Join-Path $repositoryRoot 'artifacts/release')
$buildOutputPath = Join-Path $artifactRoot 'build-output.json'
if (-not (Test-Path -LiteralPath $buildOutputPath -PathType Leaf)) { throw 'Run scripts/build-release.ps1 before scanning release images.' }
$build = Get-Content -Raw -LiteralPath $buildOutputPath | ConvertFrom-Json -DateKind String
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = $build.version }
if ($Version -ne $build.version) { throw "Scan version $Version does not match build version $($build.version)." }

$trivy = Get-Command $TrivyCommand -ErrorAction SilentlyContinue
$versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
$expectedTrivyVersion = $versionLock.MAILWISP_TRIVY
if ($expectedTrivyVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'MAILWISP_TRIVY is missing or invalid in versions.lock.' }
if (-not $trivy) { throw "Trivy $expectedTrivyVersion is required to scan release artifacts." }
$trivyVersionOutput = & $trivy.Source --version --format json | Out-String
if ($LASTEXITCODE -ne 0) { throw 'Unable to read Trivy version and database metadata.' }
$trivyBinaryMetadata = $trivyVersionOutput | ConvertFrom-Json -DateKind String
if ($trivyBinaryMetadata.Version -ne $expectedTrivyVersion) { throw "Trivy $expectedTrivyVersion is required. Actual: $($trivyBinaryMetadata.Version)" }
Invoke-Native -Name 'Trivy vulnerability database update' -Command {
    & $trivy.Source image --quiet --download-db-only
}

$securityRoot = Join-Path $artifactRoot 'security'
Remove-MailWispArtifactDirectory -RepositoryRoot $repositoryRoot -Path $securityRoot
[System.IO.Directory]::CreateDirectory($securityRoot) | Out-Null
$reports = [System.Collections.Generic.List[object]]::new()
$blockingFindings = 0

foreach ($property in $build.images.PSObject.Properties) {
    $name = $property.Name
    $image = $property.Value
    $output = Join-Path $securityRoot "trivy-$name-image.json"
    Invoke-Native -Name "$name image vulnerability scan" -Command {
        & $trivy.Source image --quiet --scanners vuln --format json --output $output $image.id
    }
    $report = Get-Content -Raw -LiteralPath $output | ConvertFrom-Json -DateKind String
    if ($report.Metadata.ImageID -ne $image.id) { throw "$name Trivy report does not describe the requested immutable image ID." }
    $findings = @($report.Results | ForEach-Object { @($_.Vulnerabilities) } | Where-Object { $_.Severity -in @('HIGH', 'CRITICAL') })
    $blockingFindings += $findings.Count
    $reports.Add([ordered]@{
        kind = 'image-vulnerability'
        name = $name
        subject = $image.id
        file = [System.IO.Path]::GetFileName($output)
        high = @($findings | Where-Object Severity -eq 'HIGH').Count
        critical = @($findings | Where-Object Severity -eq 'CRITICAL').Count
        sha256 = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash.ToLowerInvariant()
    })
}

$configOutput = Join-Path $securityRoot 'trivy-config.json'
$ignoreFile = Join-Path $repositoryRoot '.trivyignore.yaml'
if (-not (Test-Path -LiteralPath $ignoreFile -PathType Leaf)) { throw 'The reviewed Trivy exception file is missing.' }
Invoke-Native -Name 'release IaC misconfiguration scan' -Command {
    & $trivy.Source config --quiet --severity HIGH,CRITICAL `
        --ignorefile $ignoreFile `
        --skip-dirs (Join-Path $repositoryRoot 'artifacts') `
        --skip-dirs (Join-Path $repositoryRoot 'web/node_modules') `
        --skip-dirs (Join-Path $repositoryRoot 'spikes/frontend/node_modules') `
        --skip-files (Join-Path $repositoryRoot 'deploy/postfix-test/Dockerfile') `
        --format json --output $configOutput $repositoryRoot
}
$configReport = Get-Content -Raw -LiteralPath $configOutput | ConvertFrom-Json -DateKind String
$misconfigurations = @($configReport.Results | ForEach-Object { @($_.Misconfigurations) } | Where-Object { $_.Severity -in @('HIGH', 'CRITICAL') })
$blockingFindings += $misconfigurations.Count
$reports.Add([ordered]@{
    kind = 'iac-misconfiguration'
    file = [System.IO.Path]::GetFileName($configOutput)
    high = @($misconfigurations | Where-Object Severity -eq 'HIGH').Count
    critical = @($misconfigurations | Where-Object Severity -eq 'CRITICAL').Count
    sha256 = (Get-FileHash -LiteralPath $configOutput -Algorithm SHA256).Hash.ToLowerInvariant()
})

$trivyVersionOutput = & $trivy.Source --version --format json | Out-String
if ($LASTEXITCODE -ne 0) { throw 'Unable to read final Trivy database metadata.' }
$trivyMetadata = $trivyVersionOutput | ConvertFrom-Json -DateKind String
if ($trivyMetadata.Version -ne $expectedTrivyVersion -or $trivyMetadata.VulnerabilityDB.Version -lt 1 -or
    [string]::IsNullOrWhiteSpace($trivyMetadata.VulnerabilityDB.UpdatedAt) -or
    $trivyMetadata.CheckBundle.Digest -notmatch '^sha256:[a-f0-9]{64}$') {
    throw 'Trivy vulnerability database or check bundle metadata is missing.'
}
try { $databaseUpdatedAt = [DateTimeOffset]::Parse($trivyMetadata.VulnerabilityDB.UpdatedAt) } catch {
    throw 'Trivy vulnerability database updated_at is invalid.'
}
$databaseAge = [DateTimeOffset]::UtcNow - $databaseUpdatedAt.ToUniversalTime()
if ($databaseAge -lt [TimeSpan]::FromMinutes(-5) -or $databaseAge -gt [TimeSpan]::FromHours(48)) {
    throw "Trivy vulnerability database is outside the 48-hour freshness policy: $($trivyMetadata.VulnerabilityDB.UpdatedAt)"
}

$evidence = [ordered]@{
    schema_version = 1
    product = 'MailWisp'
    version = $Version
    git_commit = $build.git_commit
    trivy_version = $expectedTrivyVersion
    vulnerability_database = [ordered]@{
        schema_version = $trivyMetadata.VulnerabilityDB.Version
        updated_at = $trivyMetadata.VulnerabilityDB.UpdatedAt
        downloaded_at = $trivyMetadata.VulnerabilityDB.DownloadedAt
        next_update = $trivyMetadata.VulnerabilityDB.NextUpdate
    }
    check_bundle = [ordered]@{
        digest = $trivyMetadata.CheckBundle.Digest
        downloaded_at = $trivyMetadata.CheckBundle.DownloadedAt
    }
    policy = [ordered]@{ severities = @('HIGH', 'CRITICAL'); ignore_unfixed = $false; allowed_findings = 0; max_database_age_hours = 48 }
    accepted_risks = @([ordered]@{
        id = 'AVD-DS-0002'
        path = 'deploy/compose/postfix/Dockerfile'
        expires = '2027-01-17'
        reason = 'Postfix master requires root for privileged SMTP and queue initialization before dropping delivery privileges.'
        policy_sha256 = (Get-FileHash -LiteralPath $ignoreFile -Algorithm SHA256).Hash.ToLowerInvariant()
    })
    blocking_findings = $blockingFindings
    reports = @($reports)
}
$evidence | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $securityRoot 'security-index.json') -Encoding utf8NoBOM
if ($blockingFindings -ne 0) { throw "Release security policy found $blockingFindings HIGH or CRITICAL findings." }

Write-Output $securityRoot
