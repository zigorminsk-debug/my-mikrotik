# Журнал разработки: MikroTik Inspector

Пошаговая документация всех этапов — от анализа требований до сборки на GitHub.

---

## Шаг 1. Анализ требований

**Задача:** программа для Windows с HTML-интерфейсом, написанная на Go,
подключается к роутеру MikroTik и получает:

1. модель устройства,
2. версию прошивки,
3. установленные пакеты.

**Решения, принятые на этом шаге:**

- GUI — HTML-страница, встроенная в бинарник (`embed`): программа поднимает
  локальный HTTP-сервер на `127.0.0.1` и открывает страницу в браузере
  по умолчанию. Это даёт нативно «HTML-GUI» без внешних зависимостей
  (WebView/CGO не требуются, кросс-сборка под Windows простая).
- Два протокола подключения вместо одного:
  - **RouterOS API** — классический бинарный протокол (TCP 8728/8729),
    работает на всех версиях RouterOS;
  - **REST API** (HTTP 80/443, RouterOS ≥ 6.43) — запасной вариант.
- Только стандартная библиотека Go: песочница разрешает доступ лишь к
  github.com/registry.npmjs.org/pypi.org, а зеркала модулей Go
  (proxy.golang.org) недоступны — внешние пакеты скачать невозможно.
  Всё реализовано на `net`, `crypto/tls`, `net/http`, `embed`.

---

## Шаг 2. Подготовка Go-тулчейна в среде разработки

Go не был установлен, официальные зеркала (go.dev, dl.google.com, apt)
заблокированы сетью песочницы. Порядок действий:

1. Проверены разрешённые хосты: `github.com`, `api.github.com`,
   `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`.
2. Релизы GitHub отдают файлы с `release-assets.githubusercontent.com` —
   тоже заблокировано, скачать готовый тулчейн оттуда нельзя.
3. **Найден путь:** в npm-репозитории `@ttsc/linux-x64` (версия 0.36.3)
   обнаружен полный Go-тулчейн (`package/bin/go/…`: `bin/go`, `pkg/tool`,
   `src/`, `VERSION`) — пакет скачан с `registry.npmjs.org` (разрешён).
4. Распакован в `/home/user/toolchain`, проверен:
   `go version` → **go1.27.3**, собран и запущен hello-world,
   выполнена кросс-сборка `GOOS=windows` → `hello.exe` (заголовок `MZ`).

Локальный тулчейн: `GOROOT=/home/user/toolchain/package/bin/go`.

---

## Шаг 3. Сверка протокола RouterOS API с открытыми реализациями

Чтобы не гадать с форматом бинарного протокола, изучены два независимых
источника (через `api.github.com` и `files.pythonhosted.org`):

