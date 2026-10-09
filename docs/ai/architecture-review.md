# AI artifact: architecture review

## Задание агента

> Независимо проработай и критически проверь архитектуру задания 100 млн анонимных голосов за 60с. План root: Go net/http, PostgreSQL pgx, шардирование HMAC voter digest, bounded queues microbatch INSERT ON CONFLICT atomic ballots + sharded lane counters in same SQL transaction, admin metadata in control PG, signed browser cookie per poll, no IP uniqueness. Создай docs/architecture.md и docs/capacity.md. Сначала исследуй официальные первичные источники через web: PostgreSQL ON CONFLICT/transaction durability, Go server. Явно отличи реализовано от production proposed, target от measured. Рассчитай 1.67m/s, peaks, ingress requests UI, storage replica, bottlenecks, rollout/failover limits. Не пиши вне своих docs файлов и не отправляй никаких внешних сообщений. Дай конкретные замечания root. Файлы ИИ тоже по завершении docs/ai/architecture-review.md с prompt/decisions/sources.

Агент работал как технический редактор и reviewer. Отправлены только внутренние сообщения координации реализации; внешние комментарии, письма и сообщения людям не отправлялись.

## Принятые решения и замечания

- Dedup unit — браузер с сохранённой подписанной cookie. Открыто описаны обходы cookie/private mode/другим устройством.
- Один digest всегда маршрутизируется в один shard. Уникальность обеспечивается PostgreSQL, а не процессной памятью или предварительным SELECT.
- Counter delta строится из `INSERT ... RETURNING`, поэтому duplicate не влияет на итог.
- Проверена опасность одинакового modulo для shard и lane. Реализация использует worker index для lane; совпадение IDs между API process явно отражено как contention risk и причина production writer groups.
- При неизвестном исходе commit безопасен только повтор того же digest. HTTP-ошибка не доказывает отсутствие голоса.
- Если понадобился исходный ответ при duplicate, новый SELECT выполняется после вставки; CTE со старым снимком может не увидеть конкурирующую строку.
- Bounded channel не заменяет ограничение одновременно открытых requests, server timeouts и DB timeout.
- 1,67 млн/с — средний target, ×5 — допущение о кратковременном пике. Production hardware count не объявлен измеренным.
- Full first-visit flow содержит GET metadata, отдельный POST session и POST vote: три origin requests на нового участника. С CDN metadata остаются два origin requests. CDN нельзя кэшировать private response с одной cookie для всех.
- Pools ко всем shard из каждого API process масштабируют connections и дробят batches; production требует организованного fanout.
- Local durable commit и сохранение подтверждённого голоса при потере primary — разные требования. Второе требует synchronous replica, fencing и проверенного failover.
- Live results across shards не имеют единого distributed snapshot. Final results требуют завершённых записей и здоровых всех shard.
- Повтор после closes_at возвращает 409, в том числе после неизвестного успешного commit. Это не потеря голоса, но ограничение восстановления клиентского подтверждения.
- Все nodes должны использовать одинаковые secrets, число и порядок shard URL. Выявленная опасность URL-алиасов устранена проверкой persisted shard_identity на startup.
- После чтения реализации замечен handler wait без явного timeout: HTTP WriteTimeout не отменяет Request.Context. Замечание передано ведущему агенту; реализован отдельный five-second confirmation timer с `503 outcome_unknown`.
- На соединениях явно принудительно устанавливается `synchronous_commit=on`; `fsync=on` остаётся необходимой настройкой самого PostgreSQL.

## Проверенные первичные источники

Проверка официальной документации выполнена 9 октября 2026 года. Для PostgreSQL использованы versioned PostgreSQL 16 pages, чтобы не ссылаться на функции будущей версии.

1. [PostgreSQL 16 INSERT](https://www.postgresql.org/docs/16/sql-insert.html): конфликт по unique key, `DO NOTHING`, значение `RETURNING`, concurrent UPSERT и ограничения batch.
2. [PostgreSQL 16 transaction isolation](https://www.postgresql.org/docs/16/transaction-iso.html): READ COMMITTED snapshots и невидимая конфликтующая строка при `DO NOTHING`.
3. [PostgreSQL 16 WAL settings](https://www.postgresql.org/docs/16/runtime-config-wal.html): fsync, synchronous_commit, local vs remote durability.
4. [Go net/http Server](https://pkg.go.dev/net/http#Server): header/body timeouts, ограничение headers, shutdown и обслуживание HTTP.

## Граница доказательств

Арифметика capacity plan проверена независимо. Значения bytes/ballot, WAL amplification, per-shard throughput, коэффициент пика, число replicas и доля загрузки — плановые допущения, а не результаты benchmark. Документы не приписывают локальному прогону national-scale throughput.

Сверены `internal/api`, `internal/ingest`, `internal/store`, SQL schema и server/config: отдельная session cookie, READ COMMITTED replay create, deterministic batch ordering, mask, worker lanes, частичные результаты и закрытие окна. Точные проведённые тесты и измеренные нагрузочные результаты приводятся в остальных артефактах репозитория. Сохранены полученное задание, принятые решения, источники и ограничения; скрытое внутреннее состояние модели не записывалось.

## Продолжение: OpenAPI и повторная сверка

Полученное поручение:

> Please create docs/openapi.yaml for actual API if can save root time; inspect api/model code and update architecture docs exact changes: handler explicit 5s timeout now done, validatePoll allows past window so admin replay closed works; persisted shard identity protection being added by storage agent. Prepare comprehensive critique of UI/load once available but only editing openapi/docs your files. Include OpenAPI 3.0 schema paths session/admin/poll/vote/results/metrics/health ready, security admin bearer and voter signed cookie, all status and Idempotency-Key required, RetryAfter and identity errors. Add prompt AI artifact.

После прерывания поручение подтверждено:

> После прерывания задача осталась активной. Продолжи последний порученный docs/openapi.yaml + docs fixes. Все runtime patched Go1.26.9 pgx5.9.2, xcrypto0.57 text0.42; govulncheck No vulnerabilities found. Integration all8 DB tests passed, docs/evidence logs. Сверь persisted identity и synchronous_commit. Готовые ранее docs остаются. Не трогай чужие файлы.

Создан OpenAPI 3.0.3 контракт на основе фактических handlers/models: восемь paths, девять операций, cookie и bearer security, все явные HTTP statuses, обязательный Idempotency-Key, cache/ETag, admission/confirmation errors и ограничения повторов. Сверены [официальная спецификация OpenAPI 3.0.3](https://spec.openapis.org/oas/v3.0.3.html) и [официальная документация Swagger cookie authentication](https://swagger.io/docs/specification/v3_0/authentication/cookie-authentication/).

Проверка локальным Ruby YAML parser подтвердила синтаксис, разрешимость всех локальных `$ref` и уникальность operationId. Полный внешний OpenAPI validator не запускался. Python PyYAML в стандартном runtime отсутствовал; установка новой зависимости для этой проверки не понадобилась.

Повторная сверка подтвердила five-second timeout, persisted shard UUID alias check, принудительный synchronous_commit и допустимость replay create после закрытия. Документы явно требуют дополнительной production writer routing infrastructure и не обозначают её реализованной.

При чтении UI/load переданы ведущему агенту замечания: fixed retry interval без jitter; отображение admin idempotency conflict общим сообщением о закрытии голосования; session failure с необходимостью refresh; ограничение UI 16 вариантов при API 32; создание нового idempotency key после ошибки refresh, случившейся уже после известного успешного создания. Эти исходные UI-файлы агент архитектуры не изменял. Closed-loop и измерение только vote latency/rate пояснены в capacity.md.
