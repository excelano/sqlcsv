#!/bin/sh
# sqlcsv installer — fetches the latest release binary for the host platform
# and drops it into /usr/local/bin (or ~/.local/bin if /usr/local/bin is not
# writable). POSIX sh, no bash extensions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/install.sh | sh
#
# Environment variables:
#   SQLCSV_INSTALL_DIR   Override install directory (e.g. /opt/bin or $HOME/bin)
#   SQLCSV_VERSION       Install a specific release tag (e.g. v0.1.0) instead of latest

set -eu

REPO="excelano/sqlcsv"

say() { printf '%s\n' "$*" >&2; }
err() { say "error: $*"; exit 1; }

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		err "this installer needs '$1' on PATH; please install it and re-run"
	fi
}

need_cmd curl
need_cmd tar
need_cmd uname

detect_platform() {
	OS=$(uname -s | tr '[:upper:]' '[:lower:]')
	ARCH=$(uname -m)
	case "$OS" in
		linux|darwin) ;;
		*) err "unsupported OS: $OS (sqlcsv ships linux + darwin binaries)";;
	esac
	case "$ARCH" in
		x86_64|amd64) ARCH=amd64 ;;
		aarch64|arm64) ARCH=arm64 ;;
		*) err "unsupported architecture: $ARCH";;
	esac
	PLATFORM="${OS}_${ARCH}"
}

resolve_version() {
	if [ -n "${SQLCSV_VERSION:-}" ]; then
		VERSION="$SQLCSV_VERSION"
		say "Installing sqlcsv $VERSION (pinned via SQLCSV_VERSION)"
		return
	fi
	# Resolve the latest tag without a token: the redirect on /releases/latest
	# exposes it in the Location header.
	VERSION=$(curl -fsSI "https://github.com/${REPO}/releases/latest" \
		| awk -F'/' '/^[Ll]ocation:/ { sub(/\r$/, "", $NF); print $NF; exit }')
	if [ -z "${VERSION:-}" ]; then
		err "could not resolve latest release tag from GitHub"
	fi
	say "Installing sqlcsv $VERSION (latest)"
}

pick_install_dir() {
	if [ -n "${SQLCSV_INSTALL_DIR:-}" ]; then
		INSTALL_DIR="$SQLCSV_INSTALL_DIR"
	elif [ -w /usr/local/bin ] 2>/dev/null; then
		INSTALL_DIR=/usr/local/bin
	else
		# /usr/local/bin needs root; fall back to a user-writable spot.
		INSTALL_DIR="$HOME/.local/bin"
	fi
	mkdir -p "$INSTALL_DIR" || err "cannot create install dir $INSTALL_DIR"
}

download_and_install() {
	VERSION_NUM=${VERSION#v}
	ARCHIVE="sqlcsv_${VERSION_NUM}_${PLATFORM}.tar.gz"
	URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
	CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

	TMPDIR=$(mktemp -d)
	trap 'rm -rf "$TMPDIR"' EXIT INT TERM

	say "Downloading $ARCHIVE"
	if ! curl -fsSL -o "$TMPDIR/$ARCHIVE" "$URL"; then
		err "download failed: $URL"
	fi

	say "Verifying checksum"
	if ! curl -fsSL -o "$TMPDIR/checksums.txt" "$CHECKSUMS_URL"; then
		err "could not fetch checksums.txt from release"
	fi
	EXPECTED=$(awk -v a="$ARCHIVE" '$2==a {print $1}' "$TMPDIR/checksums.txt")
	if [ -z "$EXPECTED" ]; then
		err "checksums.txt has no entry for $ARCHIVE"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		ACTUAL=$(sha256sum "$TMPDIR/$ARCHIVE" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" | awk '{print $1}')
	else
		err "need sha256sum or shasum to verify the download"
	fi
	if [ "$EXPECTED" != "$ACTUAL" ]; then
		err "checksum mismatch: expected $EXPECTED, got $ACTUAL"
	fi

	say "Extracting to $INSTALL_DIR"
	tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR" sqlcsv
	# install(1) handles permissions and atomicity better than mv on most systems.
	if command -v install >/dev/null 2>&1; then
		install -m 0755 "$TMPDIR/sqlcsv" "$INSTALL_DIR/sqlcsv"
	else
		mv "$TMPDIR/sqlcsv" "$INSTALL_DIR/sqlcsv"
		chmod 0755 "$INSTALL_DIR/sqlcsv"
	fi
}

post_install_message() {
	say ""
	say "sqlcsv installed to $INSTALL_DIR/sqlcsv"
	case ":$PATH:" in
		*":$INSTALL_DIR:"*) ;;
		*) say "Note: $INSTALL_DIR is not on your PATH. Add it to your shell rc:"
		   say "    export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
	esac
	say ""
	say "Try it:"
	say "    sqlcsv --help"
}

detect_platform
resolve_version
pick_install_dir
download_and_install
post_install_message
