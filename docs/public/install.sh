#!/bin/sh
# ncli installer for Linux and macOS.
#
#   curl -fsSL https://ohstr.github.io/ncli/install.sh | sh
#
# Never requires sudo: if a directory already on PATH (e.g. /usr/local/bin)
# is writable by the current user it installs there, otherwise it falls back
# to ~/.local/bin and prints the PATH update needed.
#
# Env overrides: NCLI_VERSION (default: latest), NCLI_INSTALL_DIR.
set -eu

REPO="ohstr/ncli"
BIN_NAME="ncli"

# ---- output helpers ---------------------------------------------------

is_tty() { [ -t 1 ]; }

supports_color() {
    [ -z "${NO_COLOR:-}" ] && is_tty
}

if supports_color; then
    BOLD='\033[1m'; YELLOW='\033[33m'; RED='\033[31m'; RESET='\033[0m'
else
    BOLD=''; YELLOW=''; RED=''; RESET=''
fi

info()  { printf '%b\n' "${BOLD}==>${RESET} $1"; }
warn()  { printf '%b\n' "${YELLOW}warning:${RESET} $1" >&2; }
error() { printf '%b\n' "${RED}error:${RESET} $1" >&2; exit 1; }

# ---- platform detection -------------------------------------------------
#
# Raspberry Pi isn't a distinct OS branch here: 64-bit Raspberry Pi OS (Pi
# 3/4/5) reports Linux/aarch64 same as any other arm64 Linux box, so it
# falls through to the arm64 case below. Only 32-bit Raspberry Pi OS
# (armv6l/armv7l, older Pi 1/2 images) is unsupported, and gets a
# dedicated error message rather than a generic one.

detect_os() {
    case "$(uname -s)" in
        Linux)  echo linux ;;
        Darwin) echo darwin ;;
        *)
            error "unsupported OS: $(uname -s). ncli publishes prebuilt binaries for Linux and macOS only.
For Windows, use the PowerShell installer instead:
  irm https://ohstr.github.io/ncli/install.ps1 | iex"
            ;;
    esac
}

detect_arch() {
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)   echo amd64 ;;
        arm64|aarch64)  echo arm64 ;;
        armv6l|armv7l|arm)
            error "unsupported architecture: $arch (32-bit ARM - e.g. an older Raspberry Pi on a 32-bit OS image).
ncli only publishes 64-bit arm64 binaries. Either install a 64-bit OS, or install via:
  go install github.com/${REPO}/cmd/${BIN_NAME}@latest"
            ;;
        i386|i686)
            error "unsupported architecture: $arch (32-bit x86). Install via:
  go install github.com/${REPO}/cmd/${BIN_NAME}@latest"
            ;;
        *)
            error "unsupported architecture: $arch"
            ;;
    esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
info "Detected platform: ${OS}/${ARCH}"

# ---- version / URLs -----------------------------------------------------

VERSION="${NCLI_VERSION:-latest}"
ARCHIVE_NAME="${BIN_NAME}_${OS}_${ARCH}.tar.gz"

if [ "$VERSION" = "latest" ]; then
    ASSET_URL="https://github.com/${REPO}/releases/latest/download/${ARCHIVE_NAME}"
    CHECKSUMS_URL="https://github.com/${REPO}/releases/latest/download/checksums.txt"
else
    ASSET_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${ARCHIVE_NAME}"
    CHECKSUMS_URL="https://github.com/${REPO}/releases/download/v${VERSION}/checksums.txt"
fi

# ---- install directory: no sudo, ever -----------------------------------
#
# Priority: explicit override, then /usr/local/bin if it's writable, then
# the first writable directory already on PATH, then ~/.local/bin. We never
# escalate privileges to force a system directory.

resolve_install_dir() {
    if [ -n "${NCLI_INSTALL_DIR:-}" ]; then
        echo "$NCLI_INSTALL_DIR"
        return
    fi

    if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
        echo /usr/local/bin
        return
    fi

    old_ifs=$IFS
    IFS=:
    for dir in $PATH; do
        if [ -n "$dir" ] && [ -d "$dir" ] && [ -w "$dir" ]; then
            IFS=$old_ifs
            echo "$dir"
            return
        fi
    done
    IFS=$old_ifs

    echo "$HOME/.local/bin"
}

INSTALL_DIR="$(resolve_install_dir)"

# ---- download + verify + extract ----------------------------------------

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

ARCHIVE="$WORKDIR/$ARCHIVE_NAME"

info "Downloading ${BIN_NAME} ${VERSION}..."
curl -fsSL "$ASSET_URL" -o "$ARCHIVE" || error "download failed: $ASSET_URL"

if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then
    CHECKSUMS_FILE="$WORKDIR/checksums.txt"
    if curl -fsSL "$CHECKSUMS_URL" -o "$CHECKSUMS_FILE" 2>/dev/null; then
        EXPECTED="$(grep " ${ARCHIVE_NAME}\$" "$CHECKSUMS_FILE" 2>/dev/null | awk '{print $1}')"
        if [ -n "$EXPECTED" ]; then
            if command -v sha256sum >/dev/null 2>&1; then
                ACTUAL="$(sha256sum "$ARCHIVE" | awk '{print $1}')"
            else
                ACTUAL="$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')"
            fi
            [ "$EXPECTED" = "$ACTUAL" ] || error "checksum mismatch for ${ARCHIVE_NAME}: expected $EXPECTED, got $ACTUAL"
            info "Checksum verified."
        else
            warn "no checksum entry found for ${ARCHIVE_NAME}, skipping verification"
        fi
    else
        warn "could not fetch checksums.txt, skipping verification"
    fi
else
    warn "no sha256sum/shasum available, skipping checksum verification"
fi

tar -xzf "$ARCHIVE" -C "$WORKDIR" "$BIN_NAME"

mkdir -p "$INSTALL_DIR"
if command -v install >/dev/null 2>&1; then
    install -m 755 "$WORKDIR/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
else
    cp "$WORKDIR/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
    chmod 755 "$INSTALL_DIR/$BIN_NAME"
fi

info "Installed ${BIN_NAME} to ${INSTALL_DIR}/${BIN_NAME}"

# ---- PATH check ----------------------------------------------------------

case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        info "Run '${BIN_NAME} version' to verify the install."
        ;;
    *)
        warn "${INSTALL_DIR} is not on your PATH yet."
        case "${SHELL:-}" in
            */fish)
                rc="$HOME/.config/fish/config.fish"
                printf '\n  fish_add_path %s\n\n' "$INSTALL_DIR"
                ;;
            */zsh)
                rc="$HOME/.zshrc"
                printf '\n  export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
                ;;
            *)
                if [ "$OS" = "darwin" ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi
                printf '\n  export PATH="%s:$PATH"\n\n' "$INSTALL_DIR"
                ;;
        esac
        info "Add the line above to ${rc}, restart your shell (or run it now), then run '${BIN_NAME} version' to verify."
        ;;
esac
