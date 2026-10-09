# Промпты рабочих агентов

Пользовательский запрос сохранён в [task.md](task.md). Ниже — задания, которыми основной агент разделил работу; последующие изменения зафиксированы в журналах каждого направления и в [work-log.md](work-log.md).

## Архитектура и независимое ревью

```text
Независимо проработай и критически проверь архитектуру задания 100 млн анонимных голосов за 60с.
План root: Go net/http, PostgreSQL pgx, шардирование HMAC voter digest,
bounded queues microbatch INSERT ON CONFLICT atomic ballots + sharded lane counters
in same SQL transaction, admin metadata in control PG, signed browser cookie per poll,
no IP uniqueness. Создай docs/architecture.md и docs/capacity.md.
Сначала исследуй официальные первичные источники через web: PostgreSQL ON CONFLICT/
transaction durability, Go server. Явно отличи реализовано от production proposed,
target от measured. Рассчитай 1.67m/s, peaks, ingress requests UI, storage replica,
bottlenecks, rollout/failover limits. Дай конкретные замечания root.
Файлы ИИ тоже по завершении docs/ai/architecture-review.md с prompt/decisions/sources.
Не отправляй никаких внешних сообщений.
```

Уточнения: отдельно `GET metadata`, `POST /api/session`, `POST vote`; проверка таймаута ожидания, правильности SQL при конкурентном конфликте, alias одного shard, contention lanes, стабильности ключей и карты маршрутизации. Поручена машинная спецификация `docs/openapi.yaml` по актуальным обработчикам.

## PostgreSQL storage

```text
Реализуй storage для Go сервиса. Только internal/store/*, tests там, docs/ai/storage.md.
Существующие internal/model/model.go types. Go 1.26 pgx/v5.
New(ctx,controlURL,shardURLs,maxConns); Close; Ping; CreatePoll с ключом идемпотентности;
GetPoll; ListPolls; WriteBatch(ctx,shard,lane,ballots) возвращает accepted aligned;
Results с суммой всех shard; Shards.
Control metadata store Postgres, immutable polls JSON and sha canonical payload excluding
generated ID for create idempotency, unique idempotency key. Automatic startup migrations
using advisory lock transaction control and each shard. votes table poll_id uuid,
voter_digest bytea PK poll_id+digest, mask bigint, received_at timestamptz.
CTE batch INSERT ON CONFLICT DO NOTHING RETURNING, counters updated atomically same
query/transaction aggregated only inserted rows, total option_index=-1 plus each bit0..31,
lane per worker. Counters ordered to avoid deadlock; no cross DB FK.
Accepted from inserted digest in memory; same-batch duplicate first true only.
Do not SELECT existing from same CTE. Pools capped.
Results fanout parallel, fail if any missing, explicitly not a global snapshot.
Integration tests: race, retries, multi-choice, idempotent create conflict,
persisted counts, unavailable shard. Schema checks digest length32 and mask>0.
Record prompts and decisions. No external messages.
```

Уточнения: persisted UUID identity каждого shard для запрета разных URL одной физической базы; bounded rollback; принудительный `synchronous_commit=on`; доказательство rollback ballot при ошибке counter update.

## Интерфейс, контейнеры и нагрузочный клиент

```text
Сделай frontend и load tool + локальный запуск для Go проекта.
Только web/*, cmd/load/*, Dockerfile, compose.yaml, .dockerignore, scripts/*,
docs/ai/frontend-load.md, .env.example.
API: POST /api/session cookie, GET /api/polls/{uuid}, POST /votes {choices:[0,1]},
201 accepted / 200 already_voted /400 invalid /409 closed /403 cookie /503 retry samecookie.
POST /api/admin/polls Bearer token and required Idempotency-Key,
GET admin polls and aggregate results, 503 if shard fails.
Voting page /?poll=uuid and /admin with token in memory; create/list/results.
Tasteful Russian interface, accessible CSS keyboard, no external assets/CDN,
XSS safe textContent, no inline JS (CSP script-src self).
Retries reuse cookie; multi-choice percentages share of voters (can total above100%).
Docker Go and PostgreSQL; one control database + two independent vote databases,
volumes, healthchecks, bind127.0.0.1 ports8080/15432/15433/15434.
Load Go HTTP CLI: create poll, real per-voter cookie session, concurrent voting,
duplicate-every retries, admin counts check, JSON p50/p95/p99 and failures.
Do not imply local proof of100M votes/min. Record prompts and decisions.
No external messages.
```

Уточнения пользователя: бренд интерфейса «Эфир», реальные screenshots для README, согласование лимита 32 варианта с API, отдельная обработка admin idempotency conflict, jitter повторов, тесты HTTP-клиента.

## Основной агент

Основной агент реализовал HTTP API, конфигурацию, подписанные cookie и HMAC, bounded ingestion, graceful shutdown и тесты этих модулей; объединил результаты; выполнил запуск, проверки, съёмку UI и публикацию в запрошенный GitHub-репозиторий. Уточнения версии и исправления перечислены в [work-log.md](work-log.md).
