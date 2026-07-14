$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true

function Invoke-Native {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [scriptblock]$Command
    )

    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE."
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
        throw "$CommandName未安装，验证不得跳过。"
    }
    $metadata = & go version -m $command.Source | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "无法读取$CommandName构建元数据。"
    }
    $expected = "mod`t$ModulePath`t$Version"
    if (-not $metadata.Contains($expected)) {
        throw "$CommandName版本不符合要求，必须为$ModulePath@$Version。"
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
        throw "$CommandName未安装，PostgreSQL备份恢复验证不得跳过。"
    }
    $reported = & $CommandName --version | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "无法读取$CommandName版本。"
    }
    if ($reported -notmatch "^$([regex]::Escape($CommandName)) \(PostgreSQL\) $([regex]::Escape($Version))(?:\s|$)") {
        throw "$CommandName版本不符合要求，必须为PostgreSQL $Version。实际：$($reported.Trim())"
    }
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$env:GOTOOLCHAIN = 'go1.26.5'

Push-Location -LiteralPath $repositoryRoot
try {
    Invoke-Native -Name 'git diff --check' -Command { git diff --check }
    Invoke-Native -Name 'go fmt' -Command { go fmt ./... }
    Invoke-Native -Name 'go test' -Command { go test ./... }
    Invoke-Native -Name 'go test -race' -Command { go test -race ./... }
    Invoke-Native -Name 'go vet' -Command { go vet ./... }

    Assert-PostgreSQLToolVersion -CommandName 'pg_dump' -Version '18.4'
    Assert-PostgreSQLToolVersion -CommandName 'pg_restore' -Version '18.4'

    if (Get-Command docker -ErrorAction SilentlyContinue) {
        Invoke-Native -Name 'docker info' -Command { docker info --format '{{.ServerVersion}}' }
        $linuxGoImage = 'registry-1.docker.io/library/golang@sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c'
        Invoke-Native -Name 'linux go test and race test' -Command {
            docker run --rm --platform linux/amd64 `
                --mount "type=bind,source=$repositoryRoot,target=/src,readonly" `
                -w /src `
                -e GOPROXY=https://proxy.golang.org,direct `
                $linuxGoImage `
                sh -c 'go test ./... && go test -race ./...'
        }
        Invoke-Native -Name 'postgres integration tests' -Command { go test -tags=integration ./internal/postgres -count=1 }
        Invoke-Native -Name 'postgres integration race tests' -Command { go test -race -tags=integration ./internal/postgres -count=1 }
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
    Invoke-Native -Name 'gitleaks working tree scan' -Command { gitleaks dir . --no-banner --redact }
    Invoke-Native -Name 'gitleaks git history scan' -Command { gitleaks git --no-banner --redact }

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
