#!/bin/sh
# vibe-rpc installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/chauveaul/vibe-rpc/main/install.sh | sh
#
# Options (environment variables):
#   VIBE_RPC_VERSION      install a specific version (e.g. 2.1.0); default: latest
#   VIBE_RPC_INSTALL_DIR  where to put the binary; default: ~/.local/bin
#   VIBE_RPC_RELEASE_URL  release download base, for mirrors/testing; default:
#                         https://github.com/chauveaul/vibe-rpc/releases/download
#
# Downloads the release archive for this OS/arch, verifies its SHA-256 against
# the release checksums file, and installs the vibe-rpc binary. Windows users:
# download the .zip from the releases page.
set -eu

REPO="chauveaul/vibe-rpc"
RELEASE_URL="${VIBE_RPC_RELEASE_URL:-https://github.com/$REPO/releases/download}"
INSTALL_DIR="${VIBE_RPC_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'vibe-rpc install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "needs '$1' but it isn't installed"; }

need curl
need tar
need uname

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS $(uname -s); on Windows, download the .zip from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac
# macOS ships one universal binary.
[ "$os" = darwin ] && arch=all

version="${VIBE_RPC_VERSION:-}"
if [ -z "$version" ]; then
  # /releases/latest redirects to /releases/tag/vX.Y.Z; read the tag from it.
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
    die "couldn't reach GitHub to find the latest release"
  version="${latest##*/}"
  case "$version" in
    v[0-9]*) ;;
    *) die "no published release found yet" ;;
  esac
fi
version="${version#v}"

archive="vibe-rpc_${version}_${os}_${arch}.tar.gz"
sums="vibe-rpc_${version}_checksums.txt"
base="$RELEASE_URL/v${version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading vibe-rpc ${version} (${os}/${arch})…"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "download failed: $base/$archive"
curl -fsSL -o "$tmp/$sums" "$base/$sums" || die "download failed: $base/$sums"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/$sums")
[ -n "$want" ] || die "checksums file has no entry for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
  die "needs sha256sum or shasum to verify the download"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $archive; refusing to install"
say "Checksum verified."

tar -xzf "$tmp/$archive" -C "$tmp" vibe-rpc || die "couldn't extract vibe-rpc from $archive"
mkdir -p "$INSTALL_DIR" || die "can't create $INSTALL_DIR"
# Write beside the target then rename, so a running copy is never half-written.
cp "$tmp/vibe-rpc" "$INSTALL_DIR/.vibe-rpc.new" || die "can't write to $INSTALL_DIR (set VIBE_RPC_INSTALL_DIR)"
chmod 755 "$INSTALL_DIR/.vibe-rpc.new"
mv -f "$INSTALL_DIR/.vibe-rpc.new" "$INSTALL_DIR/vibe-rpc"

say "Installed $("$INSTALL_DIR/vibe-rpc" --version) to $INSTALL_DIR/vibe-rpc"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "" ; say "Note: $INSTALL_DIR isn't on your PATH. Add this to your shell profile:" ; say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
say ""
say "Next: run 'vibe-rpc setup' to wire Antigravity & Claude Code hooks and start the daemon."
