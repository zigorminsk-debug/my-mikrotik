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

### Запуск #1 — `42512972572` (push f1d52eb, ветка `arena/74faea74-my-mikrotik`)

| Job | Результат |
|---|---|
| Форматирование, vet и тесты | ✅ 40s (`gofmt`, `go vet`, `go test -race`) |
| Сборка mikrotik-inspector-linux-amd64 | ✅ 27s, артефакт загружен |
| Сборка mikrotik-inspector-windows-amd64.exe | ✅ 36s, артефакт загружен |

**Итог:** `success`, артефакты: `mikrotik-inspector-linux-amd64`,
`mikrotik-inspector-windows-amd64.exe`.

Замечания аннотаций (не влияют на результат): устаревший Node.js 20 у
`actions/checkout@v4` / `setup-go@v5` / `upload-artifact@v4` и предупреждение
о миграции `ubuntu-latest` на Ubuntu 26. **Принято решение:** обновить все
используемые действия до актуальных мажорных версий
(`checkout@v7`, `setup-go@v7`, `upload-artifact@v7`, `action-gh-release@v3`).

### Запуск #2 — `42513190476` (push e14a4d6, обновление действий до v7/v3)

| Job | Результат |
|---|---|
| Форматирование, vet и тесты | ✅ |
| Сборка mikrotik-inspector-linux-amd64 | ✅, артефакт |
| Сборка mikrotik-inspector-windows-amd64.exe | ✅, артефакт |

**Итог:** `success`. Предупреждения об устаревшем Node.js 20 исчезли
(действия обновлены до `checkout@v7`, `setup-go@v7`, `upload-artifact@v7`).

### Релиз `v0.1.0` — запуск `42513366008` (workflow `Release`)

Пушен тег `v0.1.0` → workflow собрал бинарники и опубликовал GitHub Release:

| Файл | Назначение |
|---|---|
| `mikrotik-inspector-v0.1.0-windows-amd64.exe` | готовая Windows-программа |
| `mikrotik-inspector-v0.1.0-linux-amd64` | Linux-сборка |
| `SHA256SUMS.txt` | контрольные суммы |

Релиз: https://github.com/zigorminsk-debug/my-mikrotik/releases/tag/v0.1.0

**Итог:** `success` — все шаги «изменения → сборка на GitHub» отработали.

---

# Этап 2. Конструктор конфигурации (v0.2.0)

Задача: генерировать код для New Terminal под конкретную модель и прошивку, повторять
меню RouterOS с чекбоксами, блокировать поля, затрагивающие отсутствующие пакеты,
и применять настройки на роутере (не только копировать скрипт).

## Шаг 1. Исследование: пакеты RouterOS и запуск скрипта

1. Прочитана таблица пакетов в документации MikroTik (Manual:System/Packages).
   Вывод: `dhcp` — DHCP-клиент и сервер; `ppp` — PPP/PPPoE; `wireless` — драйверы
   Wi-Fi (legacy); `system` — адреса, DNS-кэш, IP pool, firewall (то есть LAN/DNS/NTP
   зависят от `system`, который есть всегда). **NTP-клиент встроен в `system`**,
   поэтому раздел NTP не блокируется. Для RouterOS 7 используется `wifiwave2`.
2. Проверено, как запускать скрипты удалённо. Варианты:
   - API: `/system/script/remove` → `/system/script/add` → `/system/script/run`;
   - REST: `POST /rest/system/script` → `POST /rest/system/script/run` с телом
     `{"number":"mti-apply"}` (аргумент называется `number`, но принимает и имя
     скрипта — подтверждено `/console/inspect path=system,script/run` и обсуждением
     на форуме MikroTik).

   Неофициальный `POST /rest/execute` не используется.

## Шаг 2. `mikrotik/config.go` — модель настроек и генератор

- Типы `Settings` (LAN, DHCP, DNS, NTP, PPPoE, WiFi), `ConfigRequest`, `ConfigResult`,
  `FeatureInfo` (id, путь в меню, пакет, доступность, причина, режим Wi-Fi), `Derived`.
- `AnalyzeFeatures(device, ifaces)` — доступность разделов по установленным пакетам
  и наличию Wi-Fi-интерфейсов; причина формулируется дословно:
  «необходимый пакет отсутствует: dhcp».
- `deriveLAN` — из адреса с маской вычисляются сеть, шлюз и пул; адрес роутера из пула
  исключается (при адресе в середине подсети получается два диапазона через запятую).
- `Generate` — нормализация IP → валидация → скрипт. Включённый, но недоступный раздел
  **не ошибка**: он попадает в `skipped` и выводится предупреждением.
- `buildScript` — идемпотентные команды: перед `add` стоит `remove [find ...]`,
  объекты помечены `mti-lan` / `mti-net` / `mti-pppoe` / `mti-pool` / `mti-dhcp` / `mti-wifi`.
- Wi-Fi: для wave2 — `/interface wifi configuration add name=mti-wifi …` и привязка
  к интерфейсу; для legacy — `/interface wireless set <iface> mode=ap authentication-types=wpa2-psk`.

> **Корректировка после первого прогона CI.** Первая версия проверяла только точные
> имена пакетов (`system`, `dhcp`, `ppp`). На практике в RouterOS 7 и в комбинированных
> сборках RouterOS 6 установлен один пакет `routeros` (или `routeros-arm`, `routeros-mmips`),
> внутри которого уже есть system/dhcp/ppp и поддержка радио. Иначе конструктор
> ошибочно блокировал бы почти все разделы. Добавлены `isCombinedPackage` и
> `hasWiFiPackage` (`wifiwave2`, `wifi-qcom`, `wifi-qcom-ac`, `wifi`, `wireless`),
> а также три теста на этот случай.

