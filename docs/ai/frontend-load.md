# Артефакты ИИ: интерфейс, локальный запуск и load client

Работа выполнена Codex в отдельной задаче агента `frontend_load`. Ниже сохранены исходное поручение, уточнения и принятые решения. Исходный код, тесты и этот журнал входят в сдаваемый репозиторий. Личные данные, приватные токены и содержимое окружения не копировались в журнал.

## Исходный промпт делегированной задачи

```text
Сделай frontend и load tool+локальный запуск для Go проекта /Users/emelvv/Documents/ChatGPT/учеба/tvpoll. Только web/*, cmd/load/*, Dockerfile, compose.yaml, .dockerignore, scripts/*, docs/ai/frontend-load.md, .env.example. API: POST /api/session sets signed HttpOnly voter cookie, returns 204. GET /api/polls/{uuid} model.Poll {id,question,type='single'/'multiple',options []string,min_choices,max_choices,opens_at,closes_at ISO8601}; POST /api/polls/{id}/votes JSON {choices:[0,1]} uses cookie credentials; 201 {status:'accepted'}, 200 {status:'already_voted'}, 400 invalid,409 closed,403 no cookie,503 overload retry same cookie. POST /api/admin/polls Bearer ADMIN_TOKEN, required Idempotency-Key uuid client generated; body question,type,options,min_choices,max_choices,opens_at,closes_at;201 poll/200 replay. GET /api/admin/polls -> {polls:[...]}; GET /api/admin/polls/{id}/results -> {poll:poll,total_votes,option_counts,observed_at,consistent:false}, 503 if shard fails. / UI for voting link /?poll=uuid; /admin login token only in memory, create/list/results; dark/light tasteful clean Russian interface, accessible CSS keyboard, no external assets/CDN, XSS safe via textContent, no inline JS (CSP script-src self). Poll page separate from admin: no token saved localStorage, cookie required before submit, retries reuse cookie; results include multichoice percentages share of voters and no pie summing >100. web embed later root decides (web package assets.go embed). Docker Go 1.26.8 postgres17.6-alpine local 1 control PG + 2 vote shard PG separate volumes health checks, service port8080, localPG ports 15432/15433/15434 bind127.0.0.1, env config root expects CONTROL_DATABASE_URL,SHARD_DATABASE_URLS comma, ADMIN_TOKEN min32 COOKIE_SECRET min32 DEDUP_SECRET min32 COOKIE_SECURE false local, BATCH_SIZE256 BATCH_WAIT_MS5 WORKERS_PER_SHARD4 QUEUE_SIZE4096 MAX_DB_CONNS8. .env.example only dummy clearly local random-like values, docs copied env. cmd/load Go HTTP CLI creates poll via admin, closed loop named smoke benchmark, sessions real network cookie individual, -voters -concurrency -base -token env -duplicate-every repeats cookie, checks admin total equals unique accepted, output JSON summary latencies failures. Avoid falsely imply target100m load generated local. CI can run go tests root. Record own prompts/decisions. No messages external.
```

## Уточнения основного агента

```text
Подтверждаю server ./cmd/server, web.Files() fs.FS. Config PUBLIC_ORIGIN=http://localhost:8080 addition for CSRF. COOKIE secure false local explicitly. Docker no bind source full compose. HTTP cookie session no JSON body needed csrf Origin enforced. Load same header token env not CLI token output. Port readiness endpoints /healthz (liveness), /readyz all DB Ping; /metrics authenticated admin token plain Prometheus. Graceful shutdown drains accepted queues before pools close.
```

```text
Пользователь требует README скриншоты реального UI и всего для выделения. Root снимет CUA после запуска. Пожалуйста UI очень презентабельный, с демонстрационным живым poll, админ создание/результат состояния, хороший wide+mobile дизайн. Хотел бы brand «Эфир / Efir» в UI, имя repo tvpoll. Сохрани реализм, no hardcoded fake totals.
```

```text
Root changed postgres image17.11-alpine (official latest17 minor Oct09), Docker up building live now. UI limits please make2..32 match API (currently16) and results user explanation avoid implementation 'шарды' (use 'Во время голосования результат предварительный. Итог — после завершения и обработки всех принятых запросов.'). No changes Docker after building until confirm agent done. Need load main_test meaningful tests status retry using same cookie / idempotency body retry if ambiguous create initial request transport fails currently createPoll doesn't retry initial; user goal no mandatory CLI robust but UI preserves good. Test closed-loop handling HTTP503 cookie reuse and changed-option duplicate via httptest fake endpoints, success total checks. Please doc frontend-load prompt+decisions while finishing.
```

