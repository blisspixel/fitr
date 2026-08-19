#!/usr/bin/env sh
# fitr installer.
#
#   curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
#
# Downloads one static binary for your platform. No interpreter, no package
# manager, no virtualenv. Set FITR_VERSION to pin, FITR_BIN to relocate.
set -eu

REPO="blisspixel/fitr"
VERSION="${FITR_VERSION:-latest}"
BIN_DIR="${FITR_BIN:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "error: unsupported architecture $arch" >&2
     echo " note: fitr ships amd64 and arm64 builds" >&2
     exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "error: unsupported OS $os" >&2
     echo " hint: on Windows, download the .exe from the Releases page" >&2
     exit 1 ;;
esac

asset="fitr-${os}-${arch}"
if [ "$VERSION" = "latest" ]; then
  url="https://github.com/$REPO/releases/latest/download/$asset"
else
  url="https://github.com/$REPO/releases/download/$VERSION/$asset"
fi

echo "  installing fitr ($os/$arch) -> $BIN_DIR"
mkdir -p "$BIN_DIR"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
if ! curl -fsSL "$url" -o "$tmp"; then
  echo "error: download failed" >&2
  echo " note: $url" >&2
  echo " hint: check the release exists, or build from source with 'make install'" >&2
  exit 1
fi
chmod +x "$tmp"
mv "$tmp" "$BIN_DIR/fitr"
trap - EXIT

echo "  installed: $BIN_DIR/fitr"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo " hint: $BIN_DIR is not on your PATH; add it to your shell profile" ;;
esac
echo
echo "  next:  fitr device        # confirm it sees your hardware"
echo "         fitr run <model> --full"
