# Размещение демо на Render

Docker image запускает тот же Go-сервис, что используется локально. PostgreSQL остаётся настоящей базой, а не браузерной имитацией. `PORT`, предоставляемый hosting platform, поддерживается; явный `HTTP_ADDR` имеет приоритет.

## Конфигурация

Для временного free demo подходят один Render Free Web Service и один Free Postgres instance с тремя logical databases: `control`, `votes_0`, `votes_1`. Разные logical databases на одном PostgreSQL instance не являются физически независимыми shards и не увеличивают суммарную мощность машины. Локальный Compose использует три отдельных PostgreSQL контейнера; production должен распределять shards по рассчитанной инфраструктуре.

Render официально разрешает дополнительные базы через `CREATE DATABASE`: [multiple databases](https://render.com/docs/postgresql-creating-connecting#adding-multiple-databases-to-a-single-instance). Создайте свежие `votes_0`, `votes_1`; не клонируйте мигрированный voting shard вместе с его `shard_identity`.

Для отдельной новой demo instance можно использовать `go run ./cmd/provision-demo`: он читает внешний TLS URL control базы из `DATABASE_URL` и создаёт только `votes_0`, `votes_1`, если их ещё нет. Секретный URL в stdout не выводится. Этот provisioning-инструмент предназначен для выделенного стенда; основной сервер базы данных не создаёт.

Для Free Web Service выбирайте Docker runtime, root `Dockerfile`, тот же регион, что и Postgres, health path `/readyz`. Исходники берутся из `https://github.com/emelvv/tvpoll`.

| Environment | Значение |
| --- | --- |
| `CONTROL_DATABASE_URL` | Внутренний PostgreSQL URL базы `control` |
| `SHARD_DATABASE_URLS` | Внутренние URL `votes_0,votes_1` в постоянном порядке, через запятую |
| `PUBLIC_ORIGIN` | Точный HTTPS origin созданного web service |
| `COOKIE_SECURE` | `true` |
| `ADMIN_TOKEN` | Отдельный секрет минимум32 bytes; demo credential может быть публичным только для изолированного тестового стенда |
| `COOKIE_SECRET`, `DEDUP_SECRET` | Разные случайные значения минимум32 bytes, хранятся только в environment хостинга |
| `WORKERS_PER_SHARD`, `MAX_DB_CONNS` | Для малой demo instance:2,4 |
| `QUEUE_SIZE`, `MAX_IN_FLIGHT` | Для demo:1024,1024 |
| `DEMO_MODE` | `true` только на публичном demo-стенде |

Runtime secrets не передаются в Docker build arguments и не включаются в Git. `.env.*` игнорируются, исключение — публичный `.env.example`. Миграции выполняются автоматически при старте.

В `DEMO_MODE=true` фоновый seeder создаёт один настоящий демонстрационный опрос на UTC-день, с окном24часа. `/` и `/demo` перенаправляют к нему: сайт остаётся интерактивным при визите на следующий день. GET не создаёт опросов. Создание своих опросов через admin API, минутные окна и дедупликация работают как обычно. По умолчанию режим выключен; production и локальный Compose не создают демонстрационные данные автоматически.

## Бесплатные ограничения

Free Postgres имеет1GB и истекает через30дней после создания; затем14дней grace period перед удалением без upgrade. Free Web Service засыпает после15минут простоя и просыпается примерно за минуту. Локальные файлы ephemeral, поэтому polls/votes хранятся в PostgreSQL. Эти ограничения подтверждены [официальными Free docs](https://render.com/docs/free).

Не включайте paid compute, storage autoscaling или платные backups без отдельного решения владельца аккаунта. При подключённом payment method overage bandwidth/build minutes может тарифицироваться; без него платформа приостанавливает free resources при исчерпании лимитов. Для долгоживущего демо понадобится постоянная база или согласованный тариф.

## Проверка после deploy

1. HTTPS `/healthz` и `/readyz` отвечают200.
2. Создание через admin API и replay одного `Idempotency-Key` дают один ID.
3. Session выдаёт `HttpOnly; Secure; SameSite=Lax` cookie.
4. Первый vote даёт201, повтор с другим вариантом —200; aggregate total увеличился только один раз.
5. Admin UI показывает этот результат; credentials другого local environment не подходят.
6. После redeploy/restart предыдущие polls и results сохраняются.

Дату фактического expiry и опубликованный URL следует брать из dashboard текущего deployment. План конфигурации сам по себе не доказывает, что demo уже опубликовано; фактический URL и проверенный статус указываются в README после успешного deploy.
