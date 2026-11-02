<#
.SYNOPSIS
  Builds dist/lts-slack-local-<version>-<os>-<arch>.zip and appends its checksum to dist/SHA256SUMS.
.DESCRIPTION
  Cross-compiles the server (no cgo) and stages a minimal tree: the marketplace
  manifest and the plugin (manifest, skill, MCP config, the binary). It never
  includes git data, local profiles, state, .env files or HAR captures.
  The version comes from the VERSION file and is stamped into the binary and the
  plugin manifest.
#>
param(
    [ValidateSet("windows", "linux", "darwin")] [string] $Os = "windows",
    [ValidateSet("amd64", "arm64")] [string] $Arch = "amd64"
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$version = (Get-Content (Join-Path $root "VERSION") -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw "VERSION must be MAJOR.MINOR.PATCH, got '$version'" }
$ext = if ($Os -eq "windows") { ".exe" } else { "" }
$dist = Join-Path $root "dist"
$staging = Join-Path ([IO.Path]::GetTempPath()) ("lts-slack-pkg-" + [guid]::NewGuid())
$name = "lts-slack-local-$version-$Os-$Arch"
New-Item -ItemType Directory -Force -Path $dist | Out-Null
try {
    $plugin = Join-Path $staging "plugin"
    New-Item -ItemType Directory -Force -Path (Join-Path $plugin "bin"), (Join-Path $staging ".claude-plugin") | Out-Null
    Copy-Item (Join-Path $root ".claude-plugin\marketplace.json") (Join-Path $staging ".claude-plugin\")
    Copy-Item (Join-Path $root "plugin\.claude-plugin") (Join-Path $plugin ".claude-plugin") -Recurse
    Copy-Item (Join-Path $root "plugin\skills") (Join-Path $plugin "skills") -Recurse
    foreach ($f in "LICENSE", "NOTICE", "README.md", "SECURITY.md") { Copy-Item (Join-Path $root $f) $staging }

    # single-source the version
    $pj = Join-Path $plugin ".claude-plugin\plugin.json"
    $json = Get-Content $pj -Raw | ConvertFrom-Json
    $json.version = $version
    [IO.File]::WriteAllText($pj, ($json | ConvertTo-Json -Depth 10), (New-Object Text.UTF8Encoding $false))

    # MCP config pointing at this platform's binary
    $mcp = @{ mcpServers = @{ slack = @{ command = "`${CLAUDE_PLUGIN_ROOT}/bin/slack-mcp$ext" } } }
    [IO.File]::WriteAllText((Join-Path $plugin ".mcp.json"), ($mcp | ConvertTo-Json -Depth 10), (New-Object Text.UTF8Encoding $false))

    $commit = (& git -C $root rev-parse HEAD 2>$null)
    if (-not $commit) { $commit = "unknown" }
    $mod = (& go -C $root list -m)
    $ld = "-s -w -X '$mod/pkg/version.Version=$version' -X '$mod/pkg/version.CommitHash=$commit' -X '$mod/pkg/version.BuildTime=$([DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ'))' -X '$mod/pkg/version.BinaryName=slack-mcp'"
    $env:GOOS = $Os; $env:GOARCH = $Arch; $env:CGO_ENABLED = "0"
    try {
        & go -C $root build -trimpath -ldflags $ld -o (Join-Path $plugin "bin\slack-mcp$ext") ./cmd/slack-mcp-server
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    } finally { Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue }

    $zip = Join-Path $dist "$name.zip"
    if (Test-Path $zip) { Remove-Item $zip }
    Compress-Archive -Path (Join-Path $staging "*") -DestinationPath $zip
    $hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
    $sums = Join-Path $dist "SHA256SUMS"
    $lines = @()
    if (Test-Path $sums) { $lines = Get-Content $sums | Where-Object { $_ -notmatch [regex]::Escape("$name.zip") } }
    $lines += "$hash  $name.zip"
    [IO.File]::WriteAllText($sums, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))
    Write-Host "Built $zip"
    Write-Host "SHA256 $hash"
} finally {
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
}
