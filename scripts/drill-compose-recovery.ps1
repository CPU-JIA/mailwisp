[CmdletBinding()]
param(
    [string]$OutputDirectory = 'artifacts/disaster-recovery',

    [ValidateRange(0, 65535)]
    [int]$HTTPPort = 0,

    [ValidateRange(0, 65535)]
    [int]$HTTPSPort = 0,

    [ValidateRange(0, 65535)]
    [int]$SMTPPort = 0,

    [switch]$SkipBuild
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

function Invoke-NativeWithRetry {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][scriptblock]$Command,
        [ValidateRange(1, 10)][int]$Attempts = 5
    )

    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            Invoke-Native -Name $Name -Command $Command
            return
        } catch {
            if ($attempt -eq $Attempts) { throw }
            Start-Sleep -Seconds ([Math]::Pow(2, $attempt - 1))
        }
    }
}

function Assert-NoReparsePoint {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][string]$Child
    )

    foreach ($candidate in @($Root)) {
        if ([System.IO.Directory]::Exists($candidate) -or [System.IO.File]::Exists($candidate)) {
            if (([System.IO.File]::GetAttributes($candidate) -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Disaster recovery artifact root is a symbolic link or junction: $candidate"
            }
        }
    }
    $current = $Root
    $relative = [System.IO.Path]::GetRelativePath($Root, $Child)
    foreach ($segment in $relative.Split([System.IO.Path]::DirectorySeparatorChar, [StringSplitOptions]::RemoveEmptyEntries)) {
        $current = Join-Path $current $segment
        if (-not ([System.IO.Directory]::Exists($current) -or [System.IO.File]::Exists($current))) { continue }
        if (([System.IO.File]::GetAttributes($current) -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Disaster recovery output path contains a symbolic link or junction: $current"
        }
    }
}

function Protect-Fixture {
    param([Parameter(Mandatory)][string]$Path)

    if ($IsWindows) {
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        Invoke-Native -Name 'disaster recovery fixture ACL' -Command {
            icacls.exe $Path /inheritance:r /grant:r "*$sid`:(OI)(CI)F" | Out-Null
        }
        return
    }
    Invoke-Native -Name 'disaster recovery fixture permissions' -Command { chmod 0700 $Path }
}

function Get-LoopbackPort {
    param(
        [int]$Requested,
        [Parameter(Mandatory)][AllowEmptyCollection()][System.Collections.Generic.HashSet[int]]$Used
    )

    if ($Requested -ne 0) {
        if (-not $Used.Add($Requested)) { throw "TCP port $Requested was requested more than once." }
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Requested)
        try { $listener.Start() } finally { $listener.Stop() }
        return $Requested
    }
    do {
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
        try {
            $listener.Start()
            $candidate = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
        } finally {
            $listener.Stop()
        }
    } while (-not $Used.Add($candidate))
    return $candidate
}

function New-DrillCertificate {
    param([Parameter(Mandatory)][string]$CertificateRoot)

    $liveRoot = Join-Path $CertificateRoot 'live/drill'
    [System.IO.Directory]::CreateDirectory($liveRoot) | Out-Null
    $rsa = [System.Security.Cryptography.RSA]::Create(2048)
    $certificate = $null
    try {
        $request = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
            'CN=mailwisp.test', $rsa, [System.Security.Cryptography.HashAlgorithmName]::SHA256,
            [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
        )
        $san = [System.Security.Cryptography.X509Certificates.SubjectAlternativeNameBuilder]::new()
        $san.AddDnsName('mailwisp.test')
        $san.AddDnsName('mx.mailwisp.test')
        $san.AddIpAddress([System.Net.IPAddress]::Loopback)
        $request.CertificateExtensions.Add($san.Build())
        $request.CertificateExtensions.Add([System.Security.Cryptography.X509Certificates.X509BasicConstraintsExtension]::new($false, $false, 0, $true))
        $request.CertificateExtensions.Add([System.Security.Cryptography.X509Certificates.X509KeyUsageExtension]::new(
            [System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]::DigitalSignature -bor
            [System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]::KeyEncipherment, $true
        ))
        $certificate = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddHours(-1), [DateTimeOffset]::UtcNow.AddDays(7))
        $utf8 = [System.Text.UTF8Encoding]::new($false)
        [System.IO.File]::WriteAllText((Join-Path $liveRoot 'fullchain.pem'), $certificate.ExportCertificatePem(), $utf8)
        [System.IO.File]::WriteAllText((Join-Path $liveRoot 'privkey.pem'), $rsa.ExportPkcs8PrivateKeyPem(), $utf8)
    } finally {
        if ($null -ne $certificate) { $certificate.Dispose() }
        $rsa.Dispose()
    }
}