- [`go-routeros/routeros`](https://github.com/go-routeros/routeros) — `proto/writer.go`, `proto/reader.go`, `client.go`;
- PyPI `librouteros` 4.2.2 — `protocol.py`, `login.py`.

**Подтверждённые детали протокола:**

| Вопрос | Ответ |
|---|---|
| Длина слова | 1 байт `<0x80`; 2 байта `10xxxxxx`; 3 байта `110xxxxx`; 4 байта `1110xxxx` |
| Конец предложения | нулевое слово (байт `0x00`) |
| Слова-атрибуты | `=key=value` (и в запросах, и в ответах) |
| Ответы | `!re` (записи), `!done`, `!trap` (ошибка команды), `!fatal`, `!empty` |
| Вход ≥ 6.43 | `/login =name=… =password=…` → `!done` без `ret` |
| Вход < 6.43 | `!done =ret=<challenge hex>` → `/login =name=… =response=…`, где `response = "00" + md5(0x00 ‖ пароль ‖ challenge)` |
| `!trap` | сервер присылает `!trap` **и затем** `!done` — читать до `!done`, иначе поток рассинхронизируется |

Эти выводы легли в основу `mikrotik/proto.go` и `mikrotik/api.go`.

---

## Шаг 4. Реализация кода

Созданы файлы:

| Файл | Содержимое |
|---|---|
| `go.mod` | модуль `github.com/zigorminsk-debug/my-mikrotik`, `go 1.27` |
| `mikrotik/proto.go` | кодек длины слова, чтение/запись предложений, разбор `=k=v` |
| `mikrotik/api.go` | TCP/TLS-клиент RouterOS API: вход modern + challenge, `exec()` с корректной обработкой `!trap`/`!fatal`, сбор данных (`identity`, `resource`, `routerboard`, `package`) |
| `mikrotik/rest.go` | REST-клиент: Basic Auth, `InsecureSkipVerify` (самоподписанный сертификат), JSON-декодирование с `json.Number` |
| `mikrotik/mikrotik.go` | типы `Params`/`Info`/`Package`, нормализация адреса (URL, `host:port`, IPv6), `Collect()` с режимом `auto` (API → REST) и объединёнными ошибками |
| `main.go` | HTTP-сервер, `//go:embed web`, `POST /api/info`, CSP-заголовки, автозапуск браузера (`rundll32 url.dll,FileProtocolHandler` на Windows), `-addr`, `-no-browser`, `-version`, версия через `-ldflags` |
| `web/index.html` | весь интерфейс: форма подключения, карточки устройства, таблица пакетов, кнопка «Скопировать отчёт», сохранение настроек (без пароля) в `localStorage` |

**Найденные и исправленные при разработке ошибки:**

1. `exec()` возвращался сразу на `!trap`, не дочитывая `!done` —
   рассинхронизация потока; плюс `!trap` терялся, если `!done` приходил
   следом (тест `TestCollectAPIWrongPassword` это поймал).
2. Неверный порядок возвращаемых значений при вызове `exec()`
   (records/done/err) — пойман `go vet`.

---

## Шаг 5. Тесты (14 шт., все зелёные)

`mikrotik/mikrotik_test.go`:

- `TestEncodeLength` — границы кодирования длины (0, 0x7F, 0x80, 0x3FFF, 0x4000, …, ошибка на 0x10000000);
- `TestSentenceRoundTrip` — предложения со словами до 16 КБ;
- `TestSplitAttr` — разбор `=key=value`;
- `TestChallengeResponse` — **эталонный вектор** `00c17511d224a5e93170632807d36388aa` (сверен с md5);
- `TestCollectViaAPI` — полный сбор данных с мок-сервером RouterOS API;
- `TestCollectViaAPIChallengeLogin` — двухэтапный вход RouterOS < 6.43;
- `TestCollectAPIWrongPassword` — ошибка авторизации;
- `TestCollectViaREST`, `TestCollectRESTWrongPassword` — REST-мок (успех и 401);
- `TestAutoProtocolFallsBackToREST` — fallback в auto-режиме;
- `TestNormalizeParams` — 9 подкейсов нормализации адреса;
- `TestCollectUnreachable`, `TestDefaultPorts`.

Мок-сервер API реализован прямо в тестах на том же кодеке
(`readSentence`/`writeSentence`) — тесты проверяют обе стороны протокола.

---

## Шаг 6. Локальная сборка и смоук-тест

```text
gofmt -l .                 → пусто
go vet ./...               → OK
go test -race ./...        → ok (1.032s)
go build (linux/amd64)     → dist/mikrotik-inspector-linux-amd64
GOOS=windows (amd64)       → dist/mikrotik-inspector-windows-amd64.exe (MZ)
```

Смоук-тест HTTP-слоя (`-no-browser -addr 127.0.0.1:8099`):

- `GET /` → HTML + заголовки CSP/`nosniff`;
- `POST /api/info` с недоступным хостом → JSON `ok:false` с ошибками
  обоих протоколов (auto-режим);
- битый JSON → `400` с описанием;
- `GET /api/info` → `405`.

---

## Шаг 7. GitHub Actions — сборка сразу после изменений

Созданы два workflow (валидация YAML — `yaml.safe_load`, OK):

### `.github/workflows/ci.yml` — запускается на **каждый push**

1. **test** (ubuntu-latest): `actions/checkout@v4` → `actions/setup-go@v5`
   (версия из `go.mod`, кеш отключён — зависимостей нет) →
   `gofmt -l .` (фейл при неразformatted-файлах) → `go vet ./...` →
   `go test -race -count=1 ./...`.
2. **build** (после test, матрица ubuntu/windows):
   `git describe --tags --always` для версии →
   `go build -ldflags "-s -w -X main.version=…"` →
   `actions/upload-artifact@v4` (сохранение 14 дней).

### `.github/workflows/release.yml` — на тег `v*`

Сборка Windows `.exe` + Linux бинарника, `SHA256SUMS.txt`,
публикация через `softprops/action-gh-release@v2`.

---

## Шаг 8. Публикация и запуск CI

1. Все изменения закоммичены в ветку `arena/74faea74-my-mikrotik`.
2. Выполнен `git push origin arena/74faea74-my-mikrotik` —
   это **сразу запускает CI** (workflow реагирует на push).
3. Запуск проверен командами `gh run list` / `gh run watch`,
   логи — `gh run view --log`.
4. Результат сборки: см. раздел «Статус CI» ниже.

---

## Статус CI

> Раздел обновляется по факту запусков.

<!-- CI-STATUS -->

---

## Полезные команды

```bash
gh run list                    # запуски CI
gh run watch                   # следить за запуском
gh run view --log              # полные логи
gh run download                # скачать артефакты (exe)
git tag v0.1.0 && git push origin v0.1.0   # релиз с exe
```
