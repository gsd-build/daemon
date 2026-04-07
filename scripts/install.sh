#!/bin/sh
# GSD Cloud daemon installer.
# Usage: curl -fsSL https://install.gsd.build | sh
#
# Environment variables (advanced):
#   GSD_INSTALL_DIR    - override install directory (default: $HOME/.gsd-cloud/bin)
#   GSD_REPO           - override the GitHub repo (default: gsd-build/daemon)
#   GSD_VERSION        - install a specific version tag (default: latest daemon/v*)
#   GSD_API_BASE       - override GitHub API base (testing only; trusts env)
#   GSD_DOWNLOAD_BASE  - override release asset base URL (testing only; trusts env)

set -eu

REPO="${GSD_REPO:-gsd-build/daemon}"
INSTALL_DIR="${GSD_INSTALL_DIR:-$HOME/.gsd-cloud/bin}"
BIN_NAME="gsd-cloud"

bold=""
reset=""
if [ -t 1 ]; then
    bold=$(printf '\033[1m')
    reset=$(printf '\033[0m')
fi

say() {
    printf '%s\n' "$1"
}

err() {
    printf 'error: %s\n' "$1" >&2
    exit 1
}

need_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        err "required command not found: $1"
    fi
}

detect_os() {
    case "$(uname -s)" in
        Darwin) printf 'darwin' ;;
        Linux)  printf 'linux' ;;
        *)
            err "unsupported OS: $(uname -s). Windows users: download manually from https://github.com/${REPO}/releases"
            ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   printf 'amd64' ;;
        arm64|aarch64)  printf 'arm64' ;;
        *)
            err "unsupported architecture: $(uname -m)"
            ;;
    esac
}

fetch_latest_tag() {
    # Returns the latest tag like "daemon/v0.1.0" — strips quotes and other JSON noise.
    if [ -n "${GSD_VERSION:-}" ]; then
        version_check="${GSD_VERSION#v}"
        case "$version_check" in
            ""|*[!0-9A-Za-z.+-]*)
                err "invalid GSD_VERSION: $GSD_VERSION"
                ;;
        esac
        printf 'daemon/%s' "$GSD_VERSION"
        return
    fi
    # Use the GitHub Releases API. Filter to tags starting with "daemon/v".
    api_base="${GSD_API_BASE:-https://api.github.com}"
    api_url="${api_base}/repos/${REPO}/releases"
    json=$(curl -fsSL "$api_url") || err "failed to fetch release list from $api_url"
    # Pick the first tag_name that starts with daemon/v
    tag=$(printf '%s' "$json" | grep -o '"tag_name": *"daemon/v[^"]*"' | head -n1 | sed 's/.*"daemon\/v\([^"]*\)".*/daemon\/v\1/')
    if [ -z "$tag" ]; then
        err "no daemon/v* release found in $REPO"
    fi
    # Validate the version portion before we interpolate it into a URL.
    version_check="${tag#daemon/v}"
    case "$version_check" in
        ""|*[!0-9A-Za-z.+-]*)
            err "invalid release tag from $REPO: $tag"
            ;;
    esac
    printf '%s' "$tag"
}

download() {
    src="$1"
    dst="$2"
    if ! curl -fsSL "$src" -o "$dst"; then
        err "failed to download $src"
    fi
}

verify_checksum() {
    bin_path="$1"
    sum_path="$2"
    expected=$(awk '{print $1}' "$sum_path")
    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$bin_path" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$bin_path" | awk '{print $1}')
    else
        err "no sha256 tool found (need sha256sum or shasum)"
    fi
    if [ "$expected" != "$actual" ]; then
        err "checksum mismatch: expected $expected, got $actual"
    fi
}

ensure_path_hint() {
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*)
            return 0
            ;;
    esac
    say ""
    say "${bold}Add $INSTALL_DIR to your PATH:${reset}"
    shell_name=$(basename "${SHELL:-sh}")
    case "$shell_name" in
        zsh)
            say "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc"
            say "  source ~/.zshrc"
            ;;
        bash)
            say "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.bashrc"
            say "  source ~/.bashrc"
            ;;
        fish)
            say "  fish_add_path $INSTALL_DIR"
            ;;
        *)
            say "  Add this line to your shell rc file:"
            say "    export PATH=\"$INSTALL_DIR:\$PATH\""
            ;;
    esac
}

main() {
    say "${bold}Installing GSD Cloud daemon${reset}"
    need_cmd curl
    need_cmd uname
    need_cmd mkdir
    need_cmd mv
    need_cmd chmod
    need_cmd grep
    need_cmd sed
    need_cmd awk

    OS=$(detect_os)
    ARCH=$(detect_arch)
    say "  platform: ${OS}/${ARCH}"

    TAG=$(fetch_latest_tag)
    VERSION_TAG="${TAG#daemon/}"
    say "  version:  ${VERSION_TAG}"

    ASSET_NAME="gsd-cloud-${VERSION_TAG}-${OS}-${ARCH}"
    DOWNLOAD_BASE="${GSD_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download/${TAG}}"
    BIN_URL="${DOWNLOAD_BASE}/${ASSET_NAME}"
    SUM_URL="${BIN_URL}.sha256"

    TMPDIR_PATH=$(mktemp -d)
    trap 'rm -rf "$TMPDIR_PATH"' EXIT INT TERM

    say "  downloading ${ASSET_NAME}..."
    download "$BIN_URL" "$TMPDIR_PATH/$ASSET_NAME"
    download "$SUM_URL" "$TMPDIR_PATH/$ASSET_NAME.sha256"

    say "  verifying checksum..."
    verify_checksum "$TMPDIR_PATH/$ASSET_NAME" "$TMPDIR_PATH/$ASSET_NAME.sha256"

    mkdir -p "$INSTALL_DIR"
    # Two-step install so an upgrade works even if the old binary is running.
    # Stage in the install dir (same filesystem -> rename is atomic), then rename over.
    NEW_BIN="$INSTALL_DIR/$BIN_NAME.new"
    mv "$TMPDIR_PATH/$ASSET_NAME" "$NEW_BIN"
    chmod +x "$NEW_BIN"
    mv "$NEW_BIN" "$INSTALL_DIR/$BIN_NAME"

    say ""
    say "${bold}Installed!${reset}"
    say "  $INSTALL_DIR/$BIN_NAME"
    say ""
    "$INSTALL_DIR/$BIN_NAME" version || true

    ensure_path_hint

    say ""
    say "${bold}Next steps:${reset}"
    say "  1. Open https://cloud.gsd.build to get your pairing code"
    say "  2. Run: ${bold}gsd-cloud login${reset}"
    say "  3. Run: ${bold}gsd-cloud start${reset}"
    say ""
}

main "$@"
