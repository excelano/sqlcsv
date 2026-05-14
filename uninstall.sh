#!/bin/sh
# sqlcsv uninstaller — finds and removes the sqlcsv binary, with an
# optional follow-up step to remove the REPL history at
# ~/.config/sqlcsv/. POSIX sh, no bash extensions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/excelano/sqlcsv/main/uninstall.sh | sh
#
# Environment variables:
#   SQLCSV_UNINSTALL_YES=1  Skip the interactive confirmation (assume yes)
#   SQLCSV_PURGE=1          Also remove ~/.config/sqlcsv/ (history, etc.)

set -eu

say() { printf '%s\n' "$*" >&2; }
err() { say "error: $*"; exit 1; }

# read_yes reads a y/N answer from the controlling terminal, not stdin,
# because this script is typically invoked as `curl ... | sh` where stdin
# is the script itself.
read_yes() {
	prompt="$1"
	if [ "${SQLCSV_UNINSTALL_YES:-0}" = "1" ]; then
		return 0
	fi
	if [ ! -t 0 ] && [ ! -e /dev/tty ]; then
		err "no terminal available for confirmation; re-run with SQLCSV_UNINSTALL_YES=1 to skip the prompt"
	fi
	printf '%s [y/N]: ' "$prompt" >&2
	if [ -e /dev/tty ]; then
		read ans </dev/tty
	else
		read ans
	fi
	case "$ans" in
		y|Y|yes|YES) return 0 ;;
		*) return 1 ;;
	esac
}

if ! command -v sqlcsv >/dev/null 2>&1; then
	say "sqlcsv is not on PATH; nothing to uninstall."
	say "If you installed to a custom location, remove it manually:"
	say "    rm /path/to/sqlcsv"
	exit 0
fi

TARGET=$(command -v sqlcsv)
say "Found sqlcsv at $TARGET"

if [ ! -w "$TARGET" ] && [ ! -w "$(dirname "$TARGET")" ]; then
	err "$TARGET is not writable; re-run with sudo to remove it"
fi

if ! read_yes "Remove $TARGET?"; then
	say "Aborted."
	exit 1
fi

rm -f "$TARGET" || err "could not remove $TARGET"
say "Removed $TARGET"

# Invalidate the shell's command hash; without this, `command -v` happily
# reports the just-deleted path as still present and the duplicate-install
# check below cries wolf.
hash -r 2>/dev/null || true

# Check for additional installs (e.g. one in /usr/local/bin plus one in
# ~/.local/bin). PATH lookup only finds the first; warn so the user knows
# the others remain.
LEFTOVER=$(command -v sqlcsv 2>/dev/null || true)
if [ -n "$LEFTOVER" ]; then
	say ""
	say "Note: another sqlcsv binary is still on PATH at $LEFTOVER"
	say "Re-run this uninstaller to remove it, or remove it manually."
fi

# Optional state cleanup. We only remove ~/.config/sqlcsv/ contents if
# SQLCSV_PURGE=1 was passed, since some users like to keep their history.
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/sqlcsv"
if [ -d "$CONFIG_DIR" ]; then
	if [ "${SQLCSV_PURGE:-0}" = "1" ] || read_yes "Also remove $CONFIG_DIR (history)?"; then
		rm -rf "$CONFIG_DIR"
		say "Removed $CONFIG_DIR"
	else
		say "Kept $CONFIG_DIR (history)"
	fi
fi

say ""
say "Done."
