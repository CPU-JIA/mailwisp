[CmdletBinding()]
param(
    [string]$OutputDirectory = "artifacts/compose-benchmark",
    [int[]]$Concurrency = @(1, 4, 16, 32),
    [int]$HTTPReadRequests = 10000,
    [int]$HTTPCreateRequests = 500,
    [int]$LMTPRequests = 500,
    [int]$LMTPPayloadBytes = 8192,
    [int]$ParserDrainTimeoutSeconds = 120,
    [int]$HTTPPort = 18080,
    [int]$LMTPPort = 25250,
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-Native {
    param(
        [Parameter(Mandatory)] [string]$Name,
        [Parameter(Mandatory)] [scriptblock]$Command
    )
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE."
    }
}

function Assert-LoopbackPortAvailable {
    param(
        [Parameter(Mandatory)] [string]$Name,
        [Parameter(Mandatory)] [int]$Port
    )

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
    try {
        $listener.Start()
    } catch {
        throw "$Name loopback port $Port is unavailable, already in use, or reserved by the operating system. Choose another port."
    } finally {
        $listener.Stop()
    }
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    throw 'OutputDirectory is required.'
}
if ($Concurrency.Count -eq 0 -or ($Concurrency | Where-Object { $_ -le 0 -or $_ -gt 64 }).Count -ne 0) {
    throw 'Concurrency values must be between 1 and 64.'
}
if ((@($HTTPReadRequests, $HTTPCreateRequests, $LMTPRequests) | Where-Object { $_ -le 0 -or $_ -gt 10000000 }).Count -ne 0) {
    throw 'Request counts must be between 1 and 10000000.'
}
if ($LMTPPayloadBytes -lt 512 -or $LMTPPayloadBytes -gt 26214400) {
    throw 'LMTPPayloadBytes must be between 512 and 26214400.'
}
if ($ParserDrainTimeoutSeconds -le 0 -or $ParserDrainTimeoutSeconds -gt 3600) {
    throw 'ParserDrainTimeoutSeconds must be between 1 and 3600.'
}
if ($HTTPPort -le 0 -or $HTTPPort -gt 65535 -or $LMTPPort -le 0 -or $LMTPPort -gt 65535 -or $HTTPPort -eq $LMTPPort) {
    throw 'HTTPPort and LMTPPort must be distinct values between 1 and 65535.'
}
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$versionLock = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'deploy/compose/versions.lock') | ConvertFrom-StringData
$expectedComposeVersion = $versionLock.MAILWISP_DOCKER_COMPOSE
if ([string]::IsNullOrWhiteSpace($expectedComposeVersion)) {
    throw 'MAILWISP_DOCKER_COMPOSE is missing from deploy/compose/versions.lock.'
}
$artifactsRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot 'artifacts'))
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
$pathComparison = if ($IsWindows) { [System.StringComparison]::OrdinalIgnoreCase } else { [System.StringComparison]::Ordinal }
if (-not $outputRoot.StartsWith(($artifactsRoot + [System.IO.Path]::DirectorySeparatorChar), $pathComparison)) {
    throw 'OutputDirectory must be a subdirectory of the repository artifacts directory.'
}
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null
Assert-LoopbackPortAvailable -Name 'HTTP' -Port $HTTPPort
Assert-LoopbackPortAvailable -Name 'LMTP' -Port $LMTPPort
$temporaryRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$fixtureRoot = [System.IO.Path]::GetFullPath((Join-Path $temporaryRoot ("mailwisp-benchmark-" + [guid]::NewGuid().ToString('N'))))
if (-not $fixtureRoot.StartsWith($temporaryRoot, $pathComparison)) {
    throw 'Benchmark fixture escaped the temporary directory.'
}
[System.IO.Directory]::CreateDirectory($fixtureRoot) | Out-Null

$projectName = "mailwisp-bench-" + [guid]::NewGuid().ToString('N').Substring(0, 12)
$baseCompose = Join-Path $repositoryRoot 'deploy/compose/compose.yaml'
$benchmarkCompose = Join-Path $repositoryRoot 'deploy/compose/benchmark.compose.yaml'
$statsJob = $null

