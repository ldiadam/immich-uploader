#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="${HOME}/.local/bin"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TUI_SRC="${ROOT_DIR}/immich-uploader-linux-amd64"
COPY_BINS=1

usage() {
  cat <<USAGE
Usage: $(basename "$0") [options]

Set up a user-local PATH entry on Linux and optionally install binaries.

Options:
  --install-dir <dir>   Target bin directory (default: ~/.local/bin)
  --tui <path>          Path to TUI binary (default: ./immich-uploader-tui)
  --no-copy             Only configure PATH; do not copy binaries
  -h, --help            Show this help

Examples:
  ./scripts/install-linux-path.sh
  ./scripts/install-linux-path.sh --no-copy
  ./scripts/install-linux-path.sh --tui ./bin/immich-uploader-tui
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)
      INSTALL_DIR="$2"
      shift 2
      ;;
    --tui)
      TUI_SRC="$2"
      shift 2
      ;;
    --no-copy)
      COPY_BINS=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 2
      ;;
  esac
done

mkdir -p "$INSTALL_DIR"

add_path_line() {
  local rc_file="$1"
  local line='export PATH="$HOME/.local/bin:$PATH"'

  # Respect custom install dir if not default.
  if [[ "$INSTALL_DIR" != "$HOME/.local/bin" ]]; then
    line="export PATH=\"${INSTALL_DIR}:\$PATH\""
  fi

  touch "$rc_file"
  if ! grep -Fq "$line" "$rc_file"; then
    printf '\n# Added by immich-uploader installer\n%s\n' "$line" >> "$rc_file"
    echo "Updated $rc_file"
  fi
}

if [[ -f "$HOME/.bashrc" ]]; then
  add_path_line "$HOME/.bashrc"
fi
if [[ -f "$HOME/.zshrc" ]]; then
  add_path_line "$HOME/.zshrc"
fi
if [[ ! -f "$HOME/.bashrc" && ! -f "$HOME/.zshrc" ]]; then
  add_path_line "$HOME/.profile"
fi

if [[ "$COPY_BINS" -eq 1 ]]; then
  if [[ -f "$TUI_SRC" ]]; then
    install -m 0755 "$TUI_SRC" "$INSTALL_DIR/immich-uploader"
    install -m 0755 "$TUI_SRC" "$INSTALL_DIR/immich-uploader-tui"
    echo "Installed app -> $INSTALL_DIR/immich-uploader"
    echo "Installed TUI -> $INSTALL_DIR/immich-uploader-tui"
  else
    echo "TUI binary not found at: $TUI_SRC"
    echo "Build it first: go build -o immich-uploader-tui ./cmd/tui"
  fi
fi

if [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
  export PATH="${INSTALL_DIR}:$PATH"
fi

echo
echo "Done. PATH is configured for future shells."
echo "For this terminal, run:"
echo "  source ~/.bashrc  # or source ~/.zshrc"
echo
echo "Then verify:"
echo "  command -v immich-uploader"
echo "  command -v immich-uploader-tui"
