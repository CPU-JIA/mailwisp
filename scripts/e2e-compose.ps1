param(
    [ValidateRange(0, 65535)]
    [int]$HTTPPort = 0,

    [ValidateRange(0, 65535)]
    [int]$HTTPSPort = 0,

    [ValidateRange(0, 65535)]
    [int]$SMTPPort = 0,

    [string]$OutputDirectory = 'artifacts/production-e2e',

    [string[]]$ComposeFiles,

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z_.-]{0,127}$')]
    [string]$ImageTag,

    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-Native {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [scriptblock]$Command
    )

    $previousNativeErrorPreference = $PSNativeCommandUseErrorActionPreference
    try {
        $PSNativeCommandUseErrorActionPreference = $false
        & $Command
        $exitCode = $LASTEXITCODE
    } finally {
        $PSNativeCommandUseErrorActionPreference = $previousNativeErrorPreference
    }
    if ($exitCode -ne 0) {
        throw "$Name failed with exit code $exitCode."
    }
}

function Invoke-NativeWithRetry {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [scriptblock]$Command,

        [int]$Attempts = 5
    )

    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            Invoke-Native -Name $Name -Command $Command
            return
        } catch {
            if ($attempt -eq $Attempts) { throw }
            Start-Sleep -Seconds ([math]::Pow(2, $attempt - 1))
        }
    }
}

