# Размещение демо на Render

Docker image запускает тот же Go-сервис, что используется локально. PostgreSQL остаётся настоящей базой, а не браузерной имитацией. `PORT`, предоставляемый hosting platform, поддерживается; явный `HTTP_ADDR` имеет приоритет.

## Опубликованный стенд

9 октября 2026 опубликован **[efir-tvpoll-emelvv.onrender.com](https://efir-tvpoll-emelvv.onrender.com)**. [Админка](https://efir-tvpoll-emelvv.onrender.com/admin) принимает публичный playground token из README. Вопросы и ответы этого стенда — тестовые.

- Render Free Web Service, Docker, Frankfurt: 0,1 CPU / 512 MB, `$0/month`.
- Render Free PostgreSQL 17.11, Frankfurt: 0,1 CPU / 256 MB, 1 GB; dashboard указывает expiry **8 ноября 2026**.
- Metadata database фактически называется `control_cy0e` (Render добавил suffix). Дополнительные `votes_0`, `votes_1` созданы provisioning-командой. Все три находятся на одном instance.
- Сервис использует HTTPS, `COOKIE_SECURE=true`, `/readyz`, отдельные приватные HMAC secrets и внутренние TLS PostgreSQL URLs. Payment method не добавлялся; выбран бесплатный compute без autoscaling.
- HTTP smoke создал настоящий опрос, принял 10 разных cookie-сессий и проверил 5 повторов с изменённым ответом: counts `[3,3,2,2]`, total `10`, failures `0`. [Raw smoke](evidence/hosting-smoke.json), [provisioning evidence без credentials](evidence/hosting-provision.json).

Это проверка функциональности небольшой облачной машины. Высокая нагрузка на Free-стенде не запускалась; локальный benchmark и национальный capacity plan описаны отдельно.

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

При воспроизведении deployment используйте URL и expiry из dashboard своего стенда: новая база получит собственное имя и срок жизни.
