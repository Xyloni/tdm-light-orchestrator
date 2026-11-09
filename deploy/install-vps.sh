#!/usr/bin/env bash
set -euo pipefail

if (( EUID != 0 )); then exec sudo -- bash "$0" "$@"; fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Install Docker Engine and its Compose plugin before running this installer." >&2
  exit 1
fi
case "${1:-}" in
  "") manifest=compose.yaml; service=tdm-light; default_dir=/opt/tdm-light-orchestrator-miner ;;
  --orchestrator) manifest=compose.orchestrator.yaml; service=orchestrator; default_dir=/opt/tdm-light-orchestrator ;;
  *) echo "Usage: bash install-vps.sh [--orchestrator]" >&2; exit 1 ;;
esac
if (( $# > 1 )); then echo "Usage: bash install-vps.sh [--orchestrator]" >&2; exit 1; fi
destination="${TDMLIGHT_DIR:-$default_dir}"
if [[ "$destination" != /* || "$destination" == "/" || "$destination" == *$'\n'* ]]; then
  echo "TDMLIGHT_DIR must be an absolute installation directory." >&2
  exit 1
fi
if [[ -d "$destination" && ! -f "$destination/compose.yaml" && -n "$(find "$destination" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "Directory contains another installation. Choose a new TDMLIGHT_DIR." >&2
  exit 1
fi
if [[ -f "$destination/compose.yaml" ]]; then
  current="$(cd -- "$destination" && docker compose -f compose.yaml config --services)"
  if [[ "$current" != "$service" ]]; then
    echo "The directory belongs to a different installation mode. Choose a different TDMLIGHT_DIR." >&2
    exit 1
  fi
fi
staging="$(mktemp -d)"
trap 'rm -rf -- "$staging"' EXIT
curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
  https://codeload.github.com/Xyloni/tdm-light-orchestrator/tar.gz/refs/heads/main -o "$staging/source.tar.gz"
tar -xzf "$staging/source.tar.gz" -C "$staging" --strip-components=2 \
  tdm-light-orchestrator-main/vps/"$manifest" tdm-light-orchestrator-main/vps/.env.example \
  tdm-light-orchestrator-main/vps/install.sh tdm-light-orchestrator-main/vps/update.sh tdm-light-orchestrator-main/vps/README.md
install -d -m 750 "$destination"
install -m 644 "$staging/$manifest" "$destination/compose.yaml"
for name in .env.example README.md; do install -m 644 "$staging/$name" "$destination/$name"; done
for name in install.sh update.sh; do install -m 755 "$staging/$name" "$destination/$name"; done
bash "$destination/install.sh"
