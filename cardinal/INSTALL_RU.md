# Установка TDM Light Orchestrator и подключение FunPay Cardinal

Эта инструкция рассчитана на два контейнера Docker Compose на одном Linux VPS. Оркестратор проверяет инвентарь Twitch, а плагин Cardinal применяет результат к выбранным лотам FunPay. Для управления используются два отдельных бота Telegram.

## 1. Установите оркестратор

Нужны Linux amd64 или arm64, Docker Engine с Compose, `git`, `curl`, `tar`, `openssl` и SSH-доступ с `sudo`. В начале сессии VPS выполните `sudo -i`, если вы вошли обычным пользователем. Это даст доступ к каталогу установки. Если оркестратор уже установлен, переходите к настройке панели. Установщик первого запуска записывает новый `compose.yaml`. Для действующей установки с настроенным сокетом используйте `update.sh` из раздела обновления.

```bash
docker compose version
curl -fsSL https://raw.githubusercontent.com/Xyloni/tdm-light-orchestrator/main/deploy/install-vps.sh -o /tmp/tdm-orchestrator-install.sh
sudo bash /tmp/tdm-orchestrator-install.sh --orchestrator
```

Каталог по умолчанию — `/opt/tdm-light-orchestrator`. При успешном запуске контейнер `orchestrator` имеет статус `healthy`.

```bash
cd /opt/tdm-light-orchestrator
sudo docker compose -f compose.yaml ps orchestrator
```

На своём компьютере запустите SSH-туннель. Замените `user@vps` адресом и именем пользователя VPS. Оставьте это окно открытым и откройте `http://127.0.0.1:8787` в браузере.

```bash
ssh -N -L 8787:127.0.0.1:8787 user@vps
```

В разделе **Бот оркестратора** выберите Telegram, сохраните токен бота и свой числовой ID пользователя Telegram. Затем откройте личный чат с этим ботом и отправьте `/start`. Панель слушает `127.0.0.1:8787`. Доступ к ней оставьте через SSH-туннель.

## 2. Подготовьте Cardinal и файл плагина

