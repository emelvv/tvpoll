# Запуск, проверка и воспроизводимость

## Быстрый сценарий без Go на хосте

```bash
cp .env.example .env
docker compose up --build -d --wait
sh scripts/smoke.sh
```

Ожидание healthchecks включено в `--wait`. Первый запуск скачивает образы и Go modules. Открыть `http://localhost:8080/admin`, войти с локальным `ADMIN_TOKEN`, создать вопрос. Задайте начало и конец голосования; для телевизионного ролика используйте 60 секунд. Откройте полученную ссылку, выберите ответ и проголосуйте. Повтор после обновления страницы с другим вариантом должен показать, что первый ответ сохранён.

`http://localhost:8080/` без ID показывает пояснение, как открыть опрос по ссылке. Публичного просмотра результатов нет: он доступен только администратору. Локальные демонстрационные секреты из `.env.example` не подходят для публично запущенного сервиса.

## Запуск Go на хосте

Требуется Go 1.26.9+; базы можно оставить в Compose:

```bash
docker compose up -d --wait control shard-0 shard-1
set -a
. ./.env
set +a
go run ./cmd/server
```

Если контейнер `server` уже работает, сначала `docker compose stop server`, чтобы освободить 8080. Нативный процесс использует localhost URL из `.env`, а контейнер использует имена сервисов внутри Docker-сети.

## Тесты

```bash
go test -race ./...
sh scripts/test-integration.sh
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

Первый вызов без test DB env запускает unit и HTTP tests; DB-тесты явно помечены `SKIP`. Скрипт integration задаёт адреса **локальных** Compose баз и включает DB-тесты; сами базы должны уже работать после `docker compose up --build -d --wait`. Не направляйте test env в production: тесты создают свои UUID и временные ограничения для проверки rollback, затем очищают собственные строки. Существующие пользовательские опросы не удаляются.

Проверяются реальные требования:

| Инвариант | Проверка |
| --- | --- |
| Без cookie или с неверной подписью голос не пишется | API identity/validation tests |
| Авторизация перед созданием и результатами | API admin tests |
| Нельзя выбрать неизвестный вариант/дубли/слишком много вариантов | API tests, в том числе bit31 |
| Deadline определяется сервером | API tests будущего/завершённого окна |
| 24 параллельных создания с одним ключом дают один опрос | PostgreSQL integration |
| 16 конкурентных batches одного набора voters дают один ballot на voter | PostgreSQL integration |
| Счётчики обновляются вместе с ballot | Ошибка counter CHECK откатывает receipt |
| Повтор не меняет ответ, multi-choice считается корректно | Batch replay / reconnect integration |
| Частичная сумма не выдаётся как полный результат | Unavailable shard tests |
| URL alias одной базы отклоняется | Persisted shard identity integration |
| URL не может выключить durable commit | `SHOW synchronous_commit` = `on` при URL override `off` |
| Перегрузка ограничивает memory queue и допускает retry | Ingest backpressure / drain tests |
| Неизвестный outcome не меняет cookie/key/body | HTTP load-client lost-ack tests |
| Генератор замечает неверный counter по одному варианту | HTTP client corruption test |

GitHub Actions запускает форматирование, `go vet`, Docker build, реальные DB tests с race detector, smoke и vulnerability scanner. Текущий статус следует проверять у конкретного commit в Actions; архив локального вывода сам по себе не заменяет CI.

## Нагрузочный прогон

```bash
sh scripts/benchmark.sh 10000 128
# Длинный прогон, использованный для evidence:
docker compose exec -T server load -mode=benchmark -voters=500000 \
  -concurrency=128 -duplicate-every=5 -poll-duration=30m