```text
Reviewer now checks common.js: 409 always renders 'poll closed', but admin /create 409 idempotency_conflict requires different user msg. Please parse server error.code/'error' field with dictionary mapped code, fallback message friendly. Add jitter small random to vote UI 503 retry interval to avoid synchronized burst; session503 button disabled until reload maybe retrybutton visible allow same cookie bootstrap only before firstvote not when vote503. Don't featurecreep if finish. Root store synchronous_commit fix.
```

В финальной интеграции основной агент обновил Docker-образ Go до `1.26.9-alpine`, PostgreSQL до `17.11-alpine`. Это уточнение заменяет версии из первоначального промпта; файлы Docker после него не менялись агентом интерфейса.

## Реализованные решения

- Интерфейс «Эфир» использует системные шрифты, собственный CSS, адаптивную сетку и автоматическую светлую/тёмную тему. Внешних изображений, CDN, библиотек JavaScript или сборщика нет. Статические файлы встроены через `go:embed`.
- Текст опросов и ответов добавляется через `textContent` и созданные DOM-узлы, без `innerHTML`. Скрипты подключены отдельными модулями: это совместимо с `script-src 'self'`.
- Публичная страница открывает опрос по `/?poll=UUID`, создаёт анонимную сессию до голосования и проверяет число выбранных ответов. Сервер определяет, открыт ли опрос, и валидирует запрос независимо от браузера.
- Повтор при `503` использует тот же cookie и тот же набор ответов, ограничен тремя попытками, учитывает `Retry-After` и небольшой случайный сдвиг. Кнопка повторного создания сессии относится только к её первоначальному получению; она не вызывается при повторе голоса. `200 already_voted` сохраняет первый ответ.
- Админский токен хранится в переменной модуля до закрытия страницы или выхода. Он не записывается в cookies, localStorage или sessionStorage. Поле ввода очищается после передачи токена в память. Список и результаты доступны только после проверки токена сервером.
- Создание использует `crypto.randomUUID()` в `Idempotency-Key`. При неоднозначном исходе формы повтор с прежним payload сохраняет ключ, изменение payload создаёт новый ключ. Ответы API переводятся в понятные сообщения, включая различие закрытого опроса и конфликта ключа создания.
- Для нескольких ответов график показывает долю участников по каждому варианту. Сумма долей может превышать 100%; круговая диаграмма не используется. Во время голосования результат явно назван предварительным.
- Compose содержит отдельные volume и readiness-проверки для control PostgreSQL и двух PostgreSQL с голосами. Локальные порты связаны только с `127.0.0.1`. `.env.example` содержит заведомо публичные значения только для локальной демонстрации. Они не подходят для публичного развёртывания.
- `cmd/load` выполняет **closed-loop** HTTP-проверку: создаёт свежий опрос и проверяет replay создания, заводит отдельный cookie jar для каждого зрителя, отправляет голос и повторяет каждый N-й голос с другим вариантом. Сверяет общее число и распределение вариантов, чтобы обнаружить как дубли, так и замену первого ответа.
- Load client повторяет неопределённый исход записи с прежним cookie, а создание — с прежними ключом и body. Если после потерянного подтверждения первая отправка вернулась как `200 already_voted`, участник учитывается как успешно сохранённый уникальный голос. В отчёте отдельно видны `accepted` и `already_voted`.
- Отчёт JSON содержит число ошибок, количество повторов, p50/p95/p99/max полной задержки первой операции голосования, throughput принятых ответов и сверку статистики. Токен не попадает в JSON, сообщения ошибок или значения по умолчанию в `-help`.
- Локальный load test не доказывает способность принять 100 миллионов голосов за минуту. Его модель нагрузки и это ограничение обозначены прямо в коде, JSON и скрипте benchmark. План проверки требуемой мощности описывает основной агент в архитектурной документации.

## Проверка

`cmd/load/main_test.go` использует настоящий HTTP поверх `httptest.Server`: инъекция `503` до записи, потерянное подтверждение после записи, повтор создания после потерянного подтверждения, изменённый выбор при повторном голосовании и повреждённые счётчики результата. Проверяются повторное использование cookie, неизменные idempotency key/body, отсутствие второй записи и обнаружение неверных counts. Интеграционные и визуальные результаты общего проекта фиксируются в основном отчёте тестирования.

Проверено 09.10.2026: `go test -race ./cmd/load` успешно; `node --check` для `web/common.js`, `web/admin.js`, `web/vote.js` успешно. Go-тесты запускались с разрешённым локальным loopback-портом после ограничения bind в песочнице. Использован временный toolchain Go 1.26.9 и временный build cache; эти файлы не входят в репозиторий.

Локальные команды для воспроизведения:

```sh
go test -race ./cmd/load
docker compose up --build -d --wait
sh scripts/smoke.sh
sh scripts/benchmark.sh 10000 128
```

Последние две команды используют `ADMIN_TOKEN` из окружения контейнера; значения секретов не передаются параметрами командной строки. Для запуска вне Docker переменную `ADMIN_TOKEN` необходимо задать в окружении.
