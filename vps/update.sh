#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$(readlink -f -- "$0")")"
if (( EUID != 0 )); then exec sudo -- bash "$PWD/update.sh" "$@"; fi

compose() { docker compose -f compose.yaml "$@"; }
set_image() {
  local value="$1" tmp line
  tmp="$(mktemp .env.XXXXXX)"
  chmod 600 "$tmp"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" != TDMLIGHT_IMAGE=* ]]; then printf '%s\n' "$line" >> "$tmp"; fi
  done < .env
  printf 'TDMLIGHT_IMAGE=%s\n' "$value" >> "$tmp"
  mv -f -- "$tmp" .env
}

if [[ ! -f .env ]]; then echo "Run install.sh first." >&2; exit 1; fi
service="$(compose config --services)"
case "$service" in
  tdm-light|orchestrator) ;;
  *) echo "Unknown Compose installation mode." >&2; exit 1 ;;
esac

started() {
  local container state
  container="$(compose ps -a -q "$service")" || return 1
  [[ -n "$container" ]] || return 1
  state="$(docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' "$container")" || return 1
  if [[ "$service" == tdm-light ]]; then
    [[ "$state" == running* || "$state" == "exited 0" ]]
  else
    [[ "$state" == running* ]] &&
      compose exec -T "$service" wget -q -T 2 -O /dev/null http://127.0.0.1:8787/
  fi
}
if [[ "${1:-}" == "--rollback" ]]; then
  if [[ ! -s .previous-image ]]; then echo "No previous image saved." >&2; exit 1; fi
  candidate="$(cat .previous-image)"
else
  candidate="${1:-$(sed -n 's/^TDMLIGHT_IMAGE=//p' .env | tail -n 1)}"
fi
if [[ ! "$candidate" =~ ^ghcr\.io/xyloni/tdm-light-orchestrator(:[a-zA-Z0-9_.-]+|@sha256:[a-f0-9]{64})$ ]]; then
  echo "Use a ghcr.io/xyloni/tdm-light-orchestrator tag or digest." >&2
  exit 1
fi
container="$(compose ps -a -q "$service")"
previous=""
previous_id=""
if [[ -n "$container" ]]; then
  previous_id="$(docker inspect --format '{{.Image}}' "$container")"
  previous="$(docker image inspect --format '{{index .RepoDigests 0}}' "$previous_id" 2>/dev/null || true)"
fi
# Pull before stopping the running miner.
TDMLIGHT_IMAGE="$candidate" compose pull "$service"
next_id="$(docker image inspect --format '{{.Id}}' "$candidate")"
if TDMLIGHT_IMAGE="$candidate" compose up -d --no-deps --no-build --force-recreate "$service"; then
  ready=0
  for (( attempt=0; attempt<5; attempt++ )); do
    sleep 2
    if started; then ready=1; break; fi
  done
  if (( ! ready )); then
    echo "New container failed to start." >&2
  else
    if [[ "$previous" == ghcr.io/xyloni/tdm-light-orchestrator@sha256:* && "$previous_id" != "$next_id" ]]; then
      umask 077
      printf '%s\n' "$previous" > .previous-image
    fi
    set_image "$candidate"
    echo "Updated $service. Persistent data is in ./data."
    exit 0
  fi
fi
if [[ "$previous" == ghcr.io/xyloni/tdm-light-orchestrator@sha256:* ]]; then
  echo "Restoring the previous image..." >&2
  TDMLIGHT_IMAGE="$previous" compose up -d --no-deps --no-build --pull never --force-recreate "$service"
  set_image "$previous"
fi
exit 1
