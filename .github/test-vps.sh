#!/usr/bin/env bash
# Exercise installer/update file handling without a Docker daemon or credentials.
set -euo pipefail
if (( EUID != 0 )); then echo "Run this isolated test as root." >&2; exit 1; fi
root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
test_dir="$(mktemp -d)"
[[ "$test_dir" == /tmp/* ]] || exit 1
trap 'rm -rf -- "$test_dir"' EXIT
mkdir -p "$test_dir/bin" "$test_dir/app/data"
cp "$root"/vps/{compose.yaml,.env.example,install.sh,update.sh} "$test_dir/app/"
export MOCK_LOG="$test_dir/commands" MOCK_STATE="$test_dir/state"
export PATH="$test_dir/bin:$PATH"
cat > "$test_dir/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf '%s | image=%s\n' "$*" "${TDMLIGHT_IMAGE:-}" >> "$MOCK_LOG"
if [[ "$1" == info ]]; then exit 0; fi
if [[ "$1" == inspect ]]; then
  if [[ "$3" == '{{.Image}}' ]]; then echo old-id; else cat "$MOCK_STATE"; fi
  exit 0
fi
if [[ "$1" == image ]]; then
  if [[ "$4" == '{{.Id}}' ]]; then echo new-id
  else printf 'ghcr.io/xyloni/tdm-light-orchestrator@sha256:%064d\n' 1
  fi
  exit 0
fi
if [[ "$1" == compose ]]; then
  shift
  if [[ "$1" == version ]]; then exit 0; fi
  shift 2
  case "$1" in
    pull) if [[ "${MOCK_PULL_FAIL:-0}" == 1 ]]; then exit 1; fi ;;
    up)
      if [[ "${TDMLIGHT_IMAGE:-}" == *:broken ]]; then echo 'exited 1' > "$MOCK_STATE"
      else echo 'running 0' > "$MOCK_STATE"; fi ;;
    ps) echo fixture-container ;;
    config) if grep -q '^  orchestrator:' compose.yaml; then echo orchestrator; else echo tdm-light; fi ;;
    exec) [[ "$2" == "-T" && "$3" == orchestrator ]] ;;
    *) echo "Unexpected compose operation" >&2; exit 1 ;;
  esac
  exit 0
fi
exit 1
MOCK
chmod +x "$test_dir/bin/docker"
cat > "$test_dir/app/data/config.env" <<'CONFIG'
BOT_PLATFORM=telegram
TELEGRAM_BOT_TOKEN=fixture
TELEGRAM_ADMIN_ID=1
TARGET_GAME=Game
STOP_WHEN_IDLE=true
IDLE_CHECK_ROUNDS=4
CONFIG
printf 'fixture-account\n' > "$test_dir/app/data/account.txt"
before_config="$(sha256sum "$test_dir/app/data/config.env")"
before_accounts="$(sha256sum "$test_dir/app/data/account.txt")"
bash "$test_dir/app/install.sh"
bash "$test_dir/app/install.sh"
[[ "$before_config" == "$(sha256sum "$test_dir/app/data/config.env")" ]]
[[ "$before_accounts" == "$(sha256sum "$test_dir/app/data/account.txt")" ]]
[[ "$(stat -c '%u:%a' "$test_dir/app/data/config.env")" == 10001:600 ]]
! grep -q -- --build "$MOCK_LOG"
bash "$test_dir/app/update.sh"
[[ -s "$test_dir/app/.previous-image" ]]
if bash "$test_dir/app/update.sh" ghcr.io/xyloni/tdm-light-orchestrator:broken; then
  echo "Broken container accepted" >&2; exit 1
fi
grep -q '^TDMLIGHT_IMAGE=ghcr.io/xyloni/tdm-light-orchestrator@sha256:' "$test_dir/app/.env"
[[ "$(cat "$MOCK_STATE")" == 'running 0' ]]
before_up="$(grep -c ' up ' "$MOCK_LOG")"
if MOCK_PULL_FAIL=1 bash "$test_dir/app/update.sh" ghcr.io/xyloni/tdm-light-orchestrator:latest; then
  echo "Failed pull accepted" >&2; exit 1
fi
[[ "$before_up" == "$(grep -c ' up ' "$MOCK_LOG")" ]]
[[ "$before_config" == "$(sha256sum "$test_dir/app/data/config.env")" ]]
[[ "$before_accounts" == "$(sha256sum "$test_dir/app/data/account.txt")" ]]
mkdir -p "$test_dir/orch/data/orchestrator"
cp "$root"/vps/{.env.example,install.sh,update.sh} "$test_dir/orch/"
cp "$root/vps/compose.orchestrator.yaml" "$test_dir/orch/compose.yaml"
printf '{"fixture":true}\n' > "$test_dir/orch/data/orchestrator/state.json"
before_state="$(sha256sum "$test_dir/orch/data/orchestrator/state.json")"
bash "$test_dir/orch/install.sh"
[[ "$(stat -c '%u:%a' "$test_dir/orch/data/orchestrator")" == 10001:700 ]]
[[ ! -e "$test_dir/orch/data/config.env" ]]
bash "$test_dir/orch/update.sh"
grep -q 'exec -T orchestrator wget' "$MOCK_LOG"
if bash "$test_dir/orch/update.sh" ghcr.io/xyloni/tdm-light-orchestrator:broken; then
  echo "Broken orchestrator accepted" >&2; exit 1
fi
[[ "$before_state" == "$(sha256sum "$test_dir/orch/data/orchestrator/state.json")" ]]
[[ "$(cat "$MOCK_STATE")" == 'running 0' ]]
echo "VPS installer/update preservation and rollback checks passed for both modes."
