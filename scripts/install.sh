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
#   GSD_SIGNING_PUBLIC_KEY_PATH - override pinned release signing public key (testing only)

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

write_embedded_public_key() {
    cat > "$1" <<'EOF'
-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAp5KuwlYW0Q76WF3CtCSr
BfH3NAn3IY5/D+jszEcjvOR0T0PTkRQe+wpz/GS0yIuDntDqQ/a5minVt2+bDCRI
75qKx13+8Z8HmloChkJ5ISgN7NzonBWy8YKl/iHVtdL0UZq4DMBmpBD/WtRVVci9
9mEvYgOmILScYOkvVEUtUkLCvvzLjhcg6gl8QOtQVYaboFdmEQyXLTJIidxSArx5
D77ni+Romf+2+nNFJWR5W9FCkrR+PVq9L+oAH2lEtKJhJJEPZ3kiAx7d/QDZmU2V
Ip+l4P1osw55RAyEgQm5duCgTVO6Vy3DvH6no6XBRtVZFMzwLD63wtK7Z5RqW50/
9gPFhbDToGymsrYiyIN6oQAOtp/2/nMn0VQBiRD3Cu5xrVR4zgt4//uvWujkd34P
RqiTSVAOOvy6DDsAn/onFoa3PTo0Zv5UZfsTsA22pRFJ3aaisVMqo9Qxa2J99YnC
F0jGh0inhfDejRyhdj0b8pVkwL93kUtkiBQzhFd07SiZo8lOlQL4ZilWz/luHl/N
co05rYuklt4lft3rXn1cTaS6vXqT89UupCT4AdKmOYaUl7gsaUuEGqQihlKEa5T6
cqMKS+1JWENS174P12O/aa5AU9so5qeFFsndcuaYnssuoY4zhF69VhCBM92p+kpp
p2fTCbx18bnwQv/UXWUe5E8CAwEAAQ==
-----END PUBLIC KEY-----
EOF
}

prepare_public_key() {
    if [ -n "${GSD_SIGNING_PUBLIC_KEY_PATH:-}" ]; then
        printf '%s' "$GSD_SIGNING_PUBLIC_KEY_PATH"
        return
    fi

    pub_path="$TMPDIR_PATH/release-signing-public-key.pem"
    write_embedded_public_key "$pub_path"
    printf '%s' "$pub_path"
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
    # Use the GitHub Releases API "latest" endpoint which returns the most
    # recent non-prerelease, non-draft release regardless of creation order.
    api_base="${GSD_API_BASE:-https://api.github.com}"
    api_url="${api_base}/repos/${REPO}/releases/latest"
    json=$(curl -fsSL "$api_url") || err "failed to fetch latest release from $api_url"
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
    sums_path="$2"
    asset_name="$3"
    expected=$(awk -v name="$asset_name" '$2 == name {print $1; exit}' "$sums_path")
    if [ -z "$expected" ]; then
        err "checksum not found for $asset_name"
    fi
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

verify_signature() {
    sums_path="$1"
    sig_path="$2"
    pub_path="$3"

    if ! openssl dgst -sha256 -verify "$pub_path" -signature "$sig_path" "$sums_path" >/dev/null 2>&1; then
        err "release signature verification failed"
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
    need_cmd mktemp
    need_cmd openssl

    OS=$(detect_os)
    ARCH=$(detect_arch)
    say "  platform: ${OS}/${ARCH}"

    TAG=$(fetch_latest_tag)
    VERSION_TAG="${TAG#daemon/}"
    say "  version:  ${VERSION_TAG}"

    ASSET_NAME="gsd-cloud-${VERSION_TAG}-${OS}-${ARCH}"
    DOWNLOAD_BASE="${GSD_DOWNLOAD_BASE:-https://github.com/${REPO}/releases/download/${TAG}}"
    BIN_URL="${DOWNLOAD_BASE}/${ASSET_NAME}"
    SUMS_URL="${DOWNLOAD_BASE}/SHA256SUMS"
    SIG_URL="${DOWNLOAD_BASE}/SHA256SUMS.sig"

    TMPDIR_PATH=$(mktemp -d)
    trap 'rm -rf "$TMPDIR_PATH"' EXIT INT TERM
    PUBKEY_PATH=$(prepare_public_key)

    say "  downloading ${ASSET_NAME}..."
    download "$BIN_URL" "$TMPDIR_PATH/$ASSET_NAME"
    download "$SUMS_URL" "$TMPDIR_PATH/SHA256SUMS"
    download "$SIG_URL" "$TMPDIR_PATH/SHA256SUMS.sig"

    say "  verifying release signature..."
    verify_signature "$TMPDIR_PATH/SHA256SUMS" "$TMPDIR_PATH/SHA256SUMS.sig" "$PUBKEY_PATH"

    say "  verifying checksum..."
    verify_checksum "$TMPDIR_PATH/$ASSET_NAME" "$TMPDIR_PATH/SHA256SUMS" "$ASSET_NAME"

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
    say "  1. Open https://app.gsd.build to get your pairing code"
    say "  2. Run: ${bold}gsd-cloud login${reset}"
    say "  3. Run: ${bold}gsd-cloud start${reset}"
    say ""
}

main "$@"