function Wait-HTTPSReady {
    param([Parameter(Mandatory)][string]$URI, [int]$TimeoutSeconds = 120)

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        try {
            $response = Invoke-WebRequest -Uri $URI -SkipCertificateCheck -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return }
        } catch { Start-Sleep -Milliseconds 500 }
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    throw "HTTPS readiness did not succeed within $TimeoutSeconds seconds: $URI"
}

function Wait-SMTPReady {
    param([Parameter(Mandatory)][int]$Port, [int]$TimeoutSeconds = 60)

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $client = [System.Net.Sockets.TcpClient]::new()
        $reader = $null
        try {
            $client.Connect('127.0.0.1', $Port)
            $stream = $client.GetStream()
            $stream.ReadTimeout = 3000
            $reader = [System.IO.StreamReader]::new($stream, [System.Text.Encoding]::ASCII, $false, 1024, $true)
            if (($reader.ReadLine() ?? '').StartsWith('220 ')) { return }
        } catch { Start-Sleep -Milliseconds 500 } finally {
            if ($null -ne $reader) { $reader.Dispose() }
            $client.Dispose()
        }
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    throw "SMTP readiness did not succeed within $TimeoutSeconds seconds on 127.0.0.1:$Port."
}

function Wait-PostgresHealthy {
    param([Parameter(Mandatory)][string[]]$ComposeArguments, [int]$TimeoutSeconds = 120)

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastObservation = 'container not created'
    do {
        $previous = $PSNativeCommandUseErrorActionPreference
        try {
            $PSNativeCommandUseErrorActionPreference = $false
            $container = (& docker compose @ComposeArguments ps --all -q postgres 2>$null | Out-String).Trim()
            $composeExitCode = $LASTEXITCODE
        } finally {
            $PSNativeCommandUseErrorActionPreference = $previous
        }
        if ($composeExitCode -ne 0) {
            $lastObservation = "docker compose ps exit $composeExitCode"
            Start-Sleep -Milliseconds 500
            continue
        }
        if ($container -ne '') {
            $previous = $PSNativeCommandUseErrorActionPreference
            try {
                $PSNativeCommandUseErrorActionPreference = $false
                $stateJSON = (& docker inspect --format '{{json .State}}' $container 2>$null | Out-String).Trim()
                $inspectExitCode = $LASTEXITCODE
            } finally {
                $PSNativeCommandUseErrorActionPreference = $previous
            }
            if ($inspectExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($stateJSON)) {
                $lastObservation = "docker inspect exit $inspectExitCode"
                Start-Sleep -Milliseconds 500
                continue
            }
            $state = $stateJSON | ConvertFrom-Json
            if ($state.Status -in @('exited', 'dead')) {
                throw "PostgreSQL exited before becoming healthy: status=$($state.Status), exit_code=$($state.ExitCode)."
            }
            $lastObservation = "status=$($state.Status), health=$($state.Health.Status)"
            if ($state.Health.Status -eq 'healthy') { return }
        }
        Start-Sleep -Milliseconds 500
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    throw "PostgreSQL did not become healthy within $TimeoutSeconds seconds; last observation: $lastObservation."
}

function Get-DatabaseSnapshot {
    param([Parameter(Mandatory)][string[]]$ComposeArguments)

    $query = @'
SELECT json_build_object(
  'migration_version', (SELECT max(version_id) FILTER (WHERE is_applied) FROM goose_db_version),
  'inboxes', (SELECT count(*) FROM inboxes),
  'messages', (SELECT count(*) FROM messages),
  'contents', (SELECT count(*) FROM mail_contents),
  'parses', (SELECT count(*) FROM mail_content_parses),
  'capabilities', (SELECT count(*) FROM inbox_capabilities),
  'create_quotas', (SELECT count(*) FROM inbox_create_quotas),
  'duckmail_credentials', (SELECT count(*) FROM duckmail_credentials),
  'cloudflare_inbox_ids', (SELECT count(*) FROM cloudflare_temp_inbox_ids),
  'cloudflare_message_ids', (SELECT count(*) FROM cloudflare_temp_message_ids),
  'content_bytes', (SELECT COALESCE(sum(size_bytes), 0) FROM mail_contents),
  'content_catalog', (SELECT COALESCE(json_agg(json_build_array(content_key, size_bytes) ORDER BY content_key), '[]'::json) FROM mail_contents),
  'fk_orphans', (
    (SELECT count(*) FROM messages m LEFT JOIN inboxes i ON i.id = m.inbox_id LEFT JOIN mail_contents c ON c.content_key = m.content_key WHERE i.id IS NULL OR c.content_key IS NULL) +
    (SELECT count(*) FROM mail_content_parses p LEFT JOIN mail_contents c ON c.content_key = p.content_key WHERE c.content_key IS NULL) +
    (SELECT count(*) FROM inbox_capabilities a LEFT JOIN inboxes i ON i.id = a.inbox_id WHERE i.id IS NULL) +
    (SELECT count(*) FROM duckmail_credentials d LEFT JOIN inboxes i ON i.id = d.inbox_id WHERE i.id IS NULL) +
    (SELECT count(*) FROM cloudflare_temp_inbox_ids c LEFT JOIN inboxes i ON i.id = c.inbox_id WHERE i.id IS NULL) +
    (SELECT count(*) FROM cloudflare_temp_message_ids c LEFT JOIN inboxes i ON i.id = c.inbox_id LEFT JOIN messages m ON m.id = c.message_id WHERE i.id IS NULL OR m.id IS NULL)
  ),
  'non_uuid_v7', (
    (SELECT count(*) FROM inboxes WHERE uuid_extract_version(id) IS DISTINCT FROM 7) +
    (SELECT count(*) FROM messages WHERE uuid_extract_version(id) IS DISTINCT FROM 7) +
    (SELECT count(*) FROM inbox_capabilities WHERE uuid_extract_version(id) IS DISTINCT FROM 7)
  )
)::text;
'@
    $raw = Invoke-NativeText -Name 'PostgreSQL recovery snapshot' -Command {
        docker compose @ComposeArguments exec -T postgres psql --quiet --no-align --tuples-only --set ON_ERROR_STOP=1 -U mailwisp -d mailwisp --command $query
    }
    return $raw | ConvertFrom-Json
}

function Test-DockerVolumeExists {
    param([Parameter(Mandatory)][string]$Name)

    $previous = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        docker volume inspect $Name *> $null
        return $LASTEXITCODE -eq 0
    } finally {
        $PSNativeCommandUseErrorActionPreference = $previous
    }
}

