function Assert-MailWispArtifactPath {
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$Path
    )

    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    $artifactRoot = [System.IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'artifacts'))
    $resolved = [System.IO.Path]::GetFullPath($Path)
    $prefix = $artifactRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolved.Equals($artifactRoot, $comparison) -and -not $resolved.StartsWith($prefix, $comparison)) {
        throw "Artifact path escaped the repository artifact root: $Path"
    }

    $current = $artifactRoot
    $relative = [System.IO.Path]::GetRelativePath($artifactRoot, $resolved)
    $segments = if ($relative -eq '.') { @() } else { $relative.Split([System.IO.Path]::DirectorySeparatorChar, [StringSplitOptions]::RemoveEmptyEntries) }
    $candidates = [System.Collections.Generic.List[string]]::new()
    $candidates.Add($artifactRoot)
    foreach ($segment in $segments) {
        $current = Join-Path $current $segment
        $candidates.Add($current)
    }
    foreach ($candidate in $candidates) {
        if (-not ([System.IO.Directory]::Exists($candidate) -or [System.IO.File]::Exists($candidate))) { continue }
        if (([System.IO.File]::GetAttributes($candidate) -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Artifact path contains a symbolic link or junction: $candidate"
        }
    }
    return $resolved
}

function Remove-MailWispArtifactDirectory {
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$Path
    )

    $resolved = Assert-MailWispArtifactPath -RepositoryRoot $RepositoryRoot -Path $Path
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}
