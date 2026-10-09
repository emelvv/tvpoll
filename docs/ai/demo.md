# Артефакт ИИ: живое ежедневное демо

## Поручение

```text
User requested live hosted demo and signed into Render/IAB. Implement OPTIONAL DEMO_MODE=true in same Go app so public root remains interactive throughout hosting30days, actual real Go voting no mock. Scope internal/demo/* newpackage, internal/config addition validated bool DemoMode defaultfalse+tests, cmd/server lifecycle hook/wrapper only (no otherapi/store/web/docrootchanges). Design: background seeder runs atstartup and each minute; creates one fixed demo poll per UTCday via Store.CreatePoll same idempotencykey fixed canonicalbody, OpensAt UTCday0, ClosesAt nextUTCday <=24h, min1/max1, 4transportoptions questionprefix'Демонстрационный опрос: ...'. ID deterministic canonicalUUIDcustomv8 derivedSHA256(day+'efir-demo-v1'), optionalrandomID withcached returnedPoll fine idempotency. Seeder atomically cachespoll; GET /demo only READ pointerand302redirect '/?poll=id', GET / with no pollquery whenDEMO_MODE true redirects /demo; NOcreation in public GET route. Seed contextbounded2sec/DBTimeout, asyncerrorgenericlogno creds, continuepreviousseed; expiredcachedpoll /demo503 friendly retry ratherthanredirectdeadpoll. Worker cancellable Close/WG beforeStore.Close. Defaultfalse ALL existingbehaviorunchanged. Add meaningfultests repeatseeding/dayrolloveronepoll+windowexact24h, wrapperreadGET doesn'twrite, falseconfig, noexpiredballotlink, seederClose. Record prompt/decisions docs/ai/demo.md. Go1269 /tmp caches. RootconfigPORTchangedalready don'tclobber. NewdemoREADMEwillstate dailywindowonlydemo. Keepcodelean avoidextraUI changes. RootcreatesRender resources whileyouimplement.
```

## Решения и границы

- `DEMO_MODE` выключен по умолчанию. Некорректный boolean отклоняется при загрузке конфигурации. Обычный локальный и production API сохраняет существующее поведение.
- Включённый режим создаёт настоящий опрос через тот же PostgreSQL `Store.CreatePoll`, без подмены голосов или результатов. Текущий UTC-день определяет неизменяемый idempotency key и UUIDv8, поэтому перезапуск или несколько экземпляров приложения получают один и тот же опрос.
- Вопрос и четыре варианта фиксированы и помечены как демонстрационные. Окно длится ровно 24 часа — от одной полуночи UTC до следующей. Минутное окно обычных опросов не меняется; длительность 24 часа относится только к удобству открытого демо.
- Seeder запускается асинхронно при старте и повторяется раз в минуту. Каждое обращение к Store имеет deadline не больше двух секунд и учитывает меньший `DB_TIMEOUT_MS`. Ошибки пишутся в лог общим сообщением, без текста connection errors и учётных данных.
- `GET /demo` читает только текущий poll из защищённой mutex памяти и отвечает `302` на настоящую страницу `/?poll=UUID`. Главная без параметра `poll` переводит на `/demo`; прямые ссылки, `/admin` и API проходят в исходный handler.
- HTTP-запрос не создаёт опрос. При отсутствующем, ещё не открытом или уже закрытом seed `/demo` возвращает понятный `503` с `Retry-After`, а не ссылку на завершённый ballot. Redirect имеет `Cache-Control: no-store`, чтобы ссылка не задерживалась в кэше после смены дня.
- После сбоя seeder сохраняет предыдущий poll, пока его окно действительно. На смене дня при минутном интервале возможна короткая пауза до следующего успешного seed. Демо не обещает отсутствие этой паузы или доступность во время отказа БД.
- При завершении приложения `Close` отменяет context и ждёт завершения goroutine до закрытия PostgreSQL pools. Дублирующий вызов безопасен.

## Проверки

Тесты используют fake Store и HTTP recorder; проверяют реальные границы компонента: UTC при другой часовой зоне, окно ровно 24 часа, повторное создание с тем же key/body, один poll на день, смену дня, сохранение успешного seed после ошибки, отсутствие записей из GET/HEAD, передачу admin/API/явных poll links прежнему handler, отказ выдавать будущую или закрытую ссылку, ограниченный deadline и ожидание отменённой фоновой операции в `Close`.

Команды воспроизведения:

```sh
go test -race ./internal/demo ./internal/config
go test -race ./...
go vet ./internal/demo ./internal/config ./cmd/server
```

09.10.2026 выполнено: `go test -race ./internal/demo ./internal/config`, полный `go test -race ./...`, `go vet ./internal/demo ./internal/config ./cmd/server` — успешно. Полный запуск включает HTTP regression suite; DB integration tests требуют отдельно заданные `TEST_*` URL и в этом запуске не заявляются повторно проверенными. Использован Go 1.26.9 во временной папке и временный build cache. Форматирование и `git diff --check` прошли.

Сам компонент не развёртывает инфраструктуру. URL работающего hosted demo и проверку настоящего PostgreSQL/vote API фиксирует основной агент после публикации.
