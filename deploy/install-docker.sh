#!/usr/bin/env bash
set -euo pipefail

script_path="$(readlink -f -- "$0")"
repo_root="$(cd -- "$(dirname -- "$script_path")/.." && pwd)"
if [[ ${EUID} -ne 0 ]]; then
  echo "Запускаю установщик с sudo, чтобы безопасно настроить права папки данных."
  exec sudo -- bash "$script_path" "$@"
fi
cd "$repo_root"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker не найден. Установи Docker Engine и Compose plugin, затем запусти этот скрипт снова." >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Не найден Docker Compose plugin (команда docker compose)." >&2
  exit 1
fi

env_file="$repo_root/.env"
data_dir="$repo_root/data"
game_file="$data_dir/config.env"
mkdir -p "$data_dir/logs"

if [[ ! -f "$env_file" ]]; then
  cp "$repo_root/.env.example" "$env_file"
  chmod 600 "$env_file"
fi
if [[ ! -f "$game_file" ]]; then
  printf 'TARGET_GAME=Hunt: Showdown 1896\n' > "$game_file"
  chmod 600 "$game_file"
fi

get_setting() {
  local file="$1" key="$2" line value
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "$key="* ]]; then
      value="${line#*=}"
      printf '%s' "${value%$'\r'}"
      return 0
    fi
  done < "$file"
  return 0
}

set_setting() {
  local file="$1" key="$2" value="$3" line replaced=0
  local tmp="$file.tmp"
  umask 077
  : > "$tmp"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "$key="* ]]; then
      printf '%s=%s\n' "$key" "$value" >> "$tmp"
      replaced=1
    else
      printf '%s\n' "$line" >> "$tmp"
    fi
  done < "$file"
  if (( replaced == 0 )); then printf '%s=%s\n' "$key" "$value" >> "$tmp"; fi
  chmod 600 "$tmp"
  mv -f -- "$tmp" "$file"
}

# Move editable options out of container environment overrides.
for key in STOP_WHEN_IDLE IDLE_CHECK_ROUNDS DIAGNOSTICS SINGLE_LOG; do
  value="$(get_setting "$env_file" "$key")"
  if [[ -n "$value" ]]; then
    if [[ -z "$(get_setting "$game_file" "$key")" ]]; then set_setting "$game_file" "$key" "$value"; fi
    tmp="$(mktemp "$repo_root/.env-migrate.XXXXXX")"
    while IFS= read -r line || [[ -n "$line" ]]; do
      if [[ "$line" != "$key="* ]]; then printf '%s\n' "$line" >> "$tmp"; fi
    done < "$env_file"
    chmod 600 "$tmp"
    mv -f -- "$tmp" "$env_file"
  fi
done

platform="$(get_setting "$env_file" BOT_PLATFORM)"
if [[ -z "$platform" ]]; then
  if [[ -n "$(get_setting "$env_file" TELEGRAM_BOT_TOKEN)" && -n "$(get_setting "$env_file" TELEGRAM_ADMIN_ID)" ]]; then
    platform=telegram
  else
    read -r -p "Выберите бота: 1 — Telegram, 2 — Discord: " choice </dev/tty
    case "$choice" in
      1|telegram) platform=telegram ;;
      2|discord) platform=discord ;;
      *) echo "Выберите 1 или 2." >&2; exit 1 ;;
    esac
  fi
  set_setting "$env_file" BOT_PLATFORM "$platform"
fi
case "$platform" in
  telegram) token_key=TELEGRAM_BOT_TOKEN; admin_key=TELEGRAM_ADMIN_ID ;;
  discord) token_key=DISCORD_BOT_TOKEN; admin_key=DISCORD_ADMIN_ID ;;
  none) token_key=; admin_key= ;;
  *) echo "BOT_PLATFORM: telegram, discord или none." >&2; exit 1 ;;
esac
if [[ -n "$token_key" ]]; then
  token="$(get_setting "$env_file" "$token_key")"
  if [[ -z "$token" ]]; then
    read -r -s -p "Токен бота $platform (ввод скрыт): " token </dev/tty
    printf '\n'
    if [[ -z "$token" ]]; then echo "Токен обязателен." >&2; exit 1; fi
    set_setting "$env_file" "$token_key" "$token"
  fi
  unset token
  admin_id="$(get_setting "$env_file" "$admin_key")"
  if [[ -z "$admin_id" ]]; then
    read -r -p "Ваш user ID ($platform): " admin_id </dev/tty
    if [[ ! "$admin_id" =~ ^[0-9]+$ || "$admin_id" == 0 ]]; then echo "Нужен числовой user ID." >&2; exit 1; fi
    set_setting "$env_file" "$admin_key" "$admin_id"
  fi
fi

game="$(get_setting "$game_file" TARGET_GAME)"
if [[ -z "$game" ]]; then
  read -r -p "Целевая игра: " game </dev/tty
  if [[ -z "$game" ]]; then echo "Название игры обязательно." >&2; exit 1; fi
  set_setting "$game_file" TARGET_GAME "$game"
else
  read -r -p "Текущая игра: $game. Оставить её? [Y/n]: " answer </dev/tty
  if [[ "$answer" =~ ^([nN]|no|нет|н)$ ]]; then
    read -r -p "Введите новую целевую игру: " game </dev/tty
    if [[ -z "$game" ]]; then echo "Название игры обязательно." >&2; exit 1; fi
    set_setting "$game_file" TARGET_GAME "$game"
  fi
fi

chown -R 10001:10001 "$data_dir"
find "$data_dir" -type d -exec chmod 750 {} +
chmod 600 "$game_file"
chmod 600 "$env_file"

docker compose up -d --build
docker compose ps

echo
echo "Готово. Загрузи .txt через выбранного бота (Discord: /accounts file)."
echo "Логи контейнера: docker compose logs -f tdm-light"
