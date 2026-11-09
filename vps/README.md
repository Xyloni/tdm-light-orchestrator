# TDM Light on a VPS / TDM Light на VPS

## Orchestrator / Оркестратор

`sudo bash /tmp/tdm-light-install.sh --orchestrator` installs the orchestrator from the same GHCR image in `/opt/tdm-light-orchestrator`. The existing installation command without the flag still starts the standalone miner.

Данные оркестратора находятся в `data/orchestrator`. Из каталога установки используйте `sudo bash update.sh` для обновления и `sudo bash update.sh --rollback` для отката. Для панели откройте SSH-туннель `ssh -L 8787:127.0.0.1:8787 user@vps` и зайдите на `http://127.0.0.1:8787`.


## English

Use a Linux amd64/arm64 VPS with Docker Engine and its Compose plugin installed.
The installer downloads deployment files and a prebuilt image. The initial
setup asks for Telegram/Discord, the bot token, your user ID and target game.

```bash
curl -fsSL https://raw.githubusercontent.com/Xyloni/tdm-light-orchestrator/main/deploy/install-vps.sh -o /tmp/tdm-light-install.sh
sudo bash /tmp/tdm-light-install.sh
```

The default directory is `/opt/tdm-light-orchestrator-miner`. For a different directory, use
`sudo TDMLIGHT_DIR=/your/absolute/path bash /tmp/tdm-light-install.sh`.
On first publication, the GitHub container package must be public for anonymous
downloads. A private package requires `docker login ghcr.io` under the user
running the installer.

Upload any `.txt` account file to the selected bot (Discord uses `/accounts`).
Use `/settings` for automatic shutdown, empty-check count, diagnostics and
single-file logging. Buttons save to `data/config.env`. Apply immediately or
after the current group finishes. Use `/game`, `/exclude` and `/include` for
the game and reward exclusions.

Only the image name and timezone belong in the deployment `.env`. Bot
credentials and editable settings belong in `data/config.env`, so container
environment overrides do not block bot changes. Back up `data` privately.

```bash
cd /opt/tdm-light-orchestrator-miner
sudo bash update.sh
sudo docker compose -f compose.yaml logs -f tdm-light
sudo docker compose -f compose.yaml stop
sudo docker compose -f compose.yaml start
```

Updates are manual. `update.sh` pulls the selected image before replacing the
container, preserves `data`, and checks basic container startup. If startup
fails, it attempts to restore the previous image. This check verifies process
startup, not Twitch accrual or bot authentication.

To pin a successful build, run `sudo bash update.sh
ghcr.io/xyloni/tdm-light-orchestrator:sha-COMMIT_SHA` on one line with the actual commit.
To roll back the last successful update, run `sudo bash update.sh --rollback`.
To resume following successful development builds, run
`sudo bash update.sh ghcr.io/xyloni/tdm-light-orchestrator:latest`.

With `STOP_WHEN_IDLE=true`, the process sends its final bot message and exits
after the configured empty checks. The bot exits too. Use the SSH `start`
command above to run it again. The container restarts on failures.
The combined log is `data/logs/tdm-light.log`.

## Русский

Нужен Linux VPS с архитектурой amd64/arm64, Docker Engine и плагином Compose.
Установщик скачивает файлы запуска и готовый образ. При первом запуске он
спросит Telegram или Discord, токен бота, ваш user ID и целевую игру.

```bash
curl -fsSL https://raw.githubusercontent.com/Xyloni/tdm-light-orchestrator/main/deploy/install-vps.sh -o /tmp/tdm-light-install.sh
sudo bash /tmp/tdm-light-install.sh
```

Каталог по умолчанию — `/opt/tdm-light-orchestrator-miner`. Другой путь можно задать командой
`sudo TDMLIGHT_DIR=/полный/путь bash /tmp/tdm-light-install.sh`.
После первой публикации пакет контейнера GitHub нужно сделать публичным для
скачивания без авторизации. Для приватного пакета нужен `docker login ghcr.io`
от пользователя, запускающего установщик.

Загрузите любой `.txt` через выбранного бота (в Discord — через `/accounts`).
Команда `/settings` открывает автовыключение, число пустых проверок, диагностику
и общий лог. Кнопки сохраняют значения в `data/config.env`. Применить изменения
можно сразу или после завершения текущей группы. Игра меняется через `/game`,
исключения наград — через `/exclude` и `/include`.

В установочном `.env` хранятся имя образа и часовой пояс. Токен, ID владельца
и редактируемые настройки хранятся в `data/config.env`. Это позволяет боту
изменять настройки без перекрытия переменными окружения контейнера.
Резервную копию `data` храните приватно.

```bash
cd /opt/tdm-light-orchestrator-miner
sudo bash update.sh
sudo docker compose -f compose.yaml logs -f tdm-light
sudo docker compose -f compose.yaml stop
sudo docker compose -f compose.yaml start
```

Обновления запускаются вручную. Скрипт сначала скачивает выбранный образ, затем
заменяет контейнер, сохраняя `data`, и проверяет запуск процесса. При неудаче
он пытается вернуть прежний образ. Проверка запуска не измеряет начисление
Twitch и не проверяет авторизацию бота.

Закрепить успешную сборку можно командой `sudo bash update.sh
ghcr.io/xyloni/tdm-light-orchestrator:sha-COMMIT_SHA` в одну строку с реальным SHA коммита.
Откат последнего успешного обновления — `sudo bash update.sh --rollback`.
Вернуться на свежие успешные сборки — `sudo bash update.sh
ghcr.io/xyloni/tdm-light-orchestrator:latest`.

При `STOP_WHEN_IDLE=true` процесс отправляет итоговое сообщение и завершается
после заданного числа пустых проверок. Бот также выключается. Для повторного
запуска используйте команду `start` по SSH из примера выше.
При ошибках контейнер перезапускается. Общий лог — `data/logs/tdm-light.log`.