function Save-BenchmarkDiagnostics {
    param(
        [Parameter(Mandatory)] [System.Management.Automation.ErrorRecord]$Failure
    )

    $timestamp = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $diagnosticsRoot = Join-Path $outputRoot "diagnostics-$timestamp"
    [System.IO.Directory]::CreateDirectory($diagnosticsRoot) | Out-Null
    $failureText = @(
        "observed_at=$([DateTimeOffset]::UtcNow.ToString('o'))"
        "project=$projectName"
        "message=$($Failure.Exception.Message)"
        "position=$($Failure.InvocationInfo.PositionMessage)"
    ) -join [Environment]::NewLine
    [System.IO.File]::WriteAllText(
        (Join-Path $diagnosticsRoot 'failure.txt'),
        $failureText,
        [System.Text.UTF8Encoding]::new($false)
    )

    $composeArguments = @('-p', $projectName, '-f', $baseCompose, '-f', $benchmarkCompose)
    try {
        $composePS = & docker compose @composeArguments ps -a --format json 2>&1 | Out-String
        [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'compose-ps.json'), $composePS, [System.Text.UTF8Encoding]::new($false))
    } catch {
        [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'compose-ps-error.txt'), $_.Exception.Message, [System.Text.UTF8Encoding]::new($false))
    }
    try {
        $composeLogs = & docker compose @composeArguments logs --no-color postgres migrate app 2>&1 | Out-String
        [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'compose.log'), $composeLogs, [System.Text.UTF8Encoding]::new($false))
    } catch {
        [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'compose-logs-error.txt'), $_.Exception.Message, [System.Text.UTF8Encoding]::new($false))
    }
    try {
        $containerIDs = @(& docker compose @composeArguments ps -aq 2>$null) | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' }
        if ($containerIDs.Count -ne 0) {
            $inspection = & docker inspect @containerIDs 2>&1 | Out-String
            [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'docker-inspect.json'), $inspection, [System.Text.UTF8Encoding]::new($false))
        }
    } catch {
        [System.IO.File]::WriteAllText((Join-Path $diagnosticsRoot 'docker-inspect-error.txt'), $_.Exception.Message, [System.Text.UTF8Encoding]::new($false))
    }
}

