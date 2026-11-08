# MikroTik Inspector

Настольная программа для **Windows** (работает также в Linux/macOS) с **HTML-интерфейсом**,
написанная на **Go**. Подключается к роутеру MikroTik (RouterOS) и показывает:

- 📌 **модель** устройства (board-name / routerboard),
- 📌 **версию прошивки** (RouterOS, текущая/заводская прошивка),
- 📌 **установленные пакеты** (имя, версия, статус).

Интерфейс — обычная HTML-страница: программа запускает локальный HTTP-сервер
(только `127.0.0.1`) и автоматически открывает её в браузере по умолчанию.
Пароль никуда не отправляется, кроме самого роутера.

## Возможности

| Функция | Описание |
|---|---|
| Два протокола | **RouterOS API** (бинарный, TCP 8728/8729) и **REST API** (HTTP/HTTPS, 80/443) |
| Режим «Авто» | Сначала пробуется API, при неудаче — REST; в ответе видно, какой протокол сработал |
| TLS | Поддержка HTTPS и API поверх TLS (самоподписанные сертификаты роутера) |
| Старые RouterOS | Вход и по одностадийной схеме (≥ 6.43), и по challenge-response (< 6.43) |
| Отчёт | Кнопка «Скопировать отчёт» — текстовая сводка в буфер обмена |
| Ноль зависимостей | Только стандартная библиотека Go |

## Готовые сборки

Скачать можно в [Releases](https://github.com/zigorminsk-debug/my-mikrotik/releases)
(`mikrotik-inspector-<версия>-windows-amd64.exe`) или в артефактах запуска CI
(**Actions → запуск → Artifacts**).

## Сборка

Требуется Go **1.27+** (в `go.mod` зафиксирована версия, CI ставит её автоматически).

```bash
# Linux / macOS
go build -o mikrotik-inspector .

# Windows (из Windows)
go build -o mikrotik-inspector.exe .

# Кросс-сборка Windows-EXE из Linux
GOOS=windows GOARCH=amd64 go build -o mikrotik-inspector.exe .
```

Проверки перед пушем:

```bash
gofmt -l .        # должно быть пусто
go vet ./...
go test -race ./...
```

## Запуск

```bash
./mikrotik-inspector                # откроется браузер с интерфейсом
./mikrotik-inspector -no-browser    # не открывать браузер (печатает URL)
./mikrotik-inspector -addr 127.0.0.1:8099
./mikrotik-inspector -version
```

На Windows запустите `mikrotik-inspector.exe` двойным щелчком — откроется
консольное окно (логи) и страница интерфейса в браузере.

### Использование интерфейса

1. Введите адрес роутера (например `192.168.88.1`), логин и пароль.
2. Выберите протокол: **Авто** (рекомендуется), **RouterOS API** или **REST API**.
3. При необходимости укажите порт (`0` — по умолчанию) и отметьте **TLS/HTTPS**.
4. Нажмите **Подключиться** — появятся карточки устройства и таблица пакетов.

Порты по умолчанию:

| Протокол | Обычный | С TLS |
|---|---|---|
| RouterOS API | 8728 | 8729 |
| REST API | 80 | 443 |

> Для REST в RouterOS должен быть включён сервис `/ip service set www-ssl ssl-certificate=...`
> (или обычный `www`), для API — сервис `api`/`api-ssl`. Учётные данные администратора.

## Архитектура

```
my-mikrotik/
├── main.go                    # точка входа: HTTP-сервер GUI, встраивание web/, открытие браузера
├── web/index.html             # весь HTML/CSS/JS интерфейс (встраивается в бинарник через embed)
├── mikrotik/
│   ├── proto.go               # кодек бинарного протокола RouterOS API (длина слова, предложения)
│   ├── api.go                 # клиент RouterOS API: вход (modern + challenge), чтение данных
│   ├── rest.go                # клиент REST API (Basic Auth, JSON)
│   ├── mikrotik.go            # типы (Params/Info/Package), нормализация, Collect() с fallback
│   └── mikrotik_test.go       # 14 тестов: кодек, mock API-сервер, mock REST, авторизация
├── .github/workflows/
│   ├── ci.yml                 # gofmt + vet + go test -race + сборка Linux/Windows (артефакты)
│   └── release.yml            # на тег v*: сборка exe + GitHub Release с SHA256SUMS
├── docs/STEPS.md              # пошаговый журнал разработки
└── go.mod                     # модуль без внешних зависимостей
```

### Как это работает

1. `main.go` поднимает сервер на `127.0.0.1` и открывает `web/index.html`.
2. Интерфейс отправляет `POST /api/info` с параметрами подключения.
3. `mikrotik.Collect()` подключается к роутеру выбранным протоколом:
   - **API**: TCP-соединение, `/login`, затем `/system/identity/print`,
     `/system/resource/print`, `/system/routerboard/print`, `/system/package/print`;
   - **REST**: `GET /rest/system/identity|resource|routerboard|package` с Basic Auth.
4. Результат возвращается в JSON и отрисовывается в карточках и таблице.

## Сборка на GitHub (GitHub Actions)

Сборка запускаются **на каждый push** — сразу после внесения изменений.

### CI (`.github/workflows/ci.yml`)

| Job | Что делает |
|---|---|
| `test` | `gofmt -l`, `go vet ./...`, `go test -race -count=1 ./...` |
| `build` | Сборка `linux/amd64` (ubuntu) и `windows/amd64` (windows-latest), загрузка артефактов |

Как посмотреть:

```bash
gh run list                 # список запусков
gh run watch                # следить за текущим запуском
gh run view --log           # логи последнего запуска
gh run download             # скачать собранные артефакты (exe)
```

Артефакты также доступны в UI: **Actions → запуск → Artifacts**.

### Релизы (`.github/workflows/release.yml`)

```bash
git tag v0.1.0
git push origin v0.1.0
```

На тег `v*` собираются `*-windows-amd64.exe` и `*-linux-amd64`,
создаётся GitHub Release с файлами и `SHA256SUMS.txt`.

## Тесты

```bash
go test -v ./mikrotik/
```

Покрыто: кодек длин/предложений, эталонный вектор challenge-авторизации,
мок-сервер RouterOS API (современный вход, challenge-вход, неверный пароль),
мок REST API (успех/401), fallback auto-режима, нормализация адресов
(URL, `host:port`, IPv6), порты по умолчанию, недоступный хост.

## Документация

- [docs/STEPS.md](docs/STEPS.md) — пошаговый журнал: все этапы разработки,
  сборки и настройки CI.
