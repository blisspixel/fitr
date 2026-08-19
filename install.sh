#!/usr/bin/env sh
# fitr installer.
#
#   curl -fsSL https://raw.githubusercontent.com/blisspixel/fitr/main/install.sh | sh
#
# Downloads one static binary for your platform. No interpreter, no package
# manager, no virtualenv. Set FITR_VERSION to pin, FITR_BIN to relocate.
# FITR_NO_VERIFY=1 skips the checksum check.
set -eu

REPO="blisspixel/fitr"
VERSION="${FITR_VERSION:-latest}"

err() {
  echo "error: $1" >&2
  [ -n "${2:-}" ] && echo " note: $2" >&2
  [ -n "${3:-}" ] && echo " hint: $3" >&2
  exit 1
}

have() { command -v "$1" >/dev/null 2>&1; }

download() {
  _url=$1
  _dest=$2
  if have curl; then
    curl -fsSL "$_url" -o "$_dest"
  elif have wget; then
    wget -qO "$_dest" "$_url"
  else
    return 1
  fi
}

checksum() {
  _file=$1
  if have sha256sum; then
    sha256sum "$_file" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$_file" | awk '{print $1}'
  else
    echo ""
  fi
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) err "unsupported architecture $arch" "fitr ships amd64 and arm64 builds" ;;
esac

bin="fitr"
default_bin="$HOME/.local/bin"
case "$os" in
  linux)
    asset="fitr-linux-$arch"
    ;;
  darwin)
    asset="fitr-darwin-$arch"
    ;;
  mingw*|msys*|cygwin*)
    os="windows"
    bin="fitr.exe"
    asset="fitr-windows-$arch.exe"
    if [ -n "${LOCALAPPDATA:-}" ]; then
      default_bin="$LOCALAPPDATA/fitr"
    fi
    ;;
  *)
    err "unsupported OS $os" \
      "fitr ships linux, macOS, and Windows builds" \
      "on Windows PowerShell: irm https://raw.githubusercontent.com/blisspixel/fitr/main/install.ps1 | iex"
    ;;
esac

BIN_DIR="${FITR_BIN:-$default_bin}"

if [ "$VERSION" = "latest" ]; then
  url="https://github.com/$REPO/releases/latest/download/$asset"
  sum_url="https://github.com/$REPO/releases/latest/download/SHA256SUMS"
else
  case "$VERSION" in
    v*) ;;
    *) VERSION="v$VERSION" ;;
  esac
  url="https://github.com/$REPO/releases/download/$VERSION/$asset"
  sum_url="https://github.com/$REPO/releases/download/$VERSION/SHA256SUMS"
fi

echo "  installing fitr ($os/$arch) -> $BIN_DIR"
mkdir -p "$BIN_DIR"
tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.sums"' EXIT

got_binary=0
set +e
download "$url" "$tmp"
dl=$?
set -e
if [ "$dl" -eq 0 ]; then
  # A GitHub 404 page is HTML and tiny; a real binary is several megabytes.
  size=$(wc -c < "$tmp" | tr -d ' ')
  if [ "$size" -ge 1000000 ]; then
    got_binary=1
  fi
fi

if [ "$got_binary" -eq 1 ]; then
  if [ "${FITR_NO_VERIFY:-}" != "1" ]; then
    got=$(checksum "$tmp")
    if [ -z "$got" ]; then
      echo " note: no sha256sum/shasum on PATH; skipping verify" >&2
    else
      set +e
      download "$sum_url" "$tmp.sums"
      sums_ok=$?
      set -e
      if [ "$sums_ok" -eq 0 ]; then
        if ! grep -qi "$got" "$tmp.sums"; then
          err "checksum mismatch" "got $got" "the download may be corrupt; re-run the installer"
        fi
      else
        echo " note: no SHA256SUMS at this release; skipping verify" >&2
      fi
    fi
  fi
  chmod +x "$tmp"
  mv "$tmp" "$BIN_DIR/$bin"
  trap 'rm -f "$tmp.sums"' EXIT
elif have go; then
  rm -f "$tmp"
  gover=$VERSION
  echo "  no release binary; building with Go -> $BIN_DIR"
  GOBIN="$BIN_DIR" go install "github.com/$REPO/cmd/fitr@$gover" || \
    err "go install failed" "needs Go 1.25+" "git clone https://github.com/$REPO && cd fitr && make install"
else
  err "could not fetch a binary" "$url" \
    "install curl (or Go 1.25+), or clone and 'make install'"
fi

echo "  installed: $BIN_DIR/$bin"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    echo " hint: $BIN_DIR is not on your PATH; add it to your shell profile"
    echo "       echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.profile"
    ;;
esac
echo
echo "  next:  fitr device        # confirm it sees your hardware"
echo "         fitr run <model> --full"
echo "         fitr run https://huggingface.co/<org>/<gguf-repo>"
