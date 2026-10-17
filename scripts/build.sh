#!/usr/bin/env bash
# Cross-compile netdiag for Windows and macOS (amd64 + arm64) and zip each
# target with README.md, HOW-TO-OPEN.txt and the license files into dist/.
#
# Usage: bash scripts/build.sh            (VERSION defaults to git describe)
#        VERSION=1.2.0 bash scripts/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST="dist"
TARGETS="windows/amd64 windows/arm64 darwin/amd64 darwin/arm64"

DOCS="README.md HOW-TO-OPEN.txt LICENSE NOTICE THIRD_PARTY_LICENSES.txt"
for f in $DOCS; do
  [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
done

stage="$(mktemp -d)"
trap 'rm -rf "$stage" cmd/netdiag/rsrc_windows_*.syso' EXIT

# make_zip <dest.zip> <dir-containing-files>  (files are added at the zip root)
# Uses tools/mkzip (pure Go) so the build is identical on every OS and the
# netdiag binary keeps its executable bit for macOS.
make_zip() {
  local dest="$1" src="$2"
  rm -f "$dest"
  "$mkzip" -o "$dest" -x netdiag -x netdiag.exe "$src"/*
}

mkdir -p "$DIST"
# The license file must list exactly the modules being shipped.
go run ./tools/licenses -check
mkzip="$stage/mkzip"
go build -o "$mkzip" ./tools/mkzip
echo "netdiag version: $VERSION"

# Version info, manifest and icon for netdiag.exe (cmd/netdiag/winres). go
# build links the generated rsrc_windows_*.syso into the Windows targets only.
go tool go-winres make --in cmd/netdiag/winres/winres.json --out cmd/netdiag/rsrc \
  --arch amd64,arm64 --product-version "$VERSION" --file-version "$VERSION"

for target in $TARGETS; do
  goos="${target%/*}"
  goarch="${target#*/}"
  bin="netdiag"
  [ "$goos" = "windows" ] && bin="netdiag.exe"

  pkg="$stage/$goos-$goarch"
  mkdir -p "$pkg"
  echo "building $goos/$goarch"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$pkg/$bin" ./cmd/netdiag
  cp $DOCS "$pkg/"

  zipname="netdiag-$goos-$goarch.zip"
  make_zip "$DIST/$zipname" "$pkg"
  echo "wrote $DIST/$zipname"
  rm -rf "$pkg"
done
