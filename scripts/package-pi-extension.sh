#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version> <output-dir>" >&2
  exit 2
fi

VERSION="$1"
OUT_DIR="$2"

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.+-]+)?$ ]]; then
  echo "invalid version: $VERSION" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
EXTENSION_DIR="$REPO_ROOT/internal/pi/extension"
ARCHIVE_NAME="gsd-cloud-pi-extension-${VERSION}.tar.gz"

mkdir -p "$OUT_DIR"

EXTENSION_FILES=()
while IFS= read -r file; do
  EXTENSION_FILES+=("$file")
done < <(
  cd "$EXTENSION_DIR"
  {
    printf '%s\n' package.json package-lock.json
    find . -maxdepth 1 -type f \( -name '*.js' -o -name '*.ts' \) \
      ! -name '*.test.*' \
      -print | sed 's#^\./##'
  } | sort -u
)

TAR_ARGS=(-czf "$OUT_DIR/$ARCHIVE_NAME" -C "$EXTENSION_DIR")
if tar --version 2>/dev/null | grep -qi "gnu tar"; then
  TAR_ARGS=(
    --sort=name
    --mtime="UTC 2020-01-01"
    --owner=0
    --group=0
    --numeric-owner
    --dereference
    -czf "$OUT_DIR/$ARCHIVE_NAME"
    -C "$EXTENSION_DIR"
  )
fi

tar "${TAR_ARGS[@]}" "${EXTENSION_FILES[@]}"

(
  cd "$OUT_DIR"
  sha256sum "$ARCHIVE_NAME" > "$ARCHIVE_NAME.sha256"
)
