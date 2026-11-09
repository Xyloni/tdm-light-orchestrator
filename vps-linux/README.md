# TDM Light для Linux VPS

Архив `tdm-light-vps-ubuntu.tar.gz` содержит готовые бинарники Linux amd64 и arm64, Docker Compose для двух режимов и установщик самостоятельного майнера через systemd. Скрипт `build-vps.ps1` создаёт архив на Windows. Для установки на VPS Go не нужен.

## Docker Compose

На Linux VPS с Docker Engine и Compose распакуйте архив и перейдите в каталог комплекта.

```sh
tar -xzf tdm-light-vps-ubuntu.tar.gz
cd vps-linux
```

### Оркестратор для нескольких игр

```sh
sudo install -d -m 0700 -o 10001 -g 10001 data-orchestrator
sudo docker compose -f docker-compose.orchestrator.yml up -d --build
sudo docker compose -f docker-compose.orchestrator.yml logs -f orchestrator
```

Панель слушает `127.0.0.1:8787` на VPS. С ПК откройте туннель `ssh -L 8787:127.0.0.1:8787 user@vps`, затем зайдите на `http://127.0.0.1:8787`. Подключите бота в панели или перенесите существующее состояние перед первым стартом.

Для переноса остановите оркестратор на ПК и на VPS. Из `orchestrator-data` перенесите `state.json` в `vps-linux/data-orchestrator/orchestrator/state.json` на VPS. Назначьте каталогу и файлу владельца `10001:10001`, права `0700` для каталога и `0600` для файла. Состояние содержит токены, пулы, настройки и статистику. Запустите сервис после переноса. Подробности есть в [руководстве оркестратора](ORCHESTRATOR.md).

### Самостоятельный майнер для одной игры

```sh
sudo install -d -m 0700 -o 10001 -g 10001 data
test -e .env || (umask 077; touch .env)
sudo docker compose -f docker-compose.yml run --rm --build tdm-light --setup
sudo docker compose -f docker-compose.yml up -d
sudo docker compose -f docker-compose.yml logs -f tdm-light
```

Мастер сохранит целевую игру и настройки бота в `data/config.env`. Пустой `.env` оставляет настройки мастеру, а заданные в нём переменные перекрывают файл настроек. Аккаунты можно загрузить через бота или разместить в `data/account.txt` с владельцем `10001:10001` и правами `0600`. Если оба режима запущены одновременно, используйте отдельные токены ботов и непересекающиеся пулы Twitch.

## Standalone systemd installer for Ubuntu Server

This standalone bundle targets Ubuntu Server 26.04.1 on `amd64` and `arm64`. Run `build-vps.ps1` on Windows to produce `tdm-light-vps-ubuntu.tar.gz` with the binaries and configuration templates. The archive is excluded from Git. Go does not need to be installed on the VPS.

## Install

1. Copy `tdm-light-vps-ubuntu.tar.gz` to the server via SCP or SFTP and extract it.
2. Connect over SSH and enter that folder.
3. Run:

   ```bash
   chmod +x install.sh
   sudo ./install.sh
   ```

The first-launch wizard offers Telegram or Discord, then asks for the selected bot token (hidden input), administrator user ID, and the target game if missing. Existing settings are kept. It creates the restricted `tdm-light` system user and starts `tdm-light.service`.

The service starts with an empty account list and waits for a bot upload. Send any `.txt` document (up to 2 MiB) to Telegram, or use `/accounts file` in Discord. It validates and saves the list as `/opt/tdm-light/account.txt`, then reloads the miner. An existing legacy `accounts.txt` is migrated when the new filename is absent.

The miner accumulates watch time and leaves claiming rewards to the user. `/status` includes crediting speed and lagging accounts after at least ten minutes of observations; `/logs <login>` shows confirmed Inventory minutes. HLS runs every 20 seconds, beacon events every minute, and Inventory polling every two minutes by default. Persistently slow crediting triggers channel recovery with a 10–12 minute cooldown.

## Operations

```bash
sudo systemctl status tdm-light
sudo journalctl -u tdm-light -f
sudo systemctl restart tdm-light
```

The bot supports `/status`, `/logs <login>`, `/game`, `/game <name>`, `/restart`, reward notifications, `/exclude [name]`, `/include name`, and uploading `account.txt`. `/game <name>` changes the target and reloads the miner. You can also edit `TARGET_GAME` in `/opt/tdm-light/config.env` and use `/restart`.

The account list and bot credentials are stored on the server with owner-only file permissions. Keep SSH access restricted to trusted administrators. See the main README for Discord app installation and direct messages.

---

# tdm light для Ubuntu Server

Этот отдельный комплект предназначен для Ubuntu Server 26.04.1 на `amd64` и `arm64`. Запусти `build-vps.ps1` из корня репозитория в Windows: он создаст `tdm-light-vps-ubuntu.tar.gz` с бинарниками и шаблонами настроек. Архив исключён из Git. Устанавливать Go на VPS не нужно.

## Установка

1. Скопируй `tdm-light-vps-ubuntu.tar.gz` на сервер через SCP или SFTP и распакуй.
2. Подключись по SSH и перейди в эту папку.
3. Выполни:

   ```bash
   chmod +x install.sh
   sudo ./install.sh
   ```

Мастер первого запуска предложит Telegram или Discord, запросит токен выбранного бота (ввод скрыт), user ID администратора и целевую игру, если она не задана. Существующие настройки сохраняются. Установщик создаст системного пользователя `tdm-light` с ограниченными правами и запустит службу `tdm-light.service`.

Сначала служба запускается с пустым списком аккаунтов и ждёт загрузки через бота. Отправь любой `.txt` документом в Telegram (до 2 МиБ) или используй `/accounts file` в Discord. Бот проверит файл, сохранит его как `/opt/tdm-light/account.txt` и перезапустит майнер. Старый `accounts.txt` переносится, если нового файла ещё нет.

Программа набирает время просмотра, получение наград выполняется вручную. `/status` показывает скорость начисления и отстающие аккаунты после минимум десяти минут наблюдения; `/logs <логин>` — подтверждённые минуты Inventory. HLS проверяется каждые 20 секунд, beacon отправляется раз в минуту, Inventory опрашивается раз в две минуты по умолчанию. При устойчивом недоборе программа восстанавливает просмотр с паузой 10–12 минут между попытками.

## Управление

```bash
sudo systemctl status tdm-light
sudo journalctl -u tdm-light -f
sudo systemctl restart tdm-light
```

Бот поддерживает `/status`, `/logs <логин>`, `/game`, `/game <название>`, `/restart`, уведомления о наградах, `/exclude [название]`, `/include название` и загрузку `account.txt`. Команда `/game <название>` меняет игру и перезапускает майнер. Можно также изменить `TARGET_GAME` в `/opt/tdm-light/config.env` и выполнить `/restart`.

Список аккаунтов и настройки бота хранятся на сервере с доступом только владельца файла. Ограничь SSH-доступ доверенными администраторами. Настройка приложения Discord и личных сообщений описана в основном README.


Test logging / Логи теста: set / укажите `DIAGNOSTICS=true` and / и `SINGLE_LOG=true` in / в `config.env`, then restart / затем перезапустите службу. All accounts share / Все аккаунты пишут в `LOG_DIR/tdm-light.log` (default / по умолчанию `logs/tdm-light.log`). This file is appended without rotation / Файл дописывается без ротации. Disable diagnostics after testing / После теста отключите `DIAGNOSTICS`. Stop the service before archiving the full log / Перед архивацией полного лога остановите службу.