function Get-LoopbackPort {
    param(
        [int]$Requested,
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [System.Collections.Generic.HashSet[int]]$Used
    )

    if ($Requested -ne 0) {
        if (-not $Used.Add($Requested)) { throw "TCP port $Requested was requested more than once." }
        Assert-LoopbackPortAvailable -Port $Requested
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

function Assert-LoopbackPortAvailable {
    param([Parameter(Mandatory)][int]$Port)

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
    try {
        $listener.Start()
    } catch {
        throw "Loopback TCP port $Port is unavailable: $($_.Exception.Message)"
    } finally {
        $listener.Stop()
    }
}

function Protect-E2EFixture {
    param([Parameter(Mandatory)][string]$Path)

    if ($IsWindows) {
        $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        Invoke-Native -Name 'production E2E fixture ACL' -Command {
            icacls.exe $Path /inheritance:r /grant:r "*$sid`:(OI)(CI)F" | Out-Null
        }
        return
    }
    Invoke-Native -Name 'production E2E fixture permissions' -Command { chmod 0700 $Path }
}

function New-E2ECertificate {
    param([Parameter(Mandatory)][string]$CertificateRoot)

    $liveRoot = Join-Path $CertificateRoot 'live/e2e'
    [System.IO.Directory]::CreateDirectory($liveRoot) | Out-Null
    $rsa = [System.Security.Cryptography.RSA]::Create(2048)
    $certificate = $null
    try {
        $request = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
            'CN=mailwisp.test',
            $rsa,
            [System.Security.Cryptography.HashAlgorithmName]::SHA256,
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
            [System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]::KeyEncipherment,
            $true
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
    param(
        [Parameter(Mandatory)][string]$URI,
        [int]$TimeoutSeconds = 120
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        try {
            $response = Invoke-WebRequest -Uri $URI -SkipCertificateCheck -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    throw "HTTPS readiness did not succeed within $TimeoutSeconds seconds: $URI"
}

function Assert-HTTPSSecurityHeaders {
    param([Parameter(Mandatory)][string]$URI)

    $response = Invoke-WebRequest -Uri $URI -SkipCertificateCheck -TimeoutSec 5
    if ($response.StatusCode -ne 200) { throw "HTTPS root returned status $($response.StatusCode)." }
    $hsts = [string]$response.Headers['Strict-Transport-Security']
    $csp = [string]$response.Headers['Content-Security-Policy']
    if ($hsts -ne 'max-age=31536000') { throw "HTTPS root returned an invalid Strict-Transport-Security header: $hsts" }
    if (-not $csp.Contains("default-src 'self'", [StringComparison]::Ordinal)) { throw 'HTTPS root did not return the required Content-Security-Policy.' }
    if ([string]$response.Headers['X-Content-Type-Options'] -ne 'nosniff') { throw 'HTTPS root did not return X-Content-Type-Options: nosniff.' }
}

function Assert-HTTPRedirect {
    param([Parameter(Mandatory)][int]$Port)

    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.AllowAutoRedirect = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(5)
    $response = $null
    try {
        $response = $client.GetAsync("http://127.0.0.1:$Port/").GetAwaiter().GetResult()
        $status = [int]$response.StatusCode
        $location = [string]$response.Headers.Location
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        $client.Dispose()
        $handler.Dispose()
    }
    if ($status -ne 308 -or -not $location.StartsWith('https://', [StringComparison]::OrdinalIgnoreCase)) {
        throw "HTTP entrypoint did not return a permanent HTTPS redirect: status=$status location=$location."
    }
}

function Wait-SMTPReady {
    param(
        [Parameter(Mandatory)][int]$Port,
        [int]$TimeoutSeconds = 60
    )

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
        } catch {
            Start-Sleep -Milliseconds 500
        } finally {
            if ($null -ne $reader) { $reader.Dispose() }
            $client.Dispose()
        }
    } while ([DateTimeOffset]::UtcNow -lt $deadline)
    throw "SMTP readiness did not succeed within $TimeoutSeconds seconds on 127.0.0.1:$Port."
}

function Save-E2EDiagnostics {
    param(
        [Parameter(Mandatory)][string[]]$ComposeArguments,
        [Parameter(Mandatory)][string]$Destination
    )

    $utf8 = [System.Text.UTF8Encoding]::new($false)
    foreach ($capture in @(
        @{ Name = 'compose-ps.json'; Command = { & docker compose @ComposeArguments ps -a --format json 2>&1 | Out-String } },
        @{ Name = 'compose.log'; Command = { & docker compose @ComposeArguments logs --no-color postgres db-provision migrate app edge postfix 2>&1 | Out-String } },
        @{ Name = 'nginx.txt'; Command = { & docker compose @ComposeArguments exec -T edge nginx -T 2>&1 | Out-String } },
        @{ Name = 'postfix.txt'; Command = { & docker compose @ComposeArguments exec -T postfix postconf -n 2>&1 | Out-String } }
    )) {
        try {
            [System.IO.File]::WriteAllText((Join-Path $Destination $capture.Name), (& $capture.Command), $utf8)
        } catch {
            [System.IO.File]::WriteAllText((Join-Path $Destination "$($capture.Name).error.txt"), $_.Exception.Message, $utf8)
        }
    }
}

function Assert-NoReparsePoint {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][string]$Child
    )

    if ([System.IO.Directory]::Exists($Root) -or [System.IO.File]::Exists($Root)) {
        $rootAttributes = [System.IO.File]::GetAttributes($Root)
        if (($rootAttributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Production E2E artifact root is a symbolic link or junction: $Root"
        }
    }

    $current = $Root
    $relative = [System.IO.Path]::GetRelativePath($Root, $Child)
    foreach ($segment in $relative.Split([System.IO.Path]::DirectorySeparatorChar, [StringSplitOptions]::RemoveEmptyEntries)) {
        $current = Join-Path $current $segment
        if (-not ([System.IO.Directory]::Exists($current) -or [System.IO.File]::Exists($current))) { continue }
        $attributes = [System.IO.File]::GetAttributes($current)
        if (($attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Production E2E output path contains a symbolic link or junction: $current"
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
    throw 'Production E2E output must be a subdirectory of the repository artifacts directory.'
}
Assert-NoReparsePoint -Root $artifactRoot -Child $outputRoot
if ([System.IO.Directory]::Exists($outputRoot)) { [System.IO.Directory]::Delete($outputRoot, $true) }
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null

$versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
$expectedComposeVersion = $versionLock.MAILWISP_DOCKER_COMPOSE
if ([string]::IsNullOrWhiteSpace($expectedComposeVersion)) { throw 'MAILWISP_DOCKER_COMPOSE is missing from versions.lock.' }

$usedPorts = [System.Collections.Generic.HashSet[int]]::new()
$HTTPPort = Get-LoopbackPort -Requested $HTTPPort -Used $usedPorts
$HTTPSPort = Get-LoopbackPort -Requested $HTTPSPort -Used $usedPorts
$SMTPPort = Get-LoopbackPort -Requested $SMTPPort -Used $usedPorts
$temporaryRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$fixtureRoot = [System.IO.Path]::GetFullPath((Join-Path $temporaryRoot ("mailwisp-production-e2e-" + [guid]::NewGuid().ToString('N'))))
$temporaryPrefix = $temporaryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if (-not $fixtureRoot.StartsWith($temporaryPrefix, $pathComparison)) {
    throw 'Production E2E fixture path escaped the temporary directory.'
}
$projectName = "mailwisp-e2e-" + [guid]::NewGuid().ToString('N').Substring(0, 12)
$composeFilesResolved = if ($null -eq $ComposeFiles -or $ComposeFiles.Count -eq 0) {
    @((Join-Path $repositoryRoot 'deploy/compose/compose.yaml'))
} else {
    @($ComposeFiles | ForEach-Object { [System.IO.Path]::GetFullPath($_) })
}
foreach ($candidate in $composeFilesResolved) {
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { throw "Production E2E Compose file is missing: $candidate" }
    $repositoryPrefix = $repositoryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $candidate.StartsWith($repositoryPrefix, $pathComparison)) { throw "Production E2E Compose file escaped the repository: $candidate" }
    Assert-NoReparsePoint -Root $repositoryRoot -Child $candidate
}
$e2eComposeFile = Join-Path $repositoryRoot 'deploy/compose/production-e2e.compose.yaml'
$composeArguments = @('-p', $projectName)
foreach ($candidate in $composeFilesResolved) { $composeArguments += @('-f', $candidate) }
$composeArguments += @('-f', $e2eComposeFile)
$managedEnvironment = @(
    'MAILWISP_ENV_FILE', 'MAILWISP_POSTGRES_OWNER_PASSWORD_FILE_SOURCE', 'MAILWISP_POSTGRES_APP_PASSWORD_FILE_SOURCE', 'MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE',
    'MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE', 'MAILWISP_WEB_DOMAIN', 'MAILWISP_SMTP_HOST',
    'MAILWISP_PUBLIC_DOMAINS', 'MAILWISP_LMTP_MAX_MESSAGE_BYTES', 'MAILWISP_CERT_NAME', 'MAILWISP_E2E_CERT_ROOT', 'MAILWISP_E2E_HTTP_PORT',
    'MAILWISP_E2E_HTTPS_PORT', 'MAILWISP_E2E_SMTP_PORT', 'MAILWISP_E2E_BASE_URL', 'MAILWISP_IMAGE_TAG'
)
$originalEnvironment = @{}
foreach ($name in $managedEnvironment) { $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name) }

$failure = $null
$stage = 'fixture setup'
try {
    [System.IO.Directory]::CreateDirectory($fixtureRoot) | Out-Null
    Protect-E2EFixture -Path $fixtureRoot
    $certificateRoot = Join-Path $fixtureRoot 'letsencrypt'
    New-E2ECertificate -CertificateRoot $certificateRoot
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $mailwispEnvironment = Join-Path $fixtureRoot 'mailwisp.env'
    $postgresOwnerPassword = Join-Path $fixtureRoot 'postgres_owner_password.txt'
    $postgresAppPassword = Join-Path $fixtureRoot 'postgres_app_password.txt'
    $browserSessionKey = Join-Path $fixtureRoot 'browser_session_key.txt'
    $createQuotaKey = Join-Path $fixtureRoot 'create_quota_hmac_key.txt'
    [System.IO.File]::WriteAllText($mailwispEnvironment, "MAILWISP_LOG_LEVEL=info`nMAILWISP_TRUSTED_PROXY_CIDRS=172.16.0.0/12`nMAILWISP_PUBLIC_DOMAINS=mailwisp.test`nMAILWISP_LMTP_HOSTNAME=mx.mailwisp.test`n", $utf8)
    [System.IO.File]::WriteAllText($postgresOwnerPassword, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)
    [System.IO.File]::WriteAllText($postgresAppPassword, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)
    [System.IO.File]::WriteAllText($browserSessionKey, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)
    [System.IO.File]::WriteAllText($createQuotaKey, ([Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + "`n"), $utf8)

    $env:MAILWISP_ENV_FILE = $mailwispEnvironment
    if (-not $IsWindows) { Invoke-Native -Name 'chmod 0444 E2E secrets' -Command { chmod 0444 $postgresOwnerPassword $postgresAppPassword $browserSessionKey $createQuotaKey } }
    $env:MAILWISP_POSTGRES_OWNER_PASSWORD_FILE_SOURCE = $postgresOwnerPassword
    $env:MAILWISP_POSTGRES_APP_PASSWORD_FILE_SOURCE = $postgresAppPassword
    $env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE = $browserSessionKey
    $env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE = $createQuotaKey
    $env:MAILWISP_WEB_DOMAIN = 'mailwisp.test'
    $env:MAILWISP_SMTP_HOST = 'mx.mailwisp.test'
    $env:MAILWISP_PUBLIC_DOMAINS = 'mailwisp.test'
    $env:MAILWISP_LMTP_MAX_MESSAGE_BYTES = '26214400'
    $env:MAILWISP_CERT_NAME = 'e2e'
    $env:MAILWISP_E2E_CERT_ROOT = $certificateRoot
    $env:MAILWISP_E2E_HTTP_PORT = [string]$HTTPPort
    $env:MAILWISP_E2E_HTTPS_PORT = [string]$HTTPSPort
    $env:MAILWISP_E2E_SMTP_PORT = [string]$SMTPPort
    $env:MAILWISP_E2E_BASE_URL = "https://127.0.0.1:$HTTPSPort"
    if (-not [string]::IsNullOrWhiteSpace($ImageTag)) { $env:MAILWISP_IMAGE_TAG = $ImageTag }

    $stage = 'Docker availability'
    Invoke-Native -Name 'docker info' -Command { docker info --format '{{.ServerVersion}}' }
    $stage = 'Docker Compose version validation'
    $composeVersion = (& docker compose version --short).Trim()
    if ($LASTEXITCODE -ne 0 -or $composeVersion -ne $expectedComposeVersion) {
        throw "Docker Compose $composeVersion does not match locked version $expectedComposeVersion."
    }
    $stage = 'Compose configuration render'
    Invoke-Native -Name 'production E2E Compose render' -Command { docker compose @composeArguments config --quiet }
    if (-not $SkipBuild) {
        $stage = 'production image build'
        Invoke-Native -Name 'production E2E images' -Command { docker compose @composeArguments build --pull app edge postfix }
    }
    $stage = 'pinned PostgreSQL image pull'
    Invoke-NativeWithRetry -Name 'production E2E PostgreSQL pull' -Command { docker compose @composeArguments pull postgres }
    $stage = 'Compose stack startup'
    Invoke-Native -Name 'production E2E stack startup' -Command { docker compose @composeArguments up -d postgres migrate app edge postfix }
    $stage = 'HTTPS readiness'
    Wait-HTTPSReady -URI "$($env:MAILWISP_E2E_BASE_URL)/readyz"
    $stage = 'HTTPS security header validation'
    Assert-HTTPSSecurityHeaders -URI "$($env:MAILWISP_E2E_BASE_URL)/"
    $stage = 'HTTP to HTTPS redirect validation'
    Assert-HTTPRedirect -Port $HTTPPort
    $stage = 'SMTP readiness'
    Wait-SMTPReady -Port $SMTPPort
    $stage = 'Nginx configuration validation'
    Invoke-Native -Name 'production E2E Nginx configuration' -Command { docker compose @composeArguments exec -T edge nginx -t }
    $stage = 'Postfix configuration validation'
    Invoke-Native -Name 'production E2E Postfix configuration' -Command { docker compose @composeArguments exec -T postfix postfix check }
    $postfixVerifyMap = (& docker compose @composeArguments exec -T postfix postconf -h address_verify_map).Trim()
    $postfixVerifyTransport = (& docker compose @composeArguments exec -T postfix postconf -h address_verify_relay_transport).Trim()
    $postfixPositiveExpiry = (& docker compose @composeArguments exec -T postfix postconf -h address_verify_positive_expire_time).Trim()
    $postfixNegativeExpiry = (& docker compose @composeArguments exec -T postfix postconf -h address_verify_negative_expire_time).Trim()
    if ($LASTEXITCODE -ne 0 -or $postfixVerifyMap -ne 'lmdb:$data_directory/verify_cache' -or
        $postfixVerifyTransport -ne 'lmtp:inet:app:2525' -or $postfixPositiveExpiry -ne '2s' -or $postfixNegativeExpiry -ne '2s') {
        throw "Postfix address verification contract mismatch: map=$postfixVerifyMap transport=$postfixVerifyTransport positive=$postfixPositiveExpiry negative=$postfixNegativeExpiry"
    }
    $stage = 'PostgreSQL runtime role validation'
    $runtimeRole = (& docker compose @composeArguments exec -T postgres psql -X -q -U mailwisp_owner -d mailwisp -Atc "SELECT concat_ws('|', rolsuper, rolcreatedb, rolcreaterole, rolinherit, rolreplication, rolbypassrls, has_database_privilege('mailwisp_app', 'mailwisp', 'CREATE'), has_database_privilege('mailwisp_app', 'mailwisp', 'TEMPORARY'), has_schema_privilege('mailwisp_app', 'public', 'CREATE'), has_schema_privilege('mailwisp_app', 'public', 'USAGE'), has_table_privilege('mailwisp_app', 'inboxes', 'SELECT,INSERT,UPDATE,DELETE')) FROM pg_roles WHERE rolname = 'mailwisp_app';").Trim()
    if ($LASTEXITCODE -ne 0 -or $runtimeRole -ne 'f|f|f|f|f|f|f|f|f|t|t') {
        throw "PostgreSQL runtime role is overprivileged or missing: $runtimeRole"
    }

    $stage = 'published endpoint validation'
    $publishedHTTP = (& docker compose @composeArguments port edge 80).Trim()
    $publishedHTTPS = (& docker compose @composeArguments port edge 443).Trim()
    $publishedSMTP = (& docker compose @composeArguments port postfix 25).Trim()
    if ($publishedHTTP -ne "127.0.0.1:$HTTPPort" -or $publishedHTTPS -ne "127.0.0.1:$HTTPSPort" -or $publishedSMTP -ne "127.0.0.1:$SMTPPort") {
        throw "Compose published unexpected endpoints: HTTP=$publishedHTTP HTTPS=$publishedHTTPS SMTP=$publishedSMTP."
    }

    $stage = 'production browser flow'
    Push-Location -LiteralPath (Join-Path $repositoryRoot 'web')
    try {
        Invoke-Native -Name 'production Compose browser E2E' -Command { npm run test:e2e:production }
    } finally {
        Pop-Location
    }

} catch {
    $failure = $_
    Save-E2EDiagnostics -ComposeArguments $composeArguments -Destination $outputRoot
} finally {
    $cleanupFailures = [System.Collections.Generic.List[string]]::new()
    try {
        Invoke-Native -Name 'production E2E Compose teardown' -Command {
            docker compose @composeArguments down --volumes --remove-orphans --timeout 10 2>$null | Out-Null
        }
    } catch {
        $cleanupFailures.Add("Compose teardown: $($_.Exception.Message)")
    }
    foreach ($name in $managedEnvironment) {
        try {
            [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name])
        } catch {
            $cleanupFailures.Add("Environment restore for ${name}: $($_.Exception.Message)")
        }
    }
    try {
        if ([System.IO.Directory]::Exists($fixtureRoot)) { [System.IO.Directory]::Delete($fixtureRoot, $true) }
    } catch {
        $cleanupFailures.Add("Fixture removal: $($_.Exception.Message)")
    }
    if ($cleanupFailures.Count -gt 0) {
        $cleanupMessage = $cleanupFailures -join ' '
        if ($null -eq $failure) {
            $stage = 'cleanup'
            $failure = [System.Exception]::new("Production E2E cleanup failed. $cleanupMessage")
        } else {
            $executionStage = $stage
            $stage = "$executionStage; cleanup"
            $failure = [System.Exception]::new(
                "Production E2E failed during $executionStage and cleanup also failed. $cleanupMessage",
                $failure.Exception
            )
        }
    }
}

if ($null -eq $failure) {
    [ordered]@{
        schema_version = 1
        observed_at = [DateTimeOffset]::UtcNow.ToString('o')
        status = 'passed'
        project = $projectName
        docker_compose_version = $composeVersion
        endpoints = [ordered]@{ http = "127.0.0.1:$HTTPPort"; https = "127.0.0.1:$HTTPSPort"; smtp = "127.0.0.1:$SMTPPort" }
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $outputRoot 'result.json') -Encoding utf8NoBOM
    return
}

[ordered]@{
    schema_version = 1
    observed_at = [DateTimeOffset]::UtcNow.ToString('o')
    status = 'failed'
    project = $projectName
    stage = $stage
    error = if ($failure -is [System.Exception]) { $failure.Message } else { $failure.Exception.Message }
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $outputRoot 'result.json') -Encoding utf8NoBOM
throw $failure