```

Генератор создаёт отдельный опрос и проверяет idempotent create replay. Каждая итерация получает свой настоящий cookie jar через HTTP session endpoint, читает metadata и отправляет один голос. Каждый пятый voter повторяет голос с другим ответом и тем же cookie. Ожидаемые total и counts всех вариантов сравниваются с административными результатами. При mismatch/failure программа возвращает ненулевой exit code.

Это closed-loop нагрузка: 128 workers ждут ответа перед новым voter. Она может скрыть перегрузку и не воспроизводит независимые 100 млн arrivals за минуту. `accepted_per_second` — число ответов `201` на elapsed полного voting workflow; безопасно восстановленный после неизвестного commit `200` относится к `already_voted` и включается в сверку результата. `vote_latency_ms` измеряет POST первого голоса с повторами, а elapsed дополнительно включает GET metadata, session и duplicate checks. Admin create и финальное чтение counters находятся вне elapsed.

Прогон 9 октября 2026 использовал Go 1.26.9, PostgreSQL 17.11, pgx 5.9.2, две voting базы, Docker/Colima Linux VM с 2 CPU и 2 053 644 288 bytes RAM (1,91 GiB). Сервер, генератор и все БД делили эту VM. `BATCH_SIZE=256`, `BATCH_WAIT_MS=5`, `WORKERS_PER_SHARD=4`, `QUEUE_SIZE=4096`, `MAX_DB_CONNS=8`. Primary `fsync=on`, `synchronous_commit=on`; replicas отсутствовали. Зависимости дополнительно обновлены до x/crypto 0.57.0, x/text 0.42.0 и x/sync 0.23.0.

Результат: 500 000 новых ballots, 100 000 проверенных повторов, 0 failures, counts_match=true, 43,3715с, 11 528,3 accepted/s, POST p99 14,3522ms. Равные counts по четырём вариантам отражают детерминированный выбор генератора `index%4`; это не результат реального социологического опроса.

На другую машину эти цифры автоматически не переносятся. Синхронная репликация, холодный кэш, другой выбор вариантов и рост числа API nodes меняют результат. [capacity.md](capacity.md) описывает open-loop распределённые испытания, необходимые перед эфиром.

## Проверка отказа и перезапуска

В локальном Compose можно остановить один shard: `docker compose stop shard-1`. `/healthz` остаётся `200`, `/readyz` возвращает `503`; административные результаты возвращают `503 incomplete_results`. Восстановить: `docker compose start shard-1`, дождаться `/readyz`. Сервис не возвращает неполную сумму. Эти операции затрагивают только локальный demo-проект.

Перезапуск `docker compose restart server` сохраняет polls, ballots и counters в PostgreSQL volumes. Runtime очереди в памяти не переживают остановку; неподтверждённый запрос следует повторить с прежней cookie. `201` после WAL commit и неизвестный transport outcome — разные состояния.

`GET /metrics` защищён тем же bearer token; доступны accepted, duplicate, rejected, errors, batches и pending queue gauge. Это основа наблюдения, а не полный production monitoring stack.

## Публичный HTTPS стенд

9 октября 2026 на [Render demo](https://efir-tvpoll-emelvv.onrender.com) выполнен низкообъёмный функциональный smoke: `go run ./cmd/load -base=https://efir-tvpoll-emelvv.onrender.com -mode=smoke -voters=10 -concurrency=5 -duplicate-every=2 -poll-duration=10m`, с публичным demo admin token из README. Он проверил создание и replay, 10 голосов, 5 изменённых повторов и каждый counter; total10, counts `[3,3,2,2]`, failures0. [Raw result](evidence/hosting-smoke.json).

Внутренний браузер затем отправил один голос в первый вариант и повтор во второй после reload. UI подтвердил сохранение первого ответа, admin UI и API показали total11, counts `[4,3,2,2]`. [Runtime record](evidence/hosting-runtime.jsonl). Проверены `204` от session, атрибуты `HttpOnly; Secure; SameSite=Lax` и `401` от admin API без токена: [security evidence](evidence/hosting-security.json). Это проверка настоящего HTTPS backend и PostgreSQL; cloud capacity benchmark не запускался. Снимок `hosting-admin.jpg` показывает этот результат.

После успешной выкладки commit `d999f33901e732fa8bc339e8cfd7cc48eb25fb03` проверены тот же ID и неизменные total11 / counts `[4,3,2,2]`. CI runtime commit завершился success. Затем через admin UI создан A/B опрос `04fdba1a-3bd8-4bae-b232-b77ec414c240`, в нём тот же browser profile успешно проголосовал за «Комфорт»; UI и API показали total1 / counts `[0,1]`. Poll-scoped дедупликация позволяет этому браузеру участвовать в другом опросе. [Browser record](evidence/hosting-browser.json).

Снимки `hosting-voting.jpg`, `hosting-create.jpg`, `hosting-admin.jpg` сделаны непосредственно с публичного HTTPS стенда. Daily poll в снимке главной не был отправлен этим браузером. Консоли голосования и admin UI не содержали warning/error. Позднейшие commits evidence/docs не меняют исполняемый runtime этого deployment.

## Скриншоты локального стенда

Сняты браузером с работающего локального сервиса, без редактирования чисел или DOM:

- `admin-login.jpg` — вход с пустым полем токена.
- `admin-create.jpg` — заполненная форма и список реальных опросов.
- `admin-results.jpg` — 500 000 голосов из длинного прогона, по 125 000 в каждом варианте.
- `voting-desktop.jpg`, `voting-mobile.jpg` — минутный demo-опрос; мобильная проверка viewport 393×852.
- `vote-success.jpg` — успешная отправка дополнительного browser vote.
- `vote-duplicate.jpg` — повтор с другим вариантом в том же браузере.

Минутный demo сначала получил 240 голосов от генератора. Браузер затем добавил один голос в первый вариант, а повтор во второй оставил total=241 и counts=[61,60,60,60]. Его raw исходный прогон сохранён в `evidence/demo.json`, а итог браузерного сценария отдельно. Ссылки на эти исторические demo polls действуют только в базе, где снимались изображения; в новом запуске нужно создать новый опрос.

Браузерная консоль после создания, просмотра, desktop/mobile voting и повтора не содержала error/warn. Внешние ресурсы интерфейс не загружает. Темы выбираются через `prefers-color-scheme`; снимки сделаны в тёмной теме.
