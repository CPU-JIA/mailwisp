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

function Assert-GoToolVersion {
    param(
        [Parameter(Mandatory)]
        [string]$CommandName,

        [Parameter(Mandatory)]
        [string]$ModulePath,

        [Parameter(Mandatory)]
        [string]$Version
    )

    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if (-not $command) {
        throw "${CommandName}未安装，验证不得跳过。"
    }
    $metadata = & go version -m $command.Source | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "无法读取${CommandName}构建元数据。"
    }
    $expected = "mod`t$ModulePath`t$Version"
    if (-not $metadata.Contains($expected)) {
        throw "${CommandName}版本不符合要求，必须为$ModulePath@$Version。"
    }
}

function Assert-PostgreSQLToolVersion {
    param(
        [Parameter(Mandatory)]
        [string]$CommandName,

        [Parameter(Mandatory)]
        [string]$Version
    )

    if (-not (Get-Command $CommandName -ErrorAction SilentlyContinue)) {
        throw "${CommandName}未安装，PostgreSQL备份恢复验证不得跳过。"
    }
    $reported = & $CommandName --version | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "无法读取${CommandName}版本。"
    }
    if ($reported -notmatch "^$([regex]::Escape($CommandName)) \(PostgreSQL\) $([regex]::Escape($Version))(?:\s|$)") {
        throw "${CommandName}版本不符合要求，必须为PostgreSQL $Version。实际：$($reported.Trim())"
    }
}

