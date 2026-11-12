#!/usr/bin/env bash
# Build the Windows setup (one file for x64 and ARM64) with NSIS:
#
#   packaging/windows/build.sh VERSION DIST OUTFILE
#
# VERSION is a tag like v0.9.1, DIST holds the release binaries from
# 'make dist' (herakles-VERSION-windows-{amd64,arm64}.exe). Needs makensis
# (Debian: apt install nsis).
set -euo pipefail
version=$1 dist=$(realpath "$2") out=$(realpath -m "$3")
command -v makensis >/dev/null || { echo "makensis not found (apt install nsis)" >&2; exit 1; }
# NSIS wants a plain number: v0.9.1-3-gabcdef -> 0.9.1
num=$(sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/' <<<"$version")
[[ $num =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || num=0.0.0
cd "$(dirname "$0")"
makensis -V2 -INPUTCHARSET UTF8 -DVERSION="$num" \
  -DAMD64="$dist/herakles-$version-windows-amd64.exe" \
  -DARM64="$dist/herakles-$version-windows-arm64.exe" \
  -DOUTFILE="$out" herakles.nsi
