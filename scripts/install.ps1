<#
.SYNOPSIS
  Installs lts-slack-local-mcp into Claude Code (as a plugin, or as a plain MCP server).
.DESCRIPTION
  Gets the release zip for this machine (either -Zip, a local file, or a download from
  the GitHub release for -Version), verifies its SHA-256 against the release's
  SHA256SUMS, unpacks it under %LOCALAPPDATA%\lts-slack-local-mcp\install\<version>,
  and registers it with the Claude Code CLI.
  Requires Microsoft Edge or Google Chrome for the one-time interactive sign-in.
  -Mode plugin (default): registers the unpacked folder as a marketplace and installs
  the slack-local plugin.  -Mode mcp: adds only the MCP server (user scope).
.PARAMETER Zip
  A local release zip. Use -Sha256 to verify it (recommended), or -SkipChecksum to
  acknowledge it is unverified.
.PARAMETER Version
  Release to download (for example 0.1.0). Default: the latest release.
#>
param(
    [ValidateSet("plugin", "mcp")] [string] $Mode = "plugin",
    [string] $Zip,
    [string] $Sha256,
    [switch] $SkipChecksum,
    [string] $Version,
    [string] $Repo = "magpern/lts-slack-local-mcp"
)
$ErrorActionPreference = "Stop"
$arch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "Unsupported architecture" }
}
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("lts-slack-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    if (-not $Zip) {
        if (-not $Version) {
            $Version = ((Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name).TrimStart("v")
        }
        if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Unexpected version '$Version'" }
        $name = "lts-slack-local-$Version-windows-$arch.zip"
        $base = "https://github.com/$Repo/releases/download/v$Version"
        $Zip = Join-Path $tmp $name
        Write-Host "Downloading $name ..."
        Invoke-WebRequest "$base/$name" -OutFile $Zip
        Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $tmp "SHA256SUMS")
        $line = Get-Content (Join-Path $tmp "SHA256SUMS") | Where-Object { $_ -match ("\s" + [regex]::Escape($name) + "$") } | Select-Object -First 1
        if (-not $line) { throw "$name is not listed in SHA256SUMS" }
        $Sha256 = ($line -split "\s+")[0]
    } elseif (-not $Sha256 -and -not $SkipChecksum) {
        throw "Pass -Sha256 <hash> to verify the local zip, or -SkipChecksum to install it unverified."
    }
    if ($Sha256) {
        $actual = (Get-FileHash $Zip -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $Sha256.ToLower()) { throw "Checksum mismatch for $Zip (expected $Sha256, got $actual). Not installing." }
        Write-Host "Checksum OK."
    } else {
        Write-Warning "Installing without checksum verification."
    }

    # Refuse archive entries that escape the target folder.
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $z = [IO.Compression.ZipFile]::OpenRead($Zip)
    try {
        foreach ($e in $z.Entries) {
            if ($e.FullName -match '(^|[\\/])\.\.([\\/]|$)' -or [IO.Path]::IsPathRooted($e.FullName)) {
                throw "Unsafe path in archive: $($e.FullName)"
            }
        }
    } finally { $z.Dispose() }

    $label = if ($Version) { $Version } else { "local" }
    $dest = Join-Path $env:LOCALAPPDATA "lts-slack-local-mcp\install\$label"
    if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
    New-Item -ItemType Directory -Force -Path $dest | Out-Null
    Expand-Archive -Path $Zip -DestinationPath $dest -Force
    $exe = Join-Path $dest "plugin\bin\slack-mcp.exe"
    if (-not (Test-Path $exe)) { throw "The archive does not contain plugin\bin\slack-mcp.exe" }

    if (-not (Get-Command claude -ErrorAction SilentlyContinue)) {
        Write-Host "Claude Code CLI not found. Add this MCP server manually:"
        Write-Host "  command: $exe"
        exit 0
    }
    if ($Mode -eq "plugin") {
        & claude plugin marketplace add $dest
        if ($LASTEXITCODE -ne 0) { throw "claude plugin marketplace add failed" }
        & claude plugin install "slack-local@lts-slack-local-mcp"
        if ($LASTEXITCODE -ne 0) { throw "claude plugin install failed" }
    } else {
        & claude mcp add --scope user slack -- $exe
        if ($LASTEXITCODE -ne 0) { throw "claude mcp add failed" }
    }
    Write-Host "Done. Restart Claude Code, then ask it to call the Slack 'login' tool (a browser window opens once for sign-in)."
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