function Assert-MailWispTokenDetection {
    param(
        [Parameter(Mandatory)]
        [string]$RepositoryRoot
    )

    $temporaryRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    $fixtureRoot = [System.IO.Path]::GetFullPath((Join-Path $temporaryRoot ("mailwisp-gitleaks-" + [guid]::NewGuid().ToString('N'))))
    if (-not $fixtureRoot.StartsWith($temporaryRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'Gitleaks fixture path escaped the temporary directory.'
    }
    [System.IO.Directory]::CreateDirectory($fixtureRoot) | Out-Null
    try {
        $kid = 'a' * 24
        $secret = 'A' * 43
        foreach ($tokenType in @('pat', 'cap', 'ses', 'whsec')) {
            $fixture = 'credential=' + 'wisp_' + $tokenType + '_v1_' + $kid + '_' + $secret
            [System.IO.File]::WriteAllText((Join-Path $fixtureRoot 'credential.txt'), $fixture)

            $previousNativeErrorPreference = $PSNativeCommandUseErrorActionPreference
            try {
                $PSNativeCommandUseErrorActionPreference = $false
                & gitleaks dir $fixtureRoot `
                    --config (Join-Path $RepositoryRoot '.gitleaks.toml') `
                    --no-banner `
                    --redact `
                    --exit-code 23 *> $null
                $scannerExitCode = $LASTEXITCODE
            } finally {
                $PSNativeCommandUseErrorActionPreference = $previousNativeErrorPreference
            }
            if ($scannerExitCode -ne 23) {
                throw "MailWisp $tokenType token scanner probe returned exit code $scannerExitCode instead of 23."
            }
        }
    } finally {
        if ([System.IO.Directory]::Exists($fixtureRoot)) {
            [System.IO.Directory]::Delete($fixtureRoot, $true)
        }
    }
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$env:GOTOOLCHAIN = 'go1.26.5'
$containerGoProxy = $env:GOPROXY
if ([string]::IsNullOrWhiteSpace($containerGoProxy)) {
    $containerGoProxy = (& go env GOPROXY | Select-Object -First 1)
}
if ([string]::IsNullOrWhiteSpace($containerGoProxy)) {
    $containerGoProxy = 'https://proxy.golang.org,direct'
}
$containerGoProxy = $containerGoProxy.Trim()
$env:GOPROXY = $containerGoProxy

Push-Location -LiteralPath $repositoryRoot
try {
    Invoke-Native -Name 'git diff --check' -Command { git diff --check }
    Invoke-Native -Name 'go fmt' -Command { go fmt ./... }
    Invoke-Native -Name 'go test' -Command { go test ./... }
    Invoke-Native -Name 'go test -race' -Command { go test -race ./... }
    Invoke-Native -Name 'go vet' -Command { go vet ./... }
    Invoke-Native -Name 'bounded MIME parser fuzzing' -Command {
        go test ./internal/mail -run '^$' -fuzz '^FuzzParserNeverPanics$' -fuzztime 10s
    }
    Invoke-Native -Name 'canonical token parser fuzzing' -Command {
        go test ./internal/auth -run '^$' -fuzz '^FuzzParseTokenNeverPanics$' -fuzztime 10s
    }

    Assert-PostgreSQLToolVersion -CommandName 'pg_dump' -Version '18.4'
    Assert-PostgreSQLToolVersion -CommandName 'pg_restore' -Version '18.4'

    if (Get-Command docker -ErrorAction SilentlyContinue) {
        Invoke-Native -Name 'docker info' -Command { docker info --format '{{.ServerVersion}}' }
        $linuxGoImage = 'registry-1.docker.io/library/golang@sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c'
        Invoke-Native -Name 'linux go test and race test' -Command {
            docker run --rm --platform linux/amd64 `
                --mount "type=bind,source=$repositoryRoot,target=/src,readonly" `
                -w /src `
                -e "GOPROXY=$containerGoProxy" `
                $linuxGoImage `
                sh -c 'go test ./... && go test -race ./...'
        }

        $postfixImage = 'mailwisp/postfix-integration:3.11.5-r0'
        $postfixContext = Join-Path $repositoryRoot 'deploy/postfix-test'
        if ($env:GITHUB_ACTIONS -eq 'true') {
            Invoke-Native -Name 'docker buildx version' -Command { docker buildx version }
            Invoke-Native -Name 'pinned Postfix integration image with GitHub Actions cache' -Command {
                docker buildx build `
                    --platform linux/amd64 `
                    --pull `
                    --load `
                    --tag $postfixImage `
                    --cache-from 'type=gha,scope=postfix-integration' `
                    --cache-to 'type=gha,mode=max,scope=postfix-integration' `
                    $postfixContext
            }
        } else {
            Invoke-Native -Name 'pinned Postfix integration image' -Command {
                docker build `
                    --platform linux/amd64 `
                    --pull `
                    --tag $postfixImage `
                    $postfixContext
            }
        }

        $env:MAILWISP_POSTFIX_EVIDENCE_DIR = Join-Path $repositoryRoot 'artifacts/postfix-integration'
        if ($IsWindows) {
            Invoke-Native -Name 'Postfix LMTP integration and race tests in pinned Linux container' -Command {
                docker run --rm --platform linux/amd64 `
                    --network host `
                    --mount "type=bind,source=$repositoryRoot,target=/src" `
                    --mount 'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock' `
                    -w /src `
                    -e "GOPROXY=$containerGoProxy" `
                    -e MAILWISP_POSTFIX_EVIDENCE_DIR=/src/artifacts/postfix-integration `
                    $linuxGoImage `
                    sh -c 'go test -tags=integration ./internal/postfix -count=1 && go test -race -tags=integration ./internal/postfix -count=1'
            }
        } else {
            Invoke-Native -Name 'Postfix LMTP integration tests' -Command { go test -tags=integration ./internal/postfix -count=1 }
            Invoke-Native -Name 'Postfix LMTP integration race tests' -Command { go test -race -tags=integration ./internal/postfix -count=1 }
        }
        Invoke-Native -Name 'postgres integration tests' -Command { go test -tags=integration ./internal/postgres -count=1 }
        Invoke-Native -Name 'postgres integration race tests' -Command { go test -race -tags=integration ./internal/postgres -count=1 }

        $composeFixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("mailwisp-compose-" + [guid]::NewGuid().ToString('N'))
        [System.IO.Directory]::CreateDirectory($composeFixtureRoot) | Out-Null
        try {
            $composeEnvironment = Join-Path $composeFixtureRoot 'mailwisp.env'
            $composePassword = Join-Path $composeFixtureRoot 'postgres_password.txt'
            $composeBrowserSessionKey = Join-Path $composeFixtureRoot 'browser_session_key.txt'
            $composeCreateQuotaKey = Join-Path $composeFixtureRoot 'create_quota_hmac_key.txt'
            [System.IO.File]::WriteAllText($composeEnvironment, "MAILWISP_PUBLIC_DOMAINS=example.com`nMAILWISP_LMTP_HOSTNAME=mx.example.com`n")
            [System.IO.File]::WriteAllText($composePassword, "compose-verification-password`n")
            [System.IO.File]::WriteAllText($composeBrowserSessionKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`n")
            [System.IO.File]::WriteAllText($composeCreateQuotaKey, "UVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVFRUVE=`n")
            $env:MAILWISP_ENV_FILE = $composeEnvironment
            $env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE = $composePassword
            $env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE = $composeBrowserSessionKey
            $env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE = $composeCreateQuotaKey
            $env:MAILWISP_WEB_DOMAIN = 'mail.example.com'
            $env:MAILWISP_SMTP_HOST = 'mx.example.com'
            $env:MAILWISP_MAIL_DOMAIN = 'example.com'
            $env:MAILWISP_CERT_NAME = 'mail.example.com'
            $composeFile = Join-Path $repositoryRoot 'deploy/compose/compose.yaml'
            Invoke-Native -Name 'Docker Compose render' -Command { docker compose --profile tools -f $composeFile config --quiet }
            Invoke-Native -Name 'Docker Compose production images' -Command { docker compose -f $composeFile build --pull app edge postfix }
        } finally {
            Remove-Item Env:MAILWISP_ENV_FILE -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_POSTGRES_PASSWORD_FILE_SOURCE -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_BROWSER_SESSION_KEY_FILE_SOURCE -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_CREATE_QUOTA_HMAC_KEY_FILE_SOURCE -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_WEB_DOMAIN -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_SMTP_HOST -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_MAIL_DOMAIN -ErrorAction SilentlyContinue
            Remove-Item Env:MAILWISP_CERT_NAME -ErrorAction SilentlyContinue
            if ([System.IO.Directory]::Exists($composeFixtureRoot)) {
                [System.IO.Directory]::Delete($composeFixtureRoot, $true)
            }
        }
    } else {
        throw 'Docker未安装，PostgreSQL Integration验证不得跳过。'
    }

    Assert-GoToolVersion -CommandName 'govulncheck' -ModulePath 'golang.org/x/vuln' -Version 'v1.6.0'
    Invoke-Native -Name 'govulncheck including integration tests' -Command { govulncheck -test -tags=integration ./... }

    Assert-GoToolVersion -CommandName 'gosec' -ModulePath 'github.com/securego/gosec/v2' -Version 'v2.27.1'
    Invoke-Native -Name 'gosec' -Command { gosec -tags=integration ./... }
    if ($IsWindows) {
        $previousGoOS = $env:GOOS
        try {
            $env:GOOS = 'linux'
            Invoke-Native -Name 'gosec for linux target' -Command { gosec -tags=integration ./... }
        } finally {
            $env:GOOS = $previousGoOS
        }
    }

    Assert-GoToolVersion -CommandName 'gitleaks' -ModulePath 'github.com/zricethezav/gitleaks/v8' -Version 'v8.30.1'
    Assert-MailWispTokenDetection -RepositoryRoot $repositoryRoot
    Invoke-Native -Name 'gitleaks working tree scan' -Command { gitleaks dir . --config .gitleaks.toml --no-banner --redact }
    Invoke-Native -Name 'gitleaks git history scan' -Command { gitleaks git --config .gitleaks.toml --no-banner --redact }

    $productionFrontendRoot = Join-Path $repositoryRoot 'web'
    if (Test-Path -LiteralPath (Join-Path $productionFrontendRoot 'package-lock.json')) {
        Push-Location -LiteralPath $productionFrontendRoot
        try {
            Invoke-Native -Name 'production frontend npm ci' -Command { npm ci }
            Invoke-Native -Name 'production frontend typecheck' -Command { npm run typecheck }
            Invoke-Native -Name 'production frontend lint' -Command { npm run lint }
            Invoke-Native -Name 'production frontend unit tests' -Command { npm test }
            Invoke-Native -Name 'production frontend build' -Command { npm run build }
            Invoke-Native -Name 'production frontend browser tests' -Command { npm run test:e2e }
            Invoke-Native -Name 'production Compose browser E2E' -Command { & (Join-Path $repositoryRoot 'scripts/e2e-compose.ps1') -SkipBuild }
            Invoke-Native -Name 'production frontend npm audit' -Command { npm audit --audit-level=low }
        } finally {
            Pop-Location
        }
    } else {
        throw 'web/package-lock.json不存在，生产前端验证不得跳过。'
    }

    $frontendRoot = Join-Path $repositoryRoot 'spikes/frontend'
    if (Test-Path -LiteralPath (Join-Path $frontendRoot 'package-lock.json')) {
        Push-Location -LiteralPath $frontendRoot
        try {
            Invoke-Native -Name 'npm ci' -Command { npm ci }
            Invoke-Native -Name 'frontend typecheck' -Command { npm run typecheck }
            Invoke-Native -Name 'frontend production build' -Command { npm run build }
            Invoke-Native -Name 'frontend browser tests' -Command { npm run test:e2e }
            Invoke-Native -Name 'npm audit' -Command { npm audit --audit-level=low }
        } finally {
            Pop-Location
        }
    }

    Invoke-Native -Name 'final git diff --check' -Command { git diff --check }
} finally {
    Pop-Location
}
