#!/usr/bin/env bash
set -euo pipefail

if [[ -f Dockerfile && -f docker-compose.yml ]]; then
  exec bash ./deploy/install-docker.sh
fi

if [[ ! -e /dev/tty ]]; then
  echo "Нужен интерактивный терминал для ввода адреса Git-репозитория." >&2
  exit 1
fi
repository="https://github.com/Xyloni/tdm-light-orchestrator.git"
destination="${TDMLIGHT_DIR:-/opt/tdm-light}"
if [[ -e "$destination" ]]; then
  echo "Каталог уже существует: $destination" >&2
  exit 1
fi
git clone "$repository" "$destination"
cd "$destination"
exec bash ./deploy/install-docker.sh