function Assert-PostfixQueueEmpty {
    param(
        [Parameter(Mandatory)][string[]]$ComposeArguments,
        [switch]$Stopped
    )

    $queue = if ($Stopped) {
        Invoke-NativeText -Name 'stopped Postfix volume queue inspection' -Command {
            docker compose @ComposeArguments run --rm --no-deps --entrypoint postqueue postfix -p
        }
    } else {
        Invoke-NativeText -Name 'running Postfix queue inspection' -Command {
            docker compose @ComposeArguments exec -T postfix postqueue -p
        }
    }
    if ($queue -notmatch 'Mail queue is empty') { throw 'Postfix queue is not empty before backup.' }
}

function Assert-ComposeVolumeOwnership {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Project,
        [Parameter(Mandatory)][string]$LogicalName
    )

    $volumeName = $Name
    $raw = Invoke-NativeText -Name "ownership inspection for volume $volumeName" -Command { docker volume inspect $volumeName }
    $volume = @($raw | ConvertFrom-Json)[0]
    if ($volume.Labels.'com.docker.compose.project' -ne $Project -or $volume.Labels.'com.docker.compose.volume' -ne $LogicalName) {
        throw "Volume ownership proof failed for $volumeName; refusing destructive teardown."
    }
}

function Assert-NoUnexpectedComposeVolumes {
    param(
        [Parameter(Mandatory)][string]$Project,
        [Parameter(Mandatory)][string[]]$AllowedLogicalNames
    )

    $names = Invoke-NativeText -Name 'isolated Compose volume enumeration' -Command {
        docker volume ls --filter "label=com.docker.compose.project=$Project" --format '{{.Name}}'
    }
    foreach ($volumeName in @($names -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
        $raw = Invoke-NativeText -Name "ownership inspection for project volume $volumeName" -Command { docker volume inspect $volumeName }
        $volume = @($raw | ConvertFrom-Json)[0]
        $logicalName = [string]$volume.Labels.'com.docker.compose.volume'
        if ($volume.Labels.'com.docker.compose.project' -ne $Project -or $logicalName -notin $AllowedLogicalNames) {
            throw "Unexpected volume $volumeName with logical name $logicalName; refusing destructive teardown."
        }
    }
}

function Save-DrillDiagnostics {
    param(
        [Parameter(Mandatory)][string[]]$ComposeArguments,
        [Parameter(Mandatory)][string[]]$VerifierComposeArguments,
        [Parameter(Mandatory)][string]$Destination
    )

    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $redactions = [System.Collections.Generic.List[string]]::new()
    if ($null -ne $stateRoot) {
        $flowState = Join-Path $stateRoot 'flow-state.json'
        if ([System.IO.File]::Exists($flowState)) {
            try {
                $testAddress = (Get-Content -Raw -LiteralPath $flowState | ConvertFrom-Json).address
                if (-not [string]::IsNullOrWhiteSpace($testAddress)) { $redactions.Add([string]$testAddress) }
            } catch {
                # Diagnostics remain best-effort; no state-file parse error may hide the original failure.
            }
        }
    }
    foreach ($capture in @(
        @{ Name = 'compose-ps.json'; Command = { & docker compose @ComposeArguments ps -a --format json 2>&1 | Out-String } },
        @{ Name = 'compose.log'; Command = { & docker compose @ComposeArguments logs --no-color postgres migrate app edge postfix 2>&1 | Out-String } },
        @{ Name = 'volumes.txt'; Command = { & docker volume ls --filter "label=com.docker.compose.project=$projectName" --format '{{.Name}}' 2>&1 | Out-String } },
        @{ Name = 'manifest.json'; Command = { & docker compose @VerifierComposeArguments run --rm --no-deps --entrypoint cat backup-verifier /backups/recovery-bundle/manifest.json 2>&1 | Out-String } }
    )) {
        try {
            $diagnostic = [string](& $capture.Command)
            foreach ($redaction in $redactions) { $diagnostic = $diagnostic.Replace($redaction, '<redacted-test-address>', [StringComparison]::OrdinalIgnoreCase) }
            $diagnostic = [Text.RegularExpressions.Regex]::Replace($diagnostic, '(?i)\b[a-z0-9]+@mailwisp\.test\b', '<redacted-test-address>')
            [System.IO.File]::WriteAllText((Join-Path $Destination $capture.Name), $diagnostic, $utf8)
        } catch {
            [System.IO.File]::WriteAllText((Join-Path $Destination "$($capture.Name).error.txt"), $_.Exception.Message, $utf8)
        }
    }
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$artifactRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot 'artifacts'))
$outputRoot = if ([System.IO.Path]::IsPathRooted($OutputDirectory)) {
    [System.IO.Path]::GetFullPath($OutputDirectory)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
}
$pathComparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
$artifactPrefix = $artifactRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if (-not $outputRoot.StartsWith($artifactPrefix, $pathComparison)) {
    throw 'Disaster recovery output must be a subdirectory of the repository artifacts directory.'
}
Assert-NoReparsePoint -Root $artifactRoot -Child $outputRoot
if ([System.IO.Directory]::Exists($outputRoot)) { [System.IO.Directory]::Delete($outputRoot, $true) }
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null

