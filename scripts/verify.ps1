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

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$env:GOTOOLCHAIN = 'go1.26.5'

Push-Location -LiteralPath $repositoryRoot
try {
    Invoke-Native -Name 'git diff --check' -Command { git diff --check }
    Invoke-Native -Name 'go fmt' -Command { go fmt ./... }
    Invoke-Native -Name 'go test' -Command { go test ./... }
    Invoke-Native -Name 'go test -race' -Command { go test -race ./... }
    Invoke-Native -Name 'go vet' -Command { go vet ./... }

    if (Get-Command govulncheck -ErrorAction SilentlyContinue) {
        Invoke-Native -Name 'govulncheck' -Command { govulncheck ./... }
    } else {
        throw 'govulncheck未安装，安全验证不得跳过。'
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
