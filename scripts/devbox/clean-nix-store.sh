#!/usr/bin/env bash
# clean-nix-store.sh
#
# Remove all devbox/project packages from the Nix store while preserving
# the Nix daemon and its runtime dependencies.  After running, only the
# Nix tooling itself remains in /nix/store — everything installed by
# devbox is gone.
#
# Usage:
#   ./scripts/devbox/clean-nix-store.sh
#
set -Eeou pipefail

# --- Ensure Nix is on PATH ---
if ! command -v nix-collect-garbage &>/dev/null; then
    if [[ -f "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh" ]]; then
        # shellcheck disable=SC1091
        . "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    fi
fi

if ! command -v nix-collect-garbage &>/dev/null; then
    echo "ERROR: Nix is not installed or not on PATH."
    echo "  Run ./scripts/devbox/install-devbox.sh first."
    exit 1
fi

# Resolve absolute path now — we delete profiles that put it on PATH
# later, so the command would become unavailable by name.
NIX_COLLECT_GARBAGE="$(command -v nix-collect-garbage)"

echo "=== Nix Store Cleanup ==="
echo ""

# --- Show current store size ---
echo "Store size before cleanup:"
du -sh /nix/store 2>/dev/null || echo "  (could not determine size)"
echo ""

# --- 1. Remove devbox project profiles ---
# Each project's .devbox/nix/profile/ directory contains generation
# symlinks that act as GC roots via /nix/var/nix/gcroots/auto/.
echo "Removing devbox project profiles under \$HOME..."
# shellcheck disable=SC2044
for dir in $(find "$HOME" -maxdepth 5 -path '*/.devbox/nix/profile' -type d 2>/dev/null); do
    echo "  removing $dir"
    rm -rf "$dir"
done
echo ""

# --- 2. Remove devbox global profile ---
if [[ -d "$HOME/.local/share/devbox/global" ]]; then
    echo "Removing devbox global profile..."
    rm -rf "$HOME/.local/share/devbox/global"
fi

# --- 3. Remove devbox Nix cache (downloaded binaries, etc.) ---
if [[ -d "$HOME/.cache/devbox/nix" ]]; then
    echo "Removing devbox Nix cache..."
    rm -rf "$HOME/.cache/devbox/nix"
fi
echo ""

# --- 4. Remove per-user GC-root auto symlinks ---
# These are indirect-root symlinks the daemon creates to track devbox
# profiles.  Many now point at the profiles we just deleted, so they
# are dangling.  Remove them so nothing pins devbox store paths.
# We intentionally leave /nix/var/nix/gcroots/ structure intact.
echo "Clearing user GC-root auto symlinks..."
sudo rm -f /nix/var/nix/gcroots/auto/* 2>/dev/null || true
sudo find /nix/var/nix/gcroots/per-user -mindepth 1 -delete 2>/dev/null || true
echo ""

# --- 5. Remove per-user Nix profiles (but keep /nix/var/nix/profiles/default) ---
echo "Removing per-user Nix profiles..."
sudo rm -rf /nix/var/nix/profiles/per-user/*/profile* 2>/dev/null || true
echo ""

# --- 6. Remove flake registry cache ---
echo "Removing flake registry cache..."
rm -rf "$HOME/.cache/nix" 2>/dev/null || true
echo ""

# --- 7. Garbage-collect ---
echo "Running nix-collect-garbage -d ..."
echo "  This may take a few minutes."
echo ""
"$NIX_COLLECT_GARBAGE" -d
echo ""

# --- Show resulting store size ---
echo "Store size after cleanup:"
du -sh /nix/store 2>/dev/null || echo "  (could not determine size)"
echo ""
echo "=== Cleanup complete ==="
echo ""
echo "Note: run 'devbox install' in each project to re-fetch needed packages."
