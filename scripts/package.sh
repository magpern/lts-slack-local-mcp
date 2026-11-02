#!/usr/bin/env bash
# Builds dist/lts-slack-local-<version>-<os>-<arch>.zip and appends its checksum
# to dist/SHA256SUMS. Usage: scripts/package.sh <windows|linux|darwin> <amd64|arm64>
# Stages only the marketplace manifest and the plugin (manifest, skill, MCP config,
# binary): never git data, local profiles, state, .env files or HAR captures.
set -euo pipefail
os="${1:?os: windows|linux|darwin}"
arch="${2:?arch: amd64|arm64}"
case "$os" in windows|linux|darwin) ;; *) echo "bad os" >&2; exit 2;; esac
case "$arch" in amd64|arm64) ;; *) echo "bad arch" >&2; exit 2;; esac
root="$(cd "$(dirname "$0")/.." && pwd)"
version="$(tr -d '[:space:]' < "$root/VERSION")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION must be MAJOR.MINOR.PATCH" >&2; exit 2; }
ext=""; [ "$os" = windows ] && ext=".exe"
name="lts-slack-local-$version-$os-$arch"
dist="$root/dist"; mkdir -p "$dist"
staging="$(mktemp -d)"; trap 'rm -rf "$staging"' EXIT
mkdir -p "$staging/plugin/bin" "$staging/.claude-plugin"
cp "$root/.claude-plugin/marketplace.json" "$staging/.claude-plugin/"
cp -R "$root/plugin/.claude-plugin" "$staging/plugin/.claude-plugin"
cp -R "$root/plugin/skills" "$staging/plugin/skills"
for f in LICENSE NOTICE README.md SECURITY.md; do cp "$root/$f" "$staging/"; done
sed -E "s/\"version\": *\"[^\"]*\"/\"version\": \"$version\"/" "$root/plugin/.claude-plugin/plugin.json" > "$staging/plugin/.claude-plugin/plugin.json"
printf '{\n  "mcpServers": {\n    "slack": {\n      "command": "${CLAUDE_PLUGIN_ROOT}/bin/slack-mcp%s"\n    }\n  }\n}\n' "$ext" > "$staging/plugin/.mcp.json"
commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || echo unknown)"
mod="$(cd "$root" && go list -m)"
built="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
(cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
  -ldflags "-s -w -X '$mod/pkg/version.Version=$version' -X '$mod/pkg/version.CommitHash=$commit' -X '$mod/pkg/version.BuildTime=$built' -X '$mod/pkg/version.BinaryName=slack-mcp'" \
  -o "$staging/plugin/bin/slack-mcp$ext" ./cmd/slack-mcp-server)
rm -f "$dist/$name.zip"
(cd "$staging" && zip -qr "$dist/$name.zip" .)
sum="$(cd "$dist" && (sha256sum "$name.zip" 2>/dev/null || shasum -a 256 "$name.zip"))"
touch "$dist/SHA256SUMS"
grep -v " $name.zip\$" "$dist/SHA256SUMS" > "$dist/SHA256SUMS.tmp" || true
echo "$sum" >> "$dist/SHA256SUMS.tmp"; mv "$dist/SHA256SUMS.tmp" "$dist/SHA256SUMS"
echo "Built $dist/$name.zip"