Push-Location -LiteralPath $repositoryRoot
try {
    $mailwispEnvironment = Join-Path $fixtureRoot 'mailwisp.env'
    $postgresPassword = Join-Path $fixtureRoot 'postgres_password.txt'
    $browserSessionKey = Join-Path $fixtureRoot 'browser_session_key.txt'
    $createQuotaKey = Join-Path $fixtureRoot 'create_quota_hmac_key.txt'
    [System.IO.File]::WriteAllText($mailwispEnvironment, @"
MAILWISP_LOG_LEVEL=warn
MAILWISP_PUBLIC_DOMAINS=example.com
MAILWISP_LMTP_HOSTNAME=mx.example.com
MAILWISP_LMTP_MAX_SESSIONS=64
MAILWISP_PARSER_WORKERS=2
MAILWISP_POSTGRES_MIN_CONNECTIONS=1
MAILWISP_POSTGRES_MAX_CONNECTIONS=10
MAILWISP_CONTENT_MIN_FREE_BYTES=1073741824
"@)
    [System.IO.File]::WriteAllText($postgresPassword, "benchmark-postgres-password`n")
    [System.IO.File]::WriteAllText($browserSessionKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`n")
    [System.IO.File]::WriteAllText($createQuotaKey, "UVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVE=`n")

    $env:MAILWISP_ENV_FILE = $mailwispEnvironment
    $env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE = $postgresPassword
    $env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE = $browserSessionKey
    $env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE = $createQuotaKey
    $env:MAILWISP_WEB_DOMAIN = 'mail.example.com'
    $env:MAILWISP_SMTP_HOST = 'mx.example.com'
    $env:MAILWISP_MAIL_DOMAIN = 'example.com'
    $env:MAILWISP_CERT_NAME = 'mail.example.com'
    $env:MAILWISP_BENCH_HTTP_PORT = [string]$HTTPPort
    $env:MAILWISP_BENCH_LMTP_PORT = [string]$LMTPPort

    Invoke-Native -Name 'Docker info' -Command { docker info --format '{{.ServerVersion}}' }
    $composeVersion = (& docker compose version --short).Trim()
    if ($composeVersion -ne $expectedComposeVersion) {
        throw "Docker Compose version $composeVersion does not match locked version $expectedComposeVersion."
    }
    Invoke-Native -Name 'benchmark Compose render' -Command {
        docker compose -p $projectName -f $baseCompose -f $benchmarkCompose config --quiet
    }
    foreach ($pattern in @('environment.json', 'docker-stats.ndjson', 'metrics.prom', 'parser-drain.json', 'http-inbox-*.json', 'lmtp-delivery-*.json')) {
        Get-ChildItem -LiteralPath $outputRoot -File -Filter $pattern -ErrorAction SilentlyContinue | ForEach-Object {
            Remove-Item -LiteralPath $_.FullName -Force
        }
    }
    if (-not $SkipBuild) {
        Invoke-Native -Name 'benchmark App image build' -Command {
            docker compose -p $projectName -f $baseCompose -f $benchmarkCompose build --pull app
        }
    }
    Invoke-Native -Name 'benchmark stack startup' -Command {
        docker compose -p $projectName -f $baseCompose -f $benchmarkCompose up -d postgres migrate app
    }
    $publishedHTTP = (& docker compose -p $projectName -f $baseCompose -f $benchmarkCompose port app 8080).Trim()
    $publishedLMTP = (& docker compose -p $projectName -f $baseCompose -f $benchmarkCompose port app 2525).Trim()
    if ($publishedHTTP -ne "127.0.0.1:$HTTPPort" -or $publishedLMTP -ne "127.0.0.1:$LMTPPort") {
        throw "Benchmark port publication mismatch: HTTP=$publishedHTTP LMTP=$publishedLMTP."
    }

    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri "http://127.0.0.1:$HTTPPort/readyz"
            if ($response.StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {
        }
        Start-Sleep -Seconds 1
    }
    if (-not $ready) {
        throw 'Benchmark App did not become ready within 60 seconds.'
    }

    $appContainer = (& docker compose -p $projectName -f $baseCompose -f $benchmarkCompose ps -q app).Trim()
    $postgresContainer = (& docker compose -p $projectName -f $baseCompose -f $benchmarkCompose ps -q postgres).Trim()
    if ($appContainer -eq '' -or $postgresContainer -eq '') {
        throw 'Benchmark container IDs are unavailable.'
    }

    $dockerInfo = & docker info --format '{{json .}}' | ConvertFrom-Json
    $gitCommit = (& git rev-parse HEAD).Trim()
    $gitDirty = @(& git status --porcelain).Count -ne 0
    $goVersion = (& go version).Trim()
    $appImageID = (& docker inspect --format '{{.Image}}' $appContainer).Trim()
    $appImageReference = (& docker inspect --format '{{.Config.Image}}' $appContainer).Trim()
    $postgresImageID = (& docker inspect --format '{{.Image}}' $postgresContainer).Trim()
    $postgresImageReference = (& docker inspect --format '{{.Config.Image}}' $postgresContainer).Trim()
    $environment = [ordered]@{
        schema_version = 1
        observed_at = [DateTimeOffset]::UtcNow.ToString('o')
        project = $projectName
        source = [ordered]@{
            git_commit = $gitCommit
            git_dirty = $gitDirty
        }
        host = [ordered]@{
            operating_system = $dockerInfo.OperatingSystem
            architecture = $dockerInfo.Architecture
            cpu_count = $dockerInfo.NCPU
            memory_bytes = $dockerInfo.MemTotal
            docker_server_version = $dockerInfo.ServerVersion
        }
        tools = [ordered]@{
            go_version = $goVersion
            docker_compose_version = $composeVersion
            powershell_version = $PSVersionTable.PSVersion.ToString()
        }
        containers = [ordered]@{
            app_image_reference = $appImageReference
            app_image_id = $appImageID
            postgres_image_reference = $postgresImageReference
            postgres_image_id = $postgresImageID
        }
        endpoints = [ordered]@{
            http = $publishedHTTP
            lmtp = $publishedLMTP
        }
        scenarios = [ordered]@{
            concurrency = $Concurrency
            http_read_requests = $HTTPReadRequests
            http_create_requests = $HTTPCreateRequests
            lmtp_requests = $LMTPRequests
            lmtp_payload_bytes = $LMTPPayloadBytes
            parser_drain_timeout_seconds = $ParserDrainTimeoutSeconds
        }
        service_limits = [ordered]@{
            lmtp_max_sessions = 64
            parser_workers = 2
            postgres_min_connections = 1
            postgres_max_connections = 10
        }
    }
    [System.IO.File]::WriteAllText(
        (Join-Path $outputRoot 'environment.json'),
        ($environment | ConvertTo-Json -Depth 8),
        [System.Text.UTF8Encoding]::new($false)
    )

    $statsPath = Join-Path $outputRoot 'docker-stats.ndjson'
    $statsJob = Start-Job -ScriptBlock {
        param($Path, $ContainerList)
        $Containers = $ContainerList.Split(',')
        while ($true) {
            $observedAt = [DateTimeOffset]::UtcNow.ToString('o')
            $lines = & docker stats --no-stream --format '{{json .}}' $Containers 2>$null
            foreach ($line in $lines) {
                if ([string]::IsNullOrWhiteSpace($line)) { continue }
                $sample = $line | ConvertFrom-Json
                $sample | Add-Member -NotePropertyName observed_at -NotePropertyValue $observedAt
                [System.IO.File]::AppendAllText($Path, (($sample | ConvertTo-Json -Compress) + [Environment]::NewLine), [System.Text.UTF8Encoding]::new($false))
            }
            Start-Sleep -Seconds 1
        }
    } -ArgumentList $statsPath, ($appContainer + ',' + $postgresContainer)

    $scenarios = @(
        [ordered]@{ Name = 'http-inbox-read'; Requests = $HTTPReadRequests; Payload = 0 },
        [ordered]@{ Name = 'http-inbox-create'; Requests = $HTTPCreateRequests; Payload = 0 },
        [ordered]@{ Name = 'lmtp-delivery'; Requests = $LMTPRequests; Payload = $LMTPPayloadBytes }
    )
    foreach ($scenario in $scenarios) {
        foreach ($level in $Concurrency) {
            if ($level -gt $scenario.Requests) { continue }
            $arguments = @(
                'run', './cmd/mailwisp-bench',
                '-scenario', $scenario.Name,
                '-base-url', "http://127.0.0.1:$HTTPPort",
                '-lmtp-address', "127.0.0.1:$LMTPPort",
                '-domain', 'example.com',
                '-requests', [string]$scenario.Requests,
                '-concurrency', [string]$level,
                '-payload-bytes', [string]([Math]::Max(512, $scenario.Payload)),
                '-timeout', '10m'
            )
            $fileName = "$($scenario.Name)-c$level.json"
            $stderrPath = Join-Path $fixtureRoot "$($scenario.Name)-c$level.stderr.log"
            $nativeErrorPreference = $PSNativeCommandUseErrorActionPreference
            try {
                $PSNativeCommandUseErrorActionPreference = $false
                $json = & go @arguments 2> $stderrPath | Out-String
                $benchmarkExitCode = $LASTEXITCODE
            } finally {
                $PSNativeCommandUseErrorActionPreference = $nativeErrorPreference
            }
            if (-not [string]::IsNullOrWhiteSpace($json)) {
                $null = $json | ConvertFrom-Json
                [System.IO.File]::WriteAllText((Join-Path $outputRoot $fileName), $json, [System.Text.UTF8Encoding]::new($false))
            }
            if ($benchmarkExitCode -ne 0) {
                $stderr = if (Test-Path -LiteralPath $stderrPath) { (Get-Content -Raw -LiteralPath $stderrPath).Trim() } else { '' }
                throw "Benchmark $($scenario.Name) concurrency $level failed with exit code $benchmarkExitCode. $stderr"
            }
            Remove-Item -LiteralPath $stderrPath -ErrorAction SilentlyContinue
        }
    }

    $parserQuery = @"
SELECT json_build_object(
  'pending', count(*) FILTER (WHERE parse_status = 'pending'),
  'processing', count(*) FILTER (WHERE parse_status = 'processing'),
  'parsed', count(*) FILTER (WHERE parse_status = 'parsed'),
  'failed', count(*) FILTER (WHERE parse_status = 'failed')
)::text
FROM mail_contents;
"@
    $parserDrainStarted = [DateTimeOffset]::UtcNow
    $parserState = $null
    while (([DateTimeOffset]::UtcNow - $parserDrainStarted).TotalSeconds -lt $ParserDrainTimeoutSeconds) {
        $parserStateRaw = & docker compose -p $projectName -f $baseCompose -f $benchmarkCompose exec -T postgres psql -X -q -U mailwisp -d mailwisp -Atc $parserQuery | Out-String
        if ($LASTEXITCODE -ne 0) {
            throw 'Parser queue state query failed.'
        }
        $parserState = $parserStateRaw.Trim() | ConvertFrom-Json
        if (($parserState.pending + $parserState.processing) -eq 0) {
            break
        }
        Start-Sleep -Milliseconds 250
    }
    $parserDrain = [ordered]@{
        schema_version = 1
        started_at = $parserDrainStarted.ToString('o')
        duration_seconds = ([DateTimeOffset]::UtcNow - $parserDrainStarted).TotalSeconds
        timeout_seconds = $ParserDrainTimeoutSeconds
        state = $parserState
    }
    [System.IO.File]::WriteAllText(
        (Join-Path $outputRoot 'parser-drain.json'),
        ($parserDrain | ConvertTo-Json -Depth 5),
        [System.Text.UTF8Encoding]::new($false)
    )
    if ($null -eq $parserState -or ($parserState.pending + $parserState.processing) -ne 0) {
        throw "Parser queue did not drain within $ParserDrainTimeoutSeconds seconds."
    }
    if ($parserState.failed -ne 0) {
        throw "Parser queue completed with $($parserState.failed) failed content records."
    }

    $metrics = & docker compose -p $projectName -f $baseCompose -f $benchmarkCompose exec -T app wget -qO- http://127.0.0.1:8080/metrics | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw 'Final benchmark Metrics collection failed.'
    }
    [System.IO.File]::WriteAllText((Join-Path $outputRoot 'metrics.prom'), $metrics, [System.Text.UTF8Encoding]::new($false))
} catch {
    Save-BenchmarkDiagnostics -Failure $_
    throw
} finally {
    if ($statsJob) {
        Stop-Job -Job $statsJob -ErrorAction SilentlyContinue
        Receive-Job -Job $statsJob -ErrorAction SilentlyContinue | Out-Null
        Remove-Job -Job $statsJob -Force -ErrorAction SilentlyContinue
    }
    try {
        & docker compose -p $projectName -f $baseCompose -f $benchmarkCompose down --volumes --remove-orphans 2>$null | Out-Null
    } catch {
    }
    Remove-Item Env:MAILWISP_ENV_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_WEB_DOMAIN -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_SMTP_HOST -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_MAIL_DOMAIN -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_CERT_NAME -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_BENCH_HTTP_PORT -ErrorAction SilentlyContinue
    Remove-Item Env:MAILWISP_BENCH_LMTP_PORT -ErrorAction SilentlyContinue
    Pop-Location
    if ([System.IO.Directory]::Exists($fixtureRoot) -and $fixtureRoot.StartsWith($temporaryRoot, $pathComparison)) {
        [System.IO.Directory]::Delete($fixtureRoot, $true)
    }
}
