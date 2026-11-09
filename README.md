<div align="center">

# TDM Light Orchestrator

Run Twitch Drops campaigns across several games from one panel.

[English](#english) · [Русский](#русский)

Linux VPS · Docker Compose · Windows · Telegram · Discord

</div>

## English

TDM Light Orchestrator runs a separate [TDM Light](https://github.com/Xyloni/tdm-light) miner process per active game. It keeps account pools and campaign queues, plans groups against deadlines, and exposes one control panel and one bot for the whole installation.

### ✨ Features

- Automatic campaign discovery for configured games and sequential groups within each game.
- Speed and resource-saving modes, with per-game settings and a preview before regrouping.
- Account pool assignment, quarantine for invalid OAuth tokens, and confirmed Telegram exports.
- Local web control, Telegram buttons, Discord slash commands, and configurable notifications.
- Twitch Inventory progress, recovery diagnostics, cumulative statistics, and daily summaries.

### 🚀 Quick start on a VPS

On a Linux amd64 or arm64 VPS with Docker Engine, Docker Compose, curl, tar, and sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/Xyloni/tdm-light-orchestrator/main/deploy/install-vps.sh -o /tmp/tdm-orchestrator-install.sh
sudo bash /tmp/tdm-orchestrator-install.sh --orchestrator
```

From your computer, open an SSH tunnel and visit [the local panel](http://127.0.0.1:8787):

```bash
ssh -N -L 8787:127.0.0.1:8787 user@vps
```

Add a game and upload the account list in the panel. Configure Telegram or Discord under bot settings. Run the installer without `--orchestrator` for standalone, single-game mining. See the [VPS guide](vps/README.md) for startup, updates, and rollback.

### 🪟 Windows and development

Start `start-orchestrator.bat` with Go 1.22 or newer in PATH, then open the local panel. Use `start.bat` for standalone mode.

```bash
go test ./...
go vet ./...
```

### 🗃️ Data and security

The VPS keeps orchestrator data under `/opt/tdm-light-orchestrator/data/orchestrator`. Windows uses `orchestrator-data`. State contains OAuth credentials and bot settings, so keep copies private. Updating the container preserves this directory. Rewards are claimed manually.

## Русский

TDM Light Orchestrator запускает отдельный процесс [TDM Light](https://github.com/Xyloni/tdm-light) для каждой активной игры. Он ведёт пулы и очереди кампаний, рассчитывает группы по дедлайнам и управляет всеми играми из одной панели или одного бота.

### ✨ Возможности

- Автопоиск кампаний для добавленных игр и последовательная обработка групп.
- Скорость или экономия ресурсов, правила отдельных игр и предпросмотр расписания.
- Пулы аккаунтов, карантин при ошибках авторизации и выгрузка через Telegram с подтверждением.
- Локальная веб-панель, кнопки Telegram, команды Discord и переключаемые уведомления.
- Подтверждённый прогресс Twitch Inventory, диагностика восстановления скорости, статистика и суточные сводки.

### 🚀 Быстрый запуск на VPS

Нужны Linux amd64 или arm64, Docker Engine, Docker Compose, curl, tar и sudo.

```bash
curl -fsSL https://raw.githubusercontent.com/Xyloni/tdm-light-orchestrator/main/deploy/install-vps.sh -o /tmp/tdm-orchestrator-install.sh
sudo bash /tmp/tdm-orchestrator-install.sh --orchestrator
```

На ПК откройте SSH-туннель и перейдите в [локальную панель](http://127.0.0.1:8787):

```bash
ssh -N -L 8787:127.0.0.1:8787 user@vps
```

Добавьте игру, загрузите аккаунты и подключите бота в панели. В Telegram отправьте `/start` для меню. В Discord начните с `/help` в личке бота. Для самостоятельного майнера одной игры запустите установщик без `--orchestrator`.

### 🔧 Обновление и откат

```bash
cd /opt/tdm-light-orchestrator
sudo bash update.sh
sudo bash update.sh --rollback
sudo docker compose logs -f orchestrator
```

Установщик скачивает готовый образ из GHCR. Файлы пулов, история и статистика хранятся отдельно от контейнера. [Руководство по VPS](vps/README.md)

### 🪟 Windows

Запустите `start-orchestrator.bat` с Go 1.22 или новее в PATH и откройте [локальную панель](http://127.0.0.1:8787). Для самостоятельного майнера используйте `start.bat`.

### 🗃️ Данные и безопасность

На VPS данные оркестратора лежат в `/opt/tdm-light-orchestrator/data/orchestrator`, на Windows — в `orchestrator-data`. Файл состояния содержит OAuth-токены и настройки бота. Храните его и резервные копии приватно. Награды получаются вручную.

[Полное руководство оркестратора](ORCHESTRATOR.md) · [Автономная сборка для VPS](vps-linux/README.md)
