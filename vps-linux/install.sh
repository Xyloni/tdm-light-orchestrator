#!/usr/bin/env bash
set -euo pipefail

bundle_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
app_dir="/opt/tdm-light"
service_user="tdm-light"

if [[ ${EUID} -ne 0 ]]; then
  echo "Установщик настраивает systemd и /opt/tdm-light; запрашиваю sudo."
  exec sudo -- "$0" "$@"
fi

case "$(uname -m)" in
  x86_64|amd64) binary="tdm-light-linux-amd64" ;;
  aarch64|arm64) binary="tdm-light-linux-arm64" ;;
  *) echo "Неизвестная архитектура сервера: $(uname -m)" >&2; exit 1 ;;
esac

if [[ ! -f "$bundle_dir/$binary" ]]; then
  echo "В комплекте нет бинарного файла $binary." >&2
  exit 1
fi

if ! id "$service_user" >/dev/null 2>&1; then
  useradd --system --home-dir "$app_dir" --shell /usr/sbin/nologin "$service_user"
fi

install -d -o "$service_user" -g "$service_user" -m 0750 "$app_dir"
install -o "$service_user" -g "$service_user" -m 0755 "$bundle_dir/$binary" "$app_dir/tdm-light"

if [[ ! -f "$app_dir/config.json" ]]; then
  install -o "$service_user" -g "$service_user" -m 0600 "$bundle_dir/config.json.example" "$app_dir/config.json"
fi
if [[ ! -f "$app_dir/config.env" ]]; then
  install -o "$service_user" -g "$service_user" -m 0600 "$bundle_dir/config.env.example" "$app_dir/config.env"
fi
if [[ ! -e "$app_dir/account.txt" && -f "$app_dir/accounts.txt" ]]; then
  mv -- "$app_dir/accounts.txt" "$app_dir/account.txt"
fi
if [[ ! -e "$app_dir/account.txt" ]]; then
  install -o "$service_user" -g "$service_user" -m 0600 /dev/null "$app_dir/account.txt"
fi
install -d -o "$service_user" -g "$service_user" -m 0750 "$app_dir/logs"

# The same first-launch wizard is used by the standalone Windows/Linux binary.
(cd "$app_dir" && ./tdm-light --setup)

chown "$service_user:$service_user" "$app_dir/config.json" "$app_dir/config.env" "$app_dir/account.txt"
chmod 0600 "$app_dir/config.json" "$app_dir/config.env" "$app_dir/account.txt"

install -o root -g root -m 0644 "$bundle_dir/tdm-light.service" /etc/systemd/system/tdm-light.service
systemctl daemon-reload
systemctl enable tdm-light.service
if systemctl is-active --quiet tdm-light.service; then
  systemctl restart tdm-light.service
else
  systemctl start tdm-light.service
fi

echo
echo "tdm light установлен и запущен как systemd-служба."
echo "Загрузи любой .txt через выбранного бота (Discord: /accounts file); он сохранит account.txt и перезагрузит майнер."
echo "Логи службы: journalctl -u tdm-light -f"