$usedPorts = [System.Collections.Generic.HashSet[int]]::new()
$HTTPPort = Get-LoopbackPort -Requested $HTTPPort -Used $usedPorts
$HTTPSPort = Get-LoopbackPort -Requested $HTTPSPort -Used $usedPorts
$SMTPPort = Get-LoopbackPort -Requested $SMTPPort -Used $usedPorts
$temporaryRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$fixtureRoot = [System.IO.Path]::GetFullPath((Join-Path $temporaryRoot ("mailwisp-disaster-recovery-" + [guid]::NewGuid().ToString('N'))))
$temporaryPrefix = $temporaryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if (-not $fixtureRoot.StartsWith($temporaryPrefix, $pathComparison)) { throw 'Disaster recovery fixture path escaped the temporary directory.' }
$projectName = 'mailwisp-drill-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$verifierProjectName = "${projectName}-verifier"
$backupVolume = 'mailwisp-drill-backup-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$baseCompose = Join-Path $repositoryRoot 'deploy/compose/compose.yaml'
$productionOverlay = Join-Path $repositoryRoot 'deploy/compose/production-e2e.compose.yaml'
$recoveryOverlay = Join-Path $repositoryRoot 'deploy/compose/disaster-recovery.compose.yaml'
$verifierCompose = Join-Path $repositoryRoot 'deploy/compose/backup-verifier.compose.yaml'
$verifierRecoveryOverlay = Join-Path $repositoryRoot 'deploy/compose/disaster-recovery-verifier.compose.yaml'
$composeArguments = @('--profile', 'maintenance', '-p', $projectName, '-f', $baseCompose, '-f', $productionOverlay, '-f', $recoveryOverlay)
$verifierComposeArguments = @('-p', $verifierProjectName, '-f', $verifierCompose, '-f', $verifierRecoveryOverlay)
$managedEnvironment = @(
    'MAILWISP_ENV_FILE', 'MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE', 'MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE',
    'MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE', 'MAILWISP_WEB_DOMAIN', 'MAILWISP_SMTP_HOST',
    'MAILWISP_MAIL_DOMAIN', 'MAILWISP_CERT_NAME', 'MAILWISP_E2E_CERT_ROOT', 'MAILWISP_E2E_HTTP_PORT',
    'MAILWISP_E2E_HTTPS_PORT', 'MAILWISP_E2E_SMTP_PORT', 'MAILWISP_DR_BACKUP_VOLUME',
    'MAILWISP_DR_BASE_URL', 'MAILWISP_DR_SMTP_PORT', 'MAILWISP_DR_STATE_ROOT'
)
$originalEnvironment = @{}
foreach ($name in $managedEnvironment) { $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name) }

