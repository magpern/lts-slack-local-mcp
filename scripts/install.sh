#!/usr/bin/env bash
# Installs lts-slack-local-mcp into Claude Code (plugin or plain MCP server).
# Usage: install.sh [--mode plugin|mcp] [--zip FILE --sha256 HASH | --skip-checksum] [--version X.Y.Z]
# Verifies the release zip against the release's SHA256SUMS before unpacking.
# Needs Microsoft Edge or Google Chrome for the one-time interactive sign-in.
set -euo pipefail
mode=plugin; zip=""; sha=""; skip=0; version=""; repo="magpern/lts-slack-local-mcp"
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) mode="$2"; shift 2;;
    --zip) zip="$2"; shift 2;;
    --sha256) sha="$2"; shift 2;;
    --skip-checksum) skip=1; shift;;
    --version) version="$2"; shift 2;;
    --repo) repo="$2"; shift 2;;
    *) echo "unknown argument: $1" >&2; exit 2;;
  esac
done
case "$mode" in plugin|mcp) ;; *) echo "--mode must be plugin or mcp" >&2; exit 2;; esac
case "$(uname -s)" in Linux) os=linux;; Darwin) os=darwin;; *) echo "unsupported OS (use install.ps1 on Windows)" >&2; exit 1;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) echo "unsupported architecture" >&2; exit 1;; esac
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
sha256_of() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
if [ -z "$zip" ]; then
  if [ -z "$version" ]; then
    version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)"
  fi
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "unexpected version '$version'" >&2; exit 1; }
  name="lts-slack-local-$version-$os-$arch.zip"
  base="https://github.com/$repo/releases/download/v$version"
  zip="$tmp/$name"
  echo "Downloading $name ..."
  curl -fsSL "$base/$name" -o "$zip"
  curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
  sha="$(grep -E "[[:space:]]$name\$" "$tmp/SHA256SUMS" | head -1 | cut -d' ' -f1)"
  [ -n "$sha" ] || { echo "$name is not listed in SHA256SUMS" >&2; exit 1; }
elif [ -z "$sha" ] && [ "$skip" != 1 ]; then
  echo "pass --sha256 HASH to verify the local zip, or --skip-checksum to install it unverified" >&2; exit 2
fi
if [ -n "$sha" ]; then
  actual="$(sha256_of "$zip")"
  [ "$actual" = "$(echo "$sha" | tr 'A-F' 'a-f')" ] || { echo "checksum mismatch (expected $sha, got $actual); not installing" >&2; exit 1; }
  echo "Checksum OK."
else
  echo "WARNING: installing without checksum verification" >&2
fi
if unzip -Z1 "$zip" | grep -E '(^|/)\.\.(/|$)|^/' >/dev/null; then echo "unsafe path in archive" >&2; exit 1; fi
dest="${XDG_DATA_HOME:-$HOME/.local/share}/lts-slack-local-mcp/install/${version:-local}"
rm -rf "$dest"; mkdir -p "$dest"; unzip -q "$zip" -d "$dest"
exe="$dest/plugin/bin/slack-mcp"
[ -f "$exe" ] || { echo "archive does not contain plugin/bin/slack-mcp" >&2; exit 1; }
chmod +x "$exe"
if ! command -v claude >/dev/null; then
  echo "Claude Code CLI not found. Add this MCP server manually: command: $exe"; exit 0
fi
if [ "$mode" = plugin ]; then
  claude plugin marketplace add "$dest"
  claude plugin install "slack-local@lts-slack-local-mcp"
else
  claude mcp add --scope user slack -- "$exe"
fi
echo "Done. Restart Claude Code, then ask it to call the Slack 'login' tool (a browser window opens once for sign-in)."