Для новой установки используйте [официальную инструкцию Docker для FunPay Cardinal](https://github.com/sidor0912/FunPayCardinal#whale-docker). Склонируйте репозиторий в свой постоянный каталог, соберите образ и пройдите интерактивную настройку с хоста VPS. Мастер запрашивает учётные данные в терминале.

```bash
git clone https://github.com/sidor0912/FunPayCardinal.git
cd FunPayCardinal
sudo docker compose build
sudo docker compose run --rm funpaycardinal
```

Когда настройка завершится и бот ответит, выйдите из разового контейнера через `Ctrl+C` и запустите постоянный сервис.

```bash
sudo docker compose up -d funpaycardinal
```

Сохраните [файл `tdm_lot_preview.py`](tdm_lot_preview.py) из той же ревизии проекта, из которой собран используемый образ оркестратора. В **боте Cardinal** откройте `/menu` → **Плагины** → **Добавить плагин** и отправьте файл под именем `tdm_lot_preview.py`. При обновлении используйте то же имя файла. Альтернатива — положить файл с хоста в `plugins/tdm_lot_preview.py` рядом с Compose-файлом Cardinal. Каталог `./plugins` в стандартной установке монтируется как `/app/plugins` внутри контейнера. Пересоздание Cardinal в следующем шаге применит изменения.

## 3. Соедините контейнеры локальным сокетом

На VPS подготовьте каталог сокета и отдельный файл общего токена. Docker Compose прочитает этот файл с хоста для обоих сервисов. Команда генерирует токен при первом запуске и сохраняет уже существующий файл.

```bash
sudo install -d -m 0750 -o 10001 -g 10001 /opt/tdm-light-orchestrator/cardinal-ipc
sudo sh -c '
  set -eu
  umask 077
  file=/opt/tdm-light-orchestrator/cardinal.env
  if [ ! -e "$file" ]; then
    token=$(openssl rand -hex 32)
    printf "ORCHESTRATOR_LOT_PREVIEW_TOKEN=%s\n" "$token" > "$file"
  fi
  chmod 0600 "$file"
  grep -Eq "^ORCHESTRATOR_LOT_PREVIEW_TOKEN=.{32,}$" "$file"
'
```

В `/opt/tdm-light-orchestrator/compose.yaml` добавьте `env_file`, путь сокета и монтирование каталога в существующий сервис `orchestrator`. Удалите из его `environment` строку `ORCHESTRATOR_LOT_PREVIEW_TOKEN`, если она там есть. Иначе она перекроет значение из `env_file`. Пример показывает поля, нужные для соединения. Сохраните имеющиеся настройки образа, команды, сети и проверки запуска.

```yaml
services:
  orchestrator:
    env_file:
      - /opt/tdm-light-orchestrator/cardinal.env
    environment:
      ORCHESTRATOR_CARDINAL_SOCKET: /run/tdm-cardinal/preview.sock
      ORCHESTRATOR_WEB_ADDR: 127.0.0.1:8787
    volumes:
      - ./data:/data
      - /opt/tdm-light-orchestrator/cardinal-ipc:/run/tdm-cardinal
```

В каталоге Cardinal добавьте следующие поля к существующему сервису `funpaycardinal` в `docker-compose.yml`. Если токен уже указан в его `environment`, удалите эту строку, чтобы применился общий `env_file`. Сохраните существующие `volumes`, добавив последний путь из примера. Compose передаёт дополнительную группу `10001`, чтобы процесс Cardinal мог подключиться к сокету при запуске от непривилегированного пользователя.

```yaml
services:
  funpaycardinal:
    env_file:
      - /opt/tdm-light-orchestrator/cardinal.env
    environment:
      ORCHESTRATOR_CARDINAL_SOCKET: /run/tdm-cardinal/preview.sock
    group_add:
      - "10001"
    volumes:
      - ./configs:/app/configs
      - ./logs:/app/logs
      - ./storage:/app/storage
      - ./plugins:/app/plugins
      - /opt/tdm-light-orchestrator/cardinal-ipc:/run/tdm-cardinal:ro
```

Проверьте конфигурацию и пересоздайте оба контейнера. В последнем блоке перейдите в свой каталог Cardinal.

```bash
cd /opt/tdm-light-orchestrator
sudo docker compose -f compose.yaml config --quiet
sudo docker compose -f compose.yaml up -d --force-recreate --wait orchestrator
sudo stat -c '%a %u:%g %n' cardinal-ipc cardinal-ipc/preview.sock
```

Ожидаемые права каталога — `750 10001:10001`. У сокета должны быть права `660` и владелец `10001:10001`.

```bash
cd /путь/к/FunPayCardinal
sudo docker compose config --quiet
sudo docker compose up -d --force-recreate funpaycardinal
sudo docker compose exec -T funpaycardinal sh -c 'test -S "$ORCHESTRATOR_CARDINAL_SOCKET" && test "${#ORCHESTRATOR_LOT_PREVIEW_TOKEN}" -ge 32'
```

Последняя команда проходит без текста при успехе. В личном чате с ботом Cardinal откройте `/tdm_lots` → **Диагностика связи**. Бот проверит доступ к оркестратору. Из карточки лота одноимённая кнопка дополнительно проверяет чтение инвентаря Twitch на одном аккаунте из товарного TXT.

Если бот недоступен, для проверки сокета и токена можно отправить заведомо неполный запрос. Оркестратор проверит авторизацию и ответит `400` на пустые параметры. Команда печатает только HTTP-статус.

```bash
sudo docker compose exec -T funpaycardinal python3 - <<'PY'
import http.client
import os
import socket

class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(5)
        self.sock.connect(os.environ['ORCHESTRATOR_CARDINAL_SOCKET'])

connection = UnixHTTP('localhost', timeout=5)
connection.request('POST', '/internal/managed-lot', '{}', {
    'Authorization': 'Bearer ' + os.environ['ORCHESTRATOR_LOT_PREVIEW_TOKEN'],
    'Content-Type': 'application/json',
})
response = connection.getresponse()
print('HTTP', response.status)
connection.close()
raise SystemExit(0 if response.status == 400 else 1)
PY
```

## 4. Подключите один пилотный лот в Telegram

1. В **боте Cardinal** заведите запись автовыдачи с отдельным TXT для лота. В шаблоне сообщения должен быть `$product`. У самого предложения на сайте FunPay выключите встроенную автовыдачу. Заказ будет обрабатывать плагин Cardinal.
2. В `/tdm_lots` откройте лот и нажмите **Подключить лот → Открыть состав в оркестраторе**. Если ссылки нет, откройте **Лоты → Составы лотов** в боте оркестратора и выберите запись по названию.
3. Отметьте нужные Drops, нажмите **Просмотреть и сохранить**, затем **Сохранить состав**. Для завершившейся кампании используйте кнопку поиска в редакторе. Код лота можно скопировать в **Управлении** в Cardinal.
4. В карточке Cardinal нажмите **Проверить до 10 аккаунтов**. Сверьте число подтверждённых наборов и причины пропусков.
5. Вернитесь к шагу подключения и нажмите **Продолжить подключение**, затем выберите формат TXT. Если бот попросит указать ID, возьмите числовой `id` из ссылки предложения `offer?id=…` и выполните показанную ботом команду. Снова откройте карточку и проверьте, что началась проверка запаса.

| Формат | Как использовать |
| --- | --- |
| `логин:пароль` | Для обычной выдачи. Оркестратору должны быть известны OAuth и Twitch user ID. Если их нет, загрузите приватный полный TXT в **Лоты → Дополнительно → Загрузить аккаунты лотов**. |
| Полный TXT `email:login:password:oauth_token:user_ID:device_ID` | Источник данных для проверки. Шаблон `$product` передаёт исходную строку товарного файла покупателю. Полный формат в товарном файле включает почту, OAuth и идентификаторы. Используйте для товарного файла `логин:пароль`, если выдача этих полей не задумана. |

Для управляемой записи плагин выключает три штатных переключателя Cardinal — **Авто-выдача**, **Восстановление** и **Деактивация**. После обновления карточки они должны стать красными, если соответствующие глобальные настройки включены. Оставьте глобальную автовыдачу Cardinal включённой, чтобы заказы продолжал обрабатывать плагин. Автоподнятие лотов в поиске управляется общей настройкой Cardinal для категорий.

Повторная проверка проходит примерно через 15 минут после предыдущего обхода. Перед отправкой плагин сверяет оплату и снова проверяет выбранные аккаунты. Если отправка не подтверждена, резерв сохраняется для сверки с чатом FunPay. При снятии лота в свободный пул попадают только непроданные аккаунты с пустым инвентарём, строки которых убрал сам Cardinal. Уведомления о снятии и возврате повторяются при временной недоступности Telegram.

После первого обхода откройте карточку лота в Cardinal и сверьте время успешной проверки и количество на FunPay. Плагин вычитает резерв и учитывает оплаченные заказы. При неполном ответе Twitch он может уменьшить количество, если подтверждена часть остатка. Если подтверждённый остаток равен нулю, но результат проверки неоднозначен, на сайте остаётся прежнее число до следующей сверки. Выдача проходит только по аккаунтам, наличие наград на которых подтверждено перед отправкой. Если фоновая сверка долго не завершается, Telegram сообщает о сбое и затем о восстановлении.

Если возникла проблема с заказом, в `/tdm_lots` откройте **Заказы требуют внимания**, выберите заказ и нажмите **Сверить с FunPay**. Бот покажет статус оплаты, причину задержки и число зарезервированных аккаунтов. Если отправка уже состоялась, сверка подтвердит её по сообщению продавца. Команда сверки сама сообщений покупателю не отправляет.

Для снятия по дате укажите `[ДО ДД.ММ.ГГГГ]` в названии лота FunPay. Указанный день начинается в `00:00` по Москве. Дата в **Составе и сроках** служит подсказкой для этой метки. Кампании из сохранённых лотов защищены от получения наград оркестратором. У уже полученных Drops наличие награды в будущей игровой привязке остаётся неподтверждённым.

## 5. Обновление, откат и поиск ошибок

Перед обновлением сделайте приватные резервные копии каталога `/opt/tdm-light-orchestrator/data`, каталогов `configs`, `storage` и `plugins` Cardinal и обоих Compose-файлов. Журнал резервов Cardinal находится в `storage/tdm-managed-lots.json`. При обновлении сохраняйте этот файл вместе с TXT, которым он соответствует. Для отката после новых продаж сначала приостановите продажу управляемых лотов и сверьте заказы. Восстановление старого TXT поверх актуальных продаж может вернуть уже выданный аккаунт в остаток.

Для копии перед обновлением замените `CARDINAL_DIR` на действительный путь к Cardinal. Команды сохраняют архивы в закрытый каталог root. Для полной копии состояния и товарных файлов делайте её, когда продажи приостановлены и активные выдачи завершены.

```bash
CARDINAL_DIR=/opt/FunPayCardinal
umask 077
TDM_BACKUP_DIR=$(mktemp -d /root/tdm-cardinal-backup.XXXXXX) &&
tar -C /opt/tdm-light-orchestrator -czf "$TDM_BACKUP_DIR/orchestrator.tar.gz" data compose.yaml .env cardinal.env &&
tar -C "$CARDINAL_DIR" -czf "$TDM_BACKUP_DIR/cardinal.tar.gz" configs storage plugins docker-compose.yml &&
printf 'Резервная копия: %s\n' "$TDM_BACKUP_DIR"
```

```bash
cd /opt/tdm-light-orchestrator
sudo bash update.sh
# Чтобы перейти с закреплённого тега на последний проверенный образ:
sudo bash update.sh ghcr.io/xyloni/tdm-light-orchestrator:latest
# При необходимости вернуть предыдущий успешный образ
sudo bash update.sh --rollback
```

`update.sh` использует сохранённый тег образа. Для смены тега передайте его первым аргументом. `--rollback` откатывает только образ оркестратора. Для отката плагина восстановите прежний `tdm_lot_preview.py` и перезапустите Cardinal. Убедитесь, что выбранные версии плагина и оркестратора используют совместимый формат данных, прежде чем открывать продажи.

| Симптом | Что проверить |
| --- | --- |
| `/tdm_lots` не отвечает | Запущен нужный контейнер Cardinal, плагин лежит в его `/app/plugins`, ваш Telegram-аккаунт авторизован. Проверьте `sudo docker compose logs --tail 100 funpaycardinal` из каталога Cardinal. |
| Тест возвращает `HTTP 404` | Оба контейнера используют общий `env_file`, токен длиной от 32 символов, строки окружения от другой настройки не перекрывают его. Также проверьте версию образа оркестратора. |
| Тест показывает `Permission denied` | Проверьте владельца и права каталога и сокета, `group_add` и совпадение пути на хосте. После изменения Compose пересоздайте контейнер Cardinal. |
| Панель не открывается | Проверьте SSH-туннель и `sudo docker compose -f compose.yaml logs --tail 100 orchestrator` из `/opt/tdm-light-orchestrator`. |
| После падения процесса в логах `bind: address already in use` для Unix-сокета | Остановите сервис `orchestrator`. Если остался его файл `cardinal-ipc/preview.sock`, удалите только этот старый сокет и запустите сервис снова. |
| Аккаунты есть в TXT, но мало подтверждённых наборов | Сверьте состав кампаний в оркестраторе, данные Twitch и правильность формата. Временная ошибка сервиса и уже полученная награда могут оставить аккаунт неподтверждённым. |
| Возврат остатка задержан | Откройте карточку в `/tdm_lots` и сверьте причину задержки, открытые оплаты и резерв. До окончания сверки оставьте TXT как есть, чтобы сохранённый резерв соответствовал файлу. |