$startedAt = [DateTimeOffset]::UtcNow
$failure = $null
$stage = 'fixture setup'
$composeVersion = ''
$pgDumpVersion = ''
$pgRestoreVersion = ''
$manifest = $null
$sourceSnapshot = $null
$restoredSnapshot = $null
$finalSnapshot = $null
$originalVolumesRemoved = $false
$browserSessionRestored = $false
$postRestoreDelivery = $false

try {
    [System.IO.Directory]::CreateDirectory($fixtureRoot) | Out-Null
    Protect-Fixture -Path $fixtureRoot
    $certificateRoot = Join-Path $fixtureRoot 'letsencrypt'
    $stateRoot = Join-Path $fixtureRoot 'state'
    [System.IO.Directory]::CreateDirectory($stateRoot) | Out-Null
    New-DrillCertificate -CertificateRoot $certificateRoot
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $mailwispEnvironment = Join-Path $fixtureRoot 'mailwisp.env'
    $postgresPassword = Join-Path $fixtureRoot 'postgres_password.txt'
    $browserSessionKey = Join-Path $fixtureRoot 'browser_session_key.txt'
    $createQuotaKey = Join-Path $fixtureRoot 'create_quota_hmac_key.txt'
    [System.IO.File]::WriteAllText($mailwispEnvironment, "MAILWISP_LOG_LEVEL=info`nMAILWISP_TRUSTED_PROXY_CIDRS=172.16.0.0/12`nMAILWISP_PUBLIC_DOMAINS=mailwisp.test`nMAILWISP_LMTP_HOSTNAME=mx.mailwisp.test`n", $utf8)
    [System.IO.File]::WriteAllText($postgresPassword, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)
    [System.IO.File]::WriteAllText($browserSessionKey, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)
    [System.IO.File]::WriteAllText($createQuotaKey, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)

    $env:MAILWISP_ENV_FILE = $mailwispEnvironment
    $env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE = $postgresPassword
    $env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE = $browserSessionKey
    $env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE = $createQuotaKey
    $env:MAILWISP_WEB_DOMAIN = 'mailwisp.test'
    $env:MAILWISP_SMTP_HOST = 'mx.mailwisp.test'
    $env:MAILWISP_MAIL_DOMAIN = 'mailwisp.test'
    $env:MAILWISP_CERT_NAME = 'drill'
    $env:MAILWISP_E2E_CERT_ROOT = $certificateRoot
    $env:MAILWISP_E2E_HTTP_PORT = [string]$HTTPPort
    $env:MAILWISP_E2E_HTTPS_PORT = [string]$HTTPSPort
    $env:MAILWISP_E2E_SMTP_PORT = [string]$SMTPPort
    $env:MAILWISP_DR_BACKUP_VOLUME = $backupVolume
    $env:MAILWISP_DR_BASE_URL = "https://127.0.0.1:$HTTPSPort"
    $env:MAILWISP_DR_SMTP_PORT = [string]$SMTPPort
    $env:MAILWISP_DR_STATE_ROOT = $stateRoot

    $stage = 'Docker and Compose validation'
    Invoke-Native -Name 'docker info' -Command { docker info --format '{{.ServerVersion}}' }
    $versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
    $composeVersion = (Invoke-NativeText -Name 'Docker Compose version' -Command { docker compose version --short })
    if ($composeVersion -ne $versionLock.MAILWISP_DOCKER_COMPOSE) {
        throw "Docker Compose $composeVersion does not match locked version $($versionLock.MAILWISP_DOCKER_COMPOSE)."
    }
    Invoke-Native -Name 'external disaster recovery backup volume creation' -Command {
        docker volume create --label mailwisp.managed=disaster-recovery-drill --label "mailwisp.drill.project=$projectName" $backupVolume | Out-Null
    }
    Invoke-Native -Name 'disaster recovery Compose render' -Command { docker compose @composeArguments config --quiet }
    Invoke-Native -Name 'independent verifier Compose render' -Command { docker compose @verifierComposeArguments config --quiet }

    if (-not $SkipBuild) {
        $stage = 'production image build'
        Invoke-Native -Name 'disaster recovery production images' -Command { docker compose @composeArguments build --pull app maintenance edge postfix }
        $stage = 'pinned PostgreSQL image pull'
        Invoke-NativeWithRetry -Name 'disaster recovery PostgreSQL pull' -Command { docker compose @composeArguments pull postgres }
    }

    $stage = 'maintenance toolchain validation'
    $pgDumpVersion = Invoke-NativeText -Name 'maintenance pg_dump version' -Command {
        docker compose @composeArguments run --rm --no-deps --entrypoint pg_dump maintenance --version
    }
    $pgRestoreVersion = Invoke-NativeText -Name 'maintenance pg_restore version' -Command {
        docker compose @composeArguments run --rm --no-deps --entrypoint pg_restore maintenance --version
    }
    if ($pgDumpVersion -ne 'pg_dump (PostgreSQL) 18.4' -or $pgRestoreVersion -ne 'pg_restore (PostgreSQL) 18.4') {
        throw "Maintenance PostgreSQL tools are not exactly 18.4: $pgDumpVersion / $pgRestoreVersion"
    }

    $stage = 'source stack startup'
    Invoke-Native -Name 'source Compose stack startup' -Command { docker compose @composeArguments up -d postgres migrate app edge postfix }
    Wait-HTTPSReady -URI "$($env:MAILWISP_DR_BASE_URL)/readyz"
    Wait-SMTPReady -Port $SMTPPort

    $stage = 'source browser and SMTP proof'
    Push-Location -LiteralPath (Join-Path $repositoryRoot 'web')
    try { Invoke-Native -Name 'disaster recovery seed browser flow' -Command { npm run test:e2e:dr:seed } } finally { Pop-Location }
    $sourceSnapshot = Get-DatabaseSnapshot -ComposeArguments $composeArguments
    if ($sourceSnapshot.inboxes -ne 1 -or $sourceSnapshot.messages -ne 1 -or $sourceSnapshot.contents -ne 1 -or $sourceSnapshot.parses -ne 1 -or $sourceSnapshot.capabilities -ne 1 -or $sourceSnapshot.create_quotas -ne 1 -or $sourceSnapshot.fk_orphans -ne 0 -or $sourceSnapshot.non_uuid_v7 -ne 0) {
        throw "Source database snapshot is invalid: $($sourceSnapshot | ConvertTo-Json -Compress)"
    }

    $stage = 'offline ingress shutdown'
    Invoke-Native -Name 'stop source ingress' -Command { docker compose @composeArguments stop edge postfix }
    Assert-PostfixQueueEmpty -ComposeArguments $composeArguments -Stopped
    Invoke-Native -Name 'stop source application' -Command { docker compose @composeArguments stop app }
    $runningText = Invoke-NativeText -Name 'offline service inspection' -Command { docker compose @composeArguments ps --status running --services }
    $runningServices = @($runningText -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    if ($runningServices -contains 'app' -or $runningServices -contains 'edge' -or $runningServices -contains 'postfix') {
        throw "Ingress or App remained running during backup: $($runningServices -join ', ')"
    }

    $stage = 'offline backup creation'
    Invoke-Native -Name 'offline backup bundle' -Command {
        docker compose @composeArguments run --rm --no-deps maintenance backup /backups/recovery-bundle
    }
    $stage = 'independent backup verification'
    Invoke-Native -Name 'strict backup verification' -Command {
        docker compose @verifierComposeArguments run --rm --no-deps backup-verifier backup verify /backups/recovery-bundle
    }
    $manifestRaw = Invoke-NativeText -Name 'safe backup manifest read' -Command {
        docker compose @verifierComposeArguments run --rm --no-deps --entrypoint cat backup-verifier /backups/recovery-bundle/manifest.json
    }
    $manifest = $manifestRaw | ConvertFrom-Json
    if ($manifest.format -ne 'mailwisp-backup' -or $manifest.version -ne 1 -or $manifest.postgresql.server_version -ne '18.4' -or $manifest.postgresql.pg_dump_version -ne '18.4' -or $manifest.postgresql.pg_restore_version -ne '18.4' -or $manifest.content.objects -ne 1) {
        throw "Backup manifest does not match the source proof: $manifestRaw"
    }

    $stage = 'source volume destruction'
    $sourcePostgresVolume = "${projectName}_postgres_data"
    $sourceContentVolume = "${projectName}_content_data"
    if (-not (Test-DockerVolumeExists -Name $sourcePostgresVolume) -or -not (Test-DockerVolumeExists -Name $sourceContentVolume)) {
        throw 'Source PostgreSQL or Content volume was not observable before destruction.'
    }
    Assert-ComposeVolumeOwnership -Name $sourcePostgresVolume -Project $projectName -LogicalName 'postgres_data'
    Assert-ComposeVolumeOwnership -Name $sourceContentVolume -Project $projectName -LogicalName 'content_data'
    Assert-NoUnexpectedComposeVolumes -Project $projectName -AllowedLogicalNames @('postgres_data', 'content_data', 'postfix_queue', 'letsencrypt', 'acme_webroot')
    Invoke-Native -Name 'destroy isolated source data volumes' -Command {
        docker compose @composeArguments down --volumes --remove-orphans --timeout 10
    }
    if ((Test-DockerVolumeExists -Name $sourcePostgresVolume) -or (Test-DockerVolumeExists -Name $sourceContentVolume)) {
        throw 'Source PostgreSQL or Content volume survived the isolated destruction step.'
    }
    if (-not (Test-DockerVolumeExists -Name $backupVolume)) { throw 'External backup volume was removed with source data.' }
    $originalVolumesRemoved = $true

    $stage = 'empty PostgreSQL target startup'
    Invoke-Native -Name 'empty PostgreSQL startup' -Command { docker compose @composeArguments up -d --no-deps postgres }
    Wait-PostgresHealthy -ComposeArguments $composeArguments
    $publicObjects = Invoke-NativeText -Name 'empty PostgreSQL inspection' -Command {
        docker compose @composeArguments exec -T postgres psql --quiet --no-align --tuples-only --set ON_ERROR_STOP=1 -U mailwisp -d mailwisp --command "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S','f');"
    }
    if ($publicObjects -ne '0') { throw "Restore PostgreSQL target is not empty: $publicObjects public objects." }

    $stage = 'empty target restore'
    Invoke-Native -Name 'empty target backup restore' -Command {
        docker compose @composeArguments run --rm --no-deps maintenance restore /backups/recovery-bundle
    }
    $restoredSnapshot = Get-DatabaseSnapshot -ComposeArguments $composeArguments
    $sourceCanonical = $sourceSnapshot | ConvertTo-Json -Depth 8 -Compress
    $restoredCanonical = $restoredSnapshot | ConvertTo-Json -Depth 8 -Compress
    if ($sourceCanonical -ne $restoredCanonical) { throw "Restored database snapshot does not match source: source=$sourceCanonical restored=$restoredCanonical" }

    $stage = 'restored stack startup'
    Invoke-Native -Name 'restored Compose stack startup' -Command { docker compose @composeArguments up -d migrate app edge postfix }
    Wait-HTTPSReady -URI "$($env:MAILWISP_DR_BASE_URL)/readyz"
    Wait-SMTPReady -Port $SMTPPort

    $stage = 'restored browser session and write proof'
    Push-Location -LiteralPath (Join-Path $repositoryRoot 'web')
    try { Invoke-Native -Name 'disaster recovery restored browser flow' -Command { npm run test:e2e:dr:verify } } finally { Pop-Location }
    $browserSessionRestored = $true
    $postRestoreDelivery = $true
    Assert-PostfixQueueEmpty -ComposeArguments $composeArguments
    $finalSnapshot = Get-DatabaseSnapshot -ComposeArguments $composeArguments
    if ($finalSnapshot.inboxes -ne 1 -or $finalSnapshot.messages -ne 2 -or $finalSnapshot.contents -ne 2 -or $finalSnapshot.parses -ne 2 -or $finalSnapshot.capabilities -ne 1 -or $finalSnapshot.create_quotas -ne 1 -or $finalSnapshot.fk_orphans -ne 0 -or $finalSnapshot.non_uuid_v7 -ne 0) {
        throw "Post-restore write snapshot is invalid: $($finalSnapshot | ConvertTo-Json -Compress)"
    }
} catch {
    $failure = $_
    Save-DrillDiagnostics -ComposeArguments $composeArguments -VerifierComposeArguments $verifierComposeArguments -Destination $outputRoot
} finally {
    $cleanupFailures = [System.Collections.Generic.List[string]]::new()
    try {
        Invoke-Native -Name 'disaster recovery Compose teardown' -Command {
            docker compose @composeArguments down --volumes --remove-orphans --timeout 10 2>$null | Out-Null
        }
    } catch { $cleanupFailures.Add("Compose teardown: $($_.Exception.Message)") }
    try {
        Invoke-Native -Name 'backup verifier Compose teardown' -Command {
            docker compose @verifierComposeArguments down --remove-orphans --timeout 10 2>$null | Out-Null
        }
    } catch { $cleanupFailures.Add("Backup verifier teardown: $($_.Exception.Message)") }
    try {
        if (Test-DockerVolumeExists -Name $backupVolume) {
            $label = Invoke-NativeText -Name 'external backup volume ownership inspection' -Command {
                docker volume inspect --format '{{index .Labels "mailwisp.drill.project"}}' $backupVolume
            }
            if ($label -ne $projectName -or -not $backupVolume.StartsWith('mailwisp-drill-backup-', [StringComparison]::Ordinal)) {
                throw 'External backup volume ownership proof failed; refusing deletion.'
            }
            Invoke-Native -Name 'external backup volume removal' -Command { docker volume rm $backupVolume | Out-Null }
        }
    } catch { $cleanupFailures.Add("External backup removal: $($_.Exception.Message)") }
    foreach ($name in $managedEnvironment) {
        try { [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name]) } catch {
            $cleanupFailures.Add("Environment restore for ${name}: $($_.Exception.Message)")
        }
    }
    try {
        if ([System.IO.Directory]::Exists($fixtureRoot)) { [System.IO.Directory]::Delete($fixtureRoot, $true) }
    } catch { $cleanupFailures.Add("Fixture removal: $($_.Exception.Message)") }
    try {
        $remaining = foreach ($managedProject in @($projectName, $verifierProjectName)) {
            & docker ps -a --filter "label=com.docker.compose.project=$managedProject" --format '{{.ID}}' 2>$null
            & docker network ls --filter "label=com.docker.compose.project=$managedProject" --format '{{.ID}}' 2>$null
            & docker volume ls --filter "label=com.docker.compose.project=$managedProject" --format '{{.Name}}' 2>$null
        }
        $remaining = @($remaining | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        if ($remaining.Count -ne 0) { $cleanupFailures.Add("Compose resources remained: $($remaining -join ', ')") }
    } catch { $cleanupFailures.Add("Residual resource inspection: $($_.Exception.Message)") }
    if ($cleanupFailures.Count -gt 0) {
        $cleanupMessage = $cleanupFailures -join ' '
        if ($null -eq $failure) {
            $stage = 'cleanup'
            $failure = [System.Exception]::new("Disaster recovery cleanup failed. $cleanupMessage")
        } else {
            $executionStage = $stage
            $stage = "$executionStage; cleanup"
            $failure = [System.Exception]::new("Disaster recovery failed during $executionStage and cleanup also failed. $cleanupMessage", $failure.Exception)
        }
    }
}

$duration = ([DateTimeOffset]::UtcNow - $startedAt).TotalSeconds
if ($null -eq $failure) {
    [ordered]@{
        schema_version = 1
        observed_at = [DateTimeOffset]::UtcNow.ToString('o')
        duration_seconds = $duration
        status = 'passed'
        docker_compose_version = $composeVersion
        postgresql_tools = [ordered]@{ pg_dump = $pgDumpVersion; pg_restore = $pgRestoreVersion }
        backup = [ordered]@{
            format = $manifest.format
            version = $manifest.version
            migration_version = $manifest.postgresql.migration_version
            database_size_bytes = $manifest.database.size_bytes
            database_sha256 = $manifest.database.sha256
            content_archive_size_bytes = $manifest.content.size_bytes
            content_archive_sha256 = $manifest.content.sha256
            content_objects = $manifest.content.objects
            content_uncompressed_bytes = $manifest.content.uncompressed_bytes
        }
        checks = [ordered]@{
            source_browser_smtp_attachment = $true
            source_postfix_queue_empty = $true
            original_postgres_and_content_volumes_removed = $originalVolumesRemoved
            external_backup_survived_source_destruction = $true
            empty_database_observed_before_restore = $true
            database_snapshot_match = $true
            content_catalog_and_digest_match = $true
            browser_session_and_attachment_restored = $browserSessionRestored
            post_restore_delivery_passed = $postRestoreDelivery
            final_fk_orphans = $finalSnapshot.fk_orphans
            final_non_uuid_v7 = $finalSnapshot.non_uuid_v7
            cleanup_resources_remaining = 0
        }
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $outputRoot 'result.json') -Encoding utf8NoBOM
    return
}

[ordered]@{
    schema_version = 1
    observed_at = [DateTimeOffset]::UtcNow.ToString('o')
    duration_seconds = $duration
    status = 'failed'
    stage = $stage
    error = if ($failure -is [System.Exception]) { $failure.Message } else { $failure.Exception.Message }
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $outputRoot 'result.json') -Encoding utf8NoBOM
throw $failure