## Шаг 3. `mikrotik/iface.go` и `mikrotik/apply.go`

- `CollectInterfaces` — API `/interface/print` или REST `GET /rest/interface`
  (поля name, type, comment, mac-address, disabled), с тем же auto-fallback, что и `Collect`.
- `ApplyScript` — создаёт на роутере скрипт `mti-apply` (права на запись/политику)
  и запускает его; по API с запасным вариантом `=numbers=` вместо `=number=`,
  по REST — `{"number": "mti-apply"}`. Ошибка удаления старого скрипта игнорируется
  (скрипта может не быть). Пустой скрипт и размер > 64 КБ отклоняются.
- В `rest.go` добавлен общий helper `post` с разбором ошибок роутера.

## Шаг 4. Эндпоинты в `main.go`

| Метод | Путь | Назначение |
|---|---|---|
| POST | `/api/info` | данные об устройстве (модель, прошивка, пакеты) |
| POST | `/api/interfaces` | интерфейсы + доступность разделов (`features`) |
| POST | `/api/config` | валидация и генерация скрипта (`script`, `features`, `derived`, `skipped`) |
| POST | `/api/apply` | применение скрипта на роутере |

## Шаг 5. Интерфейс (`web/index.html`)

- Две вкладки: **Информация** (как было) и **Конструктор**.
- Меню повторяет RouterOS: `IP` → Addresses / DHCP Server / DNS, `System` → NTP Client,
  `PPP` → PPPoE Client, `Interface` → Wireless. У каждого пункта чекбокс и путь в меню.
- Недоступный пункт: чекбокс заблокирован, строка приглушена, справа жёлтая плашка
  с причиной; тело раздела не раскрывается.
- Все данные вводятся в конструкторе: адрес и подсеть LAN, интерфейсы (из роутера),
  пул и время аренды DHCP, DNS-серверы, серверы времени и часовой пояс,
  логин/пароль PPPoE, SSID/пароль/страна Wi-Fi, флажки NAT и маршрута по умолчанию.
- Приведение IP: на вводе допускаются только `0-9 . , /`; при потере фокуса
  значение канонизируется (`192.168.001.010/024` → `192.168.1.10/24`), плюс та же
  нормализация на сервере — на случай запроса в обход интерфейса.
- Предпросмотр: `POST /api/config` с задержкой 220 мс, отображение скрипта, ошибок
  валидации и предупреждений о пропущенных разделах, подсказки сети/шлюза/автопула.
- Кнопки: «Скопировать для New Terminal», «Применить на роутере» (с подтверждением),
  «Скачать .rsc».

## Шаг 6. Тесты и проверка

Добавлено 19 тестов конструктора (`mikrotik/config_test.go`), всего в проекте 34:
расчёт подсети (`/24`, `/30`, `/32`, IPv6, мусор), доступность разделов (DHCP, PPPoE,
Wi-Fi legacy/wave2, «пакет есть — интерфейсов нет», объединённый пакет `routeros`,
`wifi-qcom`), генерация скрипта для обоих режимов Wi-Fi, пропуск недоступного раздела, шесть случаев ошибок валидации, приведение IP
(`NormalizeAddr` и внутри `Generate`), экранирование значений, чтение интерфейсов по API
и REST, применение скрипта по API (порядок remove → add → run) и по REST.

Интерфейс проверен сценарием на jsdom (21 проверка): подключение → открытие вкладки
«Конструктор» → блокировка недоступного DHCP с надписью → выбор интерфейсов по умолчанию
→ включение PPPoE кликом по заголовку → авторамат IP → предпросмотр → применение →
показ ошибок валидации и блокировка кнопок.

## Шаг 7. Сборка на GitHub и релиз v0.2.0

| Запуск | Коммит | Что проверялось | Результат |
|---|---|---|---|
| `42518506964` | `365d19f` | конструктор целиком: gofmt, vet, тесты с `-race`, сборки Linux и Windows | ✅ `success` |
| `42518746547` | `4cd693b` | учёт объединённого пакета `routeros` / `wifi-qcom` | ✅ `success` |

Проверка перед пушем (локально): `gofmt -l .` пусто, `go vet ./...` без замечаний,
`go test -race -count=1 ./...` — 34 теста PASS, сценарий интерфейса на jsdom — 21 проверка PASS.

### Релиз `v0.2.0` — запуск `42518956002` (workflow `Release`)

Тег `v0.2.0` опубликован, GitHub Release содержит:

| Файл | Назначение |
|---|---|
| `mikrotik-inspector-v0.2.0-windows-amd64.exe` | готовая Windows-программа с конструктором |
| `mikrotik-inspector-v0.2.0-linux-amd64` | Linux-сборка |
| `SHA256SUMS.txt` | контрольные суммы |

Релиз: https://github.com/zigorminsk-debug/my-mikrotik/releases/tag/v0.2.0 — **`success`**.

Осталось одно известное предупреждение GitHub: `ubuntu-latest` переедет на Ubuntu 26
после 19.10.2026 (на работу не влияет, действия уже на актуальных мажорах).

---

## Полезные команды

```bash
gh run list                    # запуски CI
gh run watch                   # следить за запуском
gh run view --log              # полные логи
gh run download                # скачать артефакты (exe)
git tag v0.1.0 && git push origin v0.1.0   # релиз с exe
```
