# Журнал основной работы

Дата: 9 октября 2026. Инструмент: OpenAI Codex, основной агент и три рабочих агента. Пользователь потребовал полный backend, GitHub, открытые AI artifacts и реальные screenshots.

## Принятые решения

- Самостоятельный репозиторий `tvpoll`; посторонние материалы рабочей папки в него не попадают.
- Go `net/http`, pgx, control DB и несколько voting DB; один SQL commit объединяет receipts и counters. Redis/Kafka не добавлены между независимыми необъединёнными записями.
- Browser cookie, подписанная HMAC, и отдельный poll-scoped digest. Отказ от IP uniqueness из-за NAT. Ограничения cookie-based защиты явно задокументированы.
- Microbatch workers, capped pools, bounded queues/admission, HTTP/DB timeouts, 5s ожидание подтверждения и graceful drain.
- Админский bearer token, идемпотентное создание, неизменяемые polls, приватные агрегированные results, CDN-cacheable public metadata.
- Русский интерфейс «Эфир» без внешних assets; токен только в памяти. OpenAPI, CI, отчёты и README вместе с кодом.

## Замечания, которые привели к изменениям

1. `SELECT existing` в том же CTE при `ON CONFLICT DO NOTHING` может не увидеть конкурентный commit. Storage использует только `RETURNING` новых ballots; create replay читает победителя отдельным statement.
2. Совпадающий порядок locks важен при overlapping batches. Вставки receipts и обновления counters отсортированы.
3. `Server.WriteTimeout` не задаёт срок ожидания handler. Добавлен явный таймер5с; admitted ballot продолжает запись, неизвестный outcome повторяется с той же cookie.
4. Replay создания после закрытия блокировался проверкой «close должен быть в будущем». Статическая валидация разрешает прошлое окно; voting admission всё равно проверяет текущее время.
5. URL aliases одной физической базы могли дважды суммировать counters. Добавлена persisted shard UUID identity и startup rejection.
6. Durable acknowledgement не должен зависеть от URL `synchronous_commit=off`. Runtime setting принудительно `on`, добавлен real DB test. `fsync` остаётся prerequisite серверной конфигурации PostgreSQL.
7. UI имел максимум16 вариантов при API32, общий текст409 для разных ошибок и synchronized retry. Согласованы32, введены сообщения по error code, jitter и повтор bootstrap session.
8. Ошибка refresh списка после успешного create могла выглядеть как неуспешное создание. Известный success сохранён; ошибка refresh отображается в списке.
9. Первый cookie-tamper тест заменял начальный символ на тот же символ. Исправлен тест: теперь изменяется действительно другой байт. Подпись отвергает изменение.

## Проверки и версии

Начальные Go1.26.8/pgx5.7.6/transitive modules прошли функциональные tests, но официальный `govulncheck` сообщил13 reachable vulnerabilities: в стандартной библиотеке, x/text и pgx. Это не было скрыто или проигнорировано. Зафиксированы Go1.26.9, pgx5.9.2, x/crypto0.57.0, x/text0.42.0, x/sync0.23.0; повторный scan вывел `No vulnerabilities found`.

Официальные источники: [Go downloads](https://go.dev/dl/), [Go release history](https://go.dev/doc/devel/release), [pgx changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md), [PostgreSQL17.11 release notes](https://www.postgresql.org/docs/17/release-17-11.html), [Go vulnerability database](https://pkg.go.dev/vuln/).

Локально не было Go в PATH и Compose plugin. Официальный Go и Docker Compose5.6.0 скачаны во временную папку с проверкой SHA-256. Docker ссылался на отсутствующий credential helper; для публичных image pulls использована отдельная временная конфигурация без изменения настроек пользователя. Docker/Colima VM:2 CPU,1,91GiB RAM.

- `go test -race -count=1 -cover -v ./...` с настоящими PostgreSQL.
- `go vet ./...`, `gofmt`, `git diff --check`, синтаксис JS.
- HTTP smoke100 с20 repeats.
- Прогон10000, затем500000 unique sessions +100000 changed-answer retries; все counters совпали.
- Desktop/mobile UI: create, results, accepted, retry different choice; реальные screenshots и console check.
- Остановка одного локального shard, восстановление и перезапуск API с проверкой counters.

Raw evidence находится в `docs/evidence`, а методика — в `docs/verification.md`. Не утверждается, что локальная машина обслужила100млн голосов за минуту. Национальный target, ingress/WAL/storage budget и необходимость дополнительной routing/replication инфраструктуры раскрыты в capacity plan.

## Публикация

Пользователь явно запросил публичный GitHub-репозиторий. Создан `https://github.com/emelvv/tvpoll`, owner `emelvv`, visibility public. Код, документация, AI artifacts, tests и screenshots публикуются в нём; `.env` исключён. Сообщения людям, комментарии и обращения во внешних сервисах не отправлялись.

## Размещение живого демо

Дополнительное поручение пользователя: найти хостинг и разместить интерактивную демоверсию. Выбран Render Docker + настоящая PostgreSQL; выбор и актуальные официальные ограничения записаны в [hosting-research.md](hosting-research.md). Sites hosting был рассмотрен, но его Workers runtime не исполняет этот Go-сервис.

Пользователь явно разрешил GitHub login / чтение email для Render, затем самостоятельно вошёл в Render и GitHub во внутреннем браузере. Созданы только Free resources во Frankfurt; payment method не добавлялся. При смене региона форма сбросила выбранный Free plan: попытка продолжения остановилась на Add Card до создания ресурса. Модальное окно закрыто, Free повторно выбран и `$0` проверен перед успешным созданием. Платный ресурс не создан, данные карты не вводились.

Control database Render получила имя `control_cy0e`. Инструмент `cmd/provision-demo` создал `votes_0`, `votes_1` через внешний TLS connection; подтвердил PostgreSQL17.11 и `fsync=on`. Web service использует только внутренние database URLs, private cookie/dedup secrets и намеренно публичный admin token из README. Private credentials хранятся вне репозитория и в environment Render; в evidence нет DSN, паролей, cookie values или приватных browser screenshots.

Commit `6710c8a9e6eaffeba838d02e496010cc4a01f6dd` получил успешный CI и был опубликован Render как Live. Проверен реальный HTTPS URL `https://efir-tvpoll-emelvv.onrender.com`. Публичный HTTP smoke:10 voters,5 изменённых повторов,0 failures,все четыре counters совпали. Это функциональный smoke, не cloud benchmark. Optional daily seeder и его тесты задокументированы в [demo.md](demo.md). Для 24-часового demo UI countdown дополнен форматом часов; минутные опросы продолжают показывать минуты и секунды.
