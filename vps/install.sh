#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "$(readlink -f -- "$0")")"
if (( EUID != 0 )); then
  exec sudo -- bash "$PWD/install.sh" "$@"
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Install Docker Engine and its Compose plugin, then run this installer again." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon is unavailable." >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64|aarch64|amd64|arm64) ;;
  *) echo "Supported architectures: amd64, arm64." >&2; exit 1 ;;
esac

service="$(docker compose -f compose.yaml config --services)"
case "$service" in
  tdm-light|orchestrator) ;;
  *) echo "Unknown Compose installation mode." >&2; exit 1 ;;
esac

umask 077
if [[ ! -f .env ]]; then cp .env.example .env; fi
chmod 600 .env
if [[ "$service" == orchestrator ]]; then
  install -d -m 0700 -o 10001 -g 10001 data data/orchestrator
  docker compose -f compose.yaml pull orchestrator
  docker compose -f compose.yaml up -d --no-build --wait --wait-timeout 40 orchestrator
  echo "Панель оркестратора: http://127.0.0.1:8787 на VPS. Для доступа используйте SSH-туннель."
  echo "Данные: ./data/orchestrator. Логи: docker compose logs -f orchestrator"
  exit 0
fi

mkdir -p data/logs
touch data/config.env
chmod 600 data/config.env

get_setting() {
  local key="$1" line
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "$key="* ]]; then
      printf '%s' "${line#*=}"
      return
    fi
  done < data/config.env
}

set_setting() {
  local key="$1" value="$2" tmp line found=0
  tmp="$(mktemp data/.settings.XXXXXX)"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "$key="* ]]; then
      printf '%s=%s\n' "$key" "$value" >> "$tmp"
      found=1
    else
      printf '%s\n' "$line" >> "$tmp"
    fi
  done < data/config.env
  if (( found == 0 )); then printf '%s=%s\n' "$key" "$value" >> "$tmp"; fi
  chmod 600 "$tmp"
  chown 10001:10001 "$tmp"
  mv -f -- "$tmp" data/config.env
}

platform="$(get_setting BOT_PLATFORM)"
if [[ -z "$platform" ]]; then
  read -r -p "Choose bot / Выберите бота: 1 Telegram, 2 Discord: " choice </dev/tty
  case "$choice" in
    1|telegram) platform=telegram ;;
    2|discord) platform=discord ;;
    *) echo "Choose 1 or 2." >&2; exit 1 ;;
  esac
  set_setting BOT_PLATFORM "$platform"
fi
case "$platform" in
  telegram) token_key=TELEGRAM_BOT_TOKEN; admin_key=TELEGRAM_ADMIN_ID ;;
  discord) token_key=DISCORD_BOT_TOKEN; admin_key=DISCORD_ADMIN_ID ;;
  *) echo "BOT_PLATFORM must be telegram or discord." >&2; exit 1 ;;
esac
if [[ -z "$(get_setting "$token_key")" ]]; then
  read -r -s -p "Bot token / Токен бота (hidden): " bot_token </dev/tty
  printf '\n'
  if [[ -z "$bot_token" || "$bot_token" == *$'\r'* ]]; then
    echo "A nonempty token is required." >&2
    exit 1
  fi
  set_setting "$token_key" "$bot_token"
  unset bot_token
fi
admin_id="$(get_setting "$admin_key")"
if [[ -z "$admin_id" ]]; then
  read -r -p "Your user ID / Ваш user ID: " admin_id </dev/tty
  if [[ ! "$admin_id" =~ ^[1-9][0-9]*$ ]]; then echo "A positive numeric user ID is required." >&2; exit 1; fi
  set_setting "$admin_key" "$admin_id"
fi
if [[ -z "$(get_setting TARGET_GAME)" ]]; then
  read -r -p "Game / Игра [Hunt: Showdown 1896]: " target_game </dev/tty
  set_setting TARGET_GAME "${target_game:-Hunt: Showdown 1896}"
fi
if [[ ! -e data/account.txt && -f data/accounts.txt ]]; then mv -- data/accounts.txt data/account.txt; fi
if [[ ! -e data/account.txt ]]; then touch data/account.txt; fi
chown -R 10001:10001 data
find data -type d -exec chmod 750 {} +
chmod 600 data/config.env data/account.txt

docker compose -f compose.yaml pull tdm-light
docker compose -f compose.yaml up -d --no-build tdm-light
echo "Started. Upload account.txt through the bot, then use /settings."
echo "Запущено. Загрузите аккаунты через бота. Настройки доступны в /settings."
echo "Logs: docker compose -f compose.yaml logs -f tdm-light"
