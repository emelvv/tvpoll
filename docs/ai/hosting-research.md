# AI artifact: hosting research

## Полученное поручение

> New user requirement: find hosting and deploy actual full Go+PostgreSQL demo, link README. Research official Render/Koyeb/Railway docs for $0 Go Docker + persistent PostgreSQL, account/signup billing limits, DB create db permissions for 3logicalDB onone freeinstance, expiry. No external changes or usermessages. Return concrete best option for user-ownedhostingaccount. NativeSites Workers-compatible only cannotrunexistingGoserver. Need realbackend not substitutebrowsermock/reimplementation. Could Render freeDockerweb +1PGwith3DB CREATEDATABASE permissions supportor1PGnamed3schemas modifications? Do not change code. Primarydocs onlyviaweb.

Проверка выполнена 9 октября 2026 года. Агент только читал официальные страницы и сохранил этот артефакт. Аккаунты, ресурсы и платежи не создавались; приложение и его код не изменялись.

## Вывод

Для временного демонстрационного развёртывания выбран **Render Free Web Service с существующим Dockerfile и один Free Render Postgres instance**. Нужен принадлежащий пользователю аккаунт. Это сохраняет исходный Go backend и настоящие PostgreSQL-транзакции. Новый browser-only вариант не нужен.

Render документирует создание дополнительных logical databases через `CREATE DATABASE`, используя предоставленный psql-доступ. Поэтому можно оставить исходную схему подключения: начальная база control и две новые базы votes_0/votes_1 на том же instance. В разделе документации нет исключения для Free plan; фактическое выполнение и права конкретного аккаунта нужно подтвердить при deployment. Три logical databases не являются тремя физическими независимыми shard.

## Сравнение вариантов

| Платформа | Почему подходит или не выбрана |
| --- | --- |
| Render | Docker/Go web и managed Postgres доступны на Free. Web засыпает после 15 минут без входящего трафика; пробуждение занимает около минуты. 750 web instance hours в месяц. База 1 GB истекает через 30 дней; потом 14 дней на платный upgrade перед удалением. Только один Free Postgres instance на workspace, но logical databases внутри него разрешены. Нет free backups. |
| Koyeb | Free web и PostgreSQL существуют, default DB role может создавать logical databases. Но free PostgreSQL ограничен пятью часами active compute в месяц и 1 GB. Signup требует карту: проверка через отменяемый $29 hold; официальная FAQ предупреждает, что выбранный при signup Pro plan немедленно получает prorated charge. Это хуже для задачи без расходов. |
| Railway | Free Trial не требует карты и позволяет backend/database. Выдаёт $5 на срок до 30 дней, после чего Free даёт $1 в месяц. На Free/Trial volume 0.5 GB. Trial volumes удаляются через 30 дней после окончания credits. Ограничения verification могут влиять на network access. Это бюджетная временная альтернатива, а не обещание бесплатного постоянно работающего Go+Postgres. |

Render Free без привязанного payment method при исчерпании included usage приостанавливает services/builds вместо оплаты overage. При добавленной карте overage bandwidth/build minutes может тарифицироваться. Официальный first-deploy guide говорит, что для его free resources оплата не требуется; login поддерживает GitHub, Google и другие providers. Фактический signup/account-verification flow агента не проходил.

## План конфигурации без изменения Go

1. В пользовательском Render workspace создать Free Postgres, назвать начальную logical database `control`, выбрать тот же регион, что и будущий web service.
2. Из локального psql через предоставленный внешний TLS URL создать `votes_0` и `votes_1` обычным `CREATE DATABASE`, без transaction wrapper. Не клонировать уже мигрированную voting database: в каждой новой базе должна появиться отдельная shard_identity.
3. Создать Free Web Service из сдаваемого GitHub repository, выбрать Docker runtime и существующий Dockerfile. Если использовать public repo URL, официально доступен ручной deploy, но Git-provider credentials нужны для automatic redeploy.
4. В environment задать `HTTP_ADDR=:10000`, точный HTTPS `PUBLIC_ORIGIN`, `COOKIE_SECURE=true`, три разных случайных secrets и connection strings. `CONTROL_DATABASE_URL` ведёт в `control`; два URL в `SHARD_DATABASE_URLS` ведут в `votes_0` и `votes_1` в постоянном порядке. Для соединений внутри одного Render региона использовать внутренние URL. Секреты не добавляются в публичный репозиторий.
5. Startup сервиса выполнит штатные миграции. Настроить health path `/readyz`, проверить публичные `/healthz` и `/readyz`, создать опрос через настоящий admin API, отправить голос с cookie, повторить его и проверить результаты. Далее проверить сохранение результата после restart/redeploy.
6. В README записать фактический HTTPS URL и дату истечения базы из dashboard, ограничения cold start и то, что demo не является national-scale инфраструктурой. На момент исследования live URL ещё не создан и тесты hosting не проводились.

Render load balancer сам завершает TLS и передаёт HTTP к контейнеру. Binding `:10000` совместим с требованием host `0.0.0.0` и default Render port 10000; изменение Go для этого не требуется. Сам контейнер может иметь ephemeral filesystem: polls/votes хранятся в managed PostgreSQL.

Для срока больше 30 дней потребуется иной бесплатный внешний PostgreSQL либо согласованный платный upgrade; исследование не обещает постоянную бесплатную базу Render. Дополнительно найден свежий официальный [Neon Free plan announcement от 2 октября 2026](https://neon.com/blog/neon-free-plan-1-gb-per-project): 1 GB/project и 100 CU-hours/project/month. Это возможный отдельный вариант для дальнейшей проверки; его user account, logical database provisioning и интеграция здесь не проверены.

## Первичные источники

- [Render: Deploy for Free](https://render.com/docs/free) — expiry, sleep, quotas, persistence и billing behavior.
- [Render: Create and Connect to Render Postgres](https://render.com/docs/postgresql-creating-connecting#adding-multiple-databases-to-a-single-instance) — multiple logical databases, psql и internal/external URLs.
- [Render: Web Services](https://render.com/docs/web-services) — Docker runtime, public URL, port и HTTPS termination.
- [Render: Your First Deploy](https://render.com/docs/your-first-deploy) — account/repository flow, free resources без оплаты.
- [Render: FAQ](https://render.com/docs/faq) — overage при наличии карты, suspension без payment method.
- [Render: Login Settings](https://render.com/docs/login-settings) — поддерживаемые login providers.
- [Koyeb: Databases](https://www.koyeb.com/docs/databases) — free DB limits и default-role permissions.
- [Koyeb: Pricing FAQ](https://www.koyeb.com/docs/faqs/pricing) — card hold, default Pro charge, free service.
- [Railway: Free Trial](https://docs.railway.com/pricing/free-trial) — credits, verification, expiry, data retention.
- [Railway: Pricing FAQ](https://docs.railway.com/pricing/faqs) — trial without credit card.
- [Railway: PostgreSQL](https://docs.railway.com/databases/postgresql) — настоящая PostgreSQL service и соединения.
- [Railway: Volumes](https://docs.railway.com/volumes/reference) — persistent volume и Free/Trial limits.

Условия платформ меняются. Совпадение общих docs с доступными конкретному аккаунту опциями должно быть проверено перед созданием resources. Это исследование не является доказательством фактического deployment.
