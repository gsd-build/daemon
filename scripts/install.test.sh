#!/bin/sh
# Local smoke test for scripts/install.sh.
# Builds a real daemon binary, serves it from a local HTTP server,
# runs install.sh against the fake server, and asserts the install succeeded.

set -eu

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

cleanup() {
    if [ -n "${SERVER_PID:-}" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "${WORK_DIR:-}" ]; then
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT INT TERM

WORK_DIR=$(mktemp -d)
DIST_DIR="$WORK_DIR/dist"
INSTALL_DIR="$WORK_DIR/install"
mkdir -p "$DIST_DIR" "$INSTALL_DIR"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
esac
case "$OS" in
    darwin|linux) : ;;
    *) echo "test only runs on darwin/linux"; exit 0 ;;
esac

command -v go >/dev/null 2>&1 || { echo "SKIP: go not installed"; exit 0; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 not installed"; exit 0; }

VERSION="v0.0.1-test"
ASSET_NAME="gsd-cloud-${VERSION}-${OS}-${ARCH}"

echo "Building daemon binary for ${OS}/${ARCH}..."
if ! (cd "$REPO_ROOT/apps/daemon" && \
    GOOS="$OS" GOARCH="$ARCH" CGO_ENABLED=0 go build \
    -ldflags "-s -w -X github.com/gsd-cloud/daemon/cmd.Version=0.0.1-test -X github.com/gsd-cloud/daemon/cmd.Commit=test -X github.com/gsd-cloud/daemon/cmd.BuildDate=$(date -u +%Y-%m-%d)" \
    -o "$DIST_DIR/$ASSET_NAME" ./); then
    echo "FAIL: go build failed for ${OS}/${ARCH}"
    exit 1
fi

echo "Computing checksum..."
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$DIST_DIR" && sha256sum "$ASSET_NAME" > "$ASSET_NAME.sha256")
else
    (cd "$DIST_DIR" && shasum -a 256 "$ASSET_NAME" > "$ASSET_NAME.sha256")
fi

echo "Building fake releases JSON..."
mkdir -p "$DIST_DIR/repos/fake/repo"
cat > "$DIST_DIR/repos/fake/repo/releases" <<EOF
[
  {
    "tag_name": "daemon/${VERSION}",
    "name": "Daemon ${VERSION}"
  }
]
EOF

echo "Starting local HTTP server on port 0..."
PORT_FILE="$WORK_DIR/server.port"
python3 - "$DIST_DIR" "$PORT_FILE" <<'PYEOF' &
import sys, os, http.server, socketserver

serve_dir = sys.argv[1]
port_file = sys.argv[2]
os.chdir(serve_dir)

class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

with socketserver.TCPServer(("127.0.0.1", 0), QuietHandler) as httpd:
    port = httpd.server_address[1]
    with open(port_file, "w") as f:
        f.write(str(port))
    httpd.serve_forever()
PYEOF
SERVER_PID=$!

# Wait for the server to write its port
i=0
while [ ! -s "$PORT_FILE" ] && [ "$i" -lt 20 ]; do
    sleep 0.1
    i=$((i + 1))
done
PORT=$(cat "$PORT_FILE" 2>/dev/null || true)
if [ -z "$PORT" ]; then
    echo "failed to extract server port after 2s"
    exit 1
fi
SERVER_BASE="http://127.0.0.1:$PORT"
echo "Server up at $SERVER_BASE"

echo "Running install.sh against the fake server..."
GSD_REPO="fake/repo" \
GSD_API_BASE="$SERVER_BASE" \
GSD_DOWNLOAD_BASE="$SERVER_BASE" \
GSD_INSTALL_DIR="$INSTALL_DIR" \
sh "$SCRIPT_DIR/install.sh"

echo ""
echo "Verifying install..."
if [ ! -x "$INSTALL_DIR/gsd-cloud" ]; then
    echo "FAIL: $INSTALL_DIR/gsd-cloud not installed or not executable"
    exit 1
fi

OUTPUT=$("$INSTALL_DIR/gsd-cloud" version)
if ! echo "$OUTPUT" | grep -q "0.0.1-test"; then
    echo "FAIL: installed binary did not report expected version: $OUTPUT"
    exit 1
fi

echo "PASS: installer works end-to-end"
