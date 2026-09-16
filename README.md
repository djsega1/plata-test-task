# plata-test-task

[![CI](https://github.com/djsega1/plata-test-task/actions/workflows/ci.yml/badge.svg)](https://github.com/djsega1/plata-test-task/actions/workflows/ci.yml)

Асинхронный сервис котировок валют. Дизайн и его обоснование — в `docs/design.md`; команды сборки,
тестов и работы с БД — в разделе «Команды» ниже.

## Быстрый старт (Docker)

```bash
cp .env.example .env   # опционально — по умолчанию нужна только внешняя сеть, ключ не нужен
docker compose up -d --build   # или: task docker:up
./scripts/smoke.sh
```

Поднимается PostgreSQL 18, сервис (`PROVIDER=exchangeratedev` по умолчанию — реальный апстрим,
работает анонимно, `PROVIDER_API_KEY` опционален, см. `docs/design.md` §2) и Swagger UI на
`http://localhost:8081`. Скрипт дожидается `healthy` и проходит все три бизнес-ручки насквозь:
`POST /api/v1/quotes/updates` → поллинг `GET /api/v1/quotes/updates/{id}` до `succeeded` →
`GET /api/v1/quotes/latest`.

Чтобы прогнать полностью офлайн, без сети и ключа: выставить `PROVIDER=fake` в `.env` и пересобрать.

`task docker:down` останавливает связку; добавить `-v` к `docker compose down`, чтобы ещё и удалить
том БД.

## API

`api/openapi.yaml` описывает все пять ручек (три бизнес-, `/healthz`, `/readyz`) — форму
запроса/ответа, коды статусов и разделение `400`/`422` (невалидная форма пары vs пара вне
allow-list), см. `docs/design.md` §4.

`docker compose up` (или `task docker:up`) поднимает вместе с сервисом и интерактивный UI по спеке
на `http://localhost:8081`.

«Try it out» реально бьёт в поднятый бэкенд (как встроенный `/docs` у FastAPI), а не только рисует
спеку: сервис отдаёт CORS-заголовки, а `servers.url` в спеке — абсолютный адрес самого API
(`localhost:8080`), а не `localhost:8081`, откуда отдаётся сама страница; `/healthz`/`/readyz` и
бизнес-ручки резолвятся из одного и того же адреса сервера каждый по своему полному пути.

## Схема БД

Две таблицы (`internal/storage/postgres/migrations/00001_init.sql`); полное обоснование каждого
решения — `docs/design.md` §5.

**`quotes`** — append-only журнал цен, строка = цена пары на момент времени у источника:

| колонка | назначение |
|---|---|
| `id` | `BIGSERIAL` PK |
| `base`, `quote` | `CHAR(3)`, валютная пара |
| `rate` | `NUMERIC(24,10)` — деньги никогда не `float64` |
| `provider`, `quality` | источник и его метка свежести (`live` / `ecb_daily` / `fred_daily`) |
| `derived`, `indicative` | цена посчитана как `1/rate`; является ли индикативной |
| `quoted_at` | время цены **у источника** — по нему определяется «последняя» котировка |
| `fetched_at` | время, когда сервис её прочитал (может быть позже `quoted_at`) |
| `stale_after` | до этого момента поход к апстриму за этой парой не нужен |

Уникальный индекс `(provider, base, quote, quoted_at)` — дедуп; `(base, quote, quoted_at DESC, id
DESC)` — быстрый поиск «последней» котировки пары.

**`quote_updates`** — она же очередь фоновых задач, `FOR UPDATE SKIP LOCKED`-клейм воркерами:

| колонка | назначение |
|---|---|
| `id` | `UUID` PK, `uuidv7()` — упорядочен по времени вставки |
| `base`, `quote` | запрошенная пара |
| `status` | `pending` → `in_progress` → `succeeded` / `failed` |
| `source` | `api` (клиентский запрос) / `scheduler` |
| `idempotency_key` | опциональный, `UNIQUE` частичный индекс — дедуп повторной отправки клиента |
| `attempts`, `next_attempt_at` | счётчик и время следующей попытки (backoff) |
| `locked_at` | когда воркер заклеймил строку — по нему реапер находит зависшие `in_progress` |
| `quote_id` | `FK` на `quotes`, когда `status = succeeded` |
| `error_code`, `error_message` | когда `status = failed` |

Частичные индексы по `next_attempt_at WHERE status = 'pending'` и `locked_at WHERE status =
'in_progress'` держат очередь маленькой независимо от того, насколько вырос журнал. Почему claim,
работа и завершение — три отдельные транзакции, а не одна: `docs/design.md` §3.

## Команды

Полный список задач — `Taskfile.yml` (или `task --list`).

**Сборка и проверки** (то же самое, что гоняет CI):

```bash
task build           # go build ./...
task fmt             # gofmt -l . — должно быть пусто
task fmt:fix         # gofmt -w .
task vet             # go vet, включая integration-tagged файлы (без БД)
task lint            # golangci-lint run
task lint:fix
task test            # go test ./... — обязан проходить без БД
task test:race
task test:cover
task test:integration   # нужен TEST_DATABASE_URL, например postgres://postgres@localhost:5432/quotes
task check           # fmt + vet + lint + test — всё, что должно быть зелёным перед коммитом
```

Один пакет или один тест — напрямую через `go test`, без `task`:

```bash
go test ./internal/provider/...
go test ./internal/provider/ -run TestRateParsesResponse
```

**Docker-связка:**

```bash
task docker:up       # docker compose up -d --build
task docker:down     # docker compose down (добавить -v — ещё и удалить том БД)
task docker:logs     # docker compose logs -f app
task smoke           # scripts/smoke.sh — сквозной прогон трёх бизнес-ручек
task throughput      # scripts/throughput.sh — см. «Пропускная способность» ниже
```

**Прямой доступ к БД** (при поднятой `task docker:up`):

```bash
docker exec -it plata-test-task-db-1 psql -U quotes -d quotes
```

**Интеграционные тесты без Docker** — нужен локальный PostgreSQL 18+; на Debian/Ubuntu без него
поднять временный кластер можно так:

```bash
sudo -u postgres /usr/lib/postgresql/18/bin/initdb -D /tmp/pgb/data -U postgres --auth=trust
sudo -u postgres /usr/lib/postgresql/18/bin/pg_ctl -D /tmp/pgb/data \
  -o '-p 5433 -k /tmp/pgb/sock -c listen_addresses=' -l /tmp/pgb/pg.log start
sudo -u postgres /usr/lib/postgresql/18/bin/createdb -p 5433 -h /tmp/pgb/sock quotes

TEST_DATABASE_URL='postgres://postgres@localhost:5433/quotes' task test:integration
```

## Пропускная способность

Не частота HTTP-запросов, а сколько `quote_updates` фоновый конвейер реально доводит из `pending`
в `succeeded` за 10 секунд — именно то число, ради которого архитектура из `docs/design.md`
(очередь в Postgres, пул воркеров, single-flight по паре) и построена. Измерено `scripts/
throughput.sh`: POST-only нагрузка (`cmd/throughput`, 50 конкурентных воркеров, 20 секунд,
`PROVIDER=fake`) против поднятой связки, затем прямой запрос к `quote_updates`/`quotes` за
реальной скоростью дренирования. Разбор — `docs/design.md` §8; сырые данные по секундам —
`docs/perf/2026-09-15-throughput/` и `docs/perf/2026-09-15-throughput-fixed/`.

| | до фикса | после фикса |
|---|---|---|
| создано → succeeded | 11928 / 50205 | **49216 / 49216** |
| зависло в очереди | 38277 | **0** |
| лучшее окно за 10 секунд | 7845 котировок/10с | **14708 котировок/10с** |

Прогон нашёл реальный потолок, не синтетический: `Dispatcher.Run` заново клеймил батч только по
`nudge` или пятисекундному тику, так что после прекращения `POST`-трафика большой бэклог дренировался
строго `DISPATCH_BATCH_SIZE / DISPATCH_TICK_INTERVAL` = 200 котировок/10с — независимо от размера
очереди и от того, насколько простаивал пул из 8 воркеров. Исправлено: `Run` теперь сам продолжает
клеймить, пока батч приходит полным (`internal/usecase/quotes/dispatcher.go`, тест
`TestDispatcher_Run_FullBatchKeepsDrainingWithoutWaitingForNudgeOrTick`). За весь прогон к провайдеру
реально сходили всего 6 раз (по одному на пару) — single-flight + `stale_after` переиспользуют одну
и ту же котировку для всех остальных запросов по паре в её пределах.

```bash
docker compose up -d --build
./scripts/throughput.sh   # CONCURRENCY / LOAD_DURATION / DRAIN_TIMEOUT переопределяются через env
```

## Что можно улучшить

**Распределённый rate limiting / single-flight (Redis).** `RateLimiter` и single-flight по паре у
`Worker` (`internal/usecase/quotes/ratelimiter.go`, `worker.go`) — оба process-local: при нескольких
репликах `cmd/server` каждая соблюдает свой бюджет 12/мин + 100/час и свой дедуп по паре, независимо
от остальных. Общий лимитер на Redis (или Postgres advisory lock по паре, в рамках уже имеющейся
зависимости) позволил бы репликам координироваться, чтобы N реплик вместе соблюдали один бюджет и не
дублировали поход за одной и той же парой одновременно.

Это оптимизация, не исправление корректности. Две реплики, одновременно потянувшие `EUR/USD`, всё
равно каждая честно запишут валидную строку `quotes` — либо продедуплицированную по `(provider,
base, quote, quoted_at)`, если `quoted_at` совпал, либо две соседние строки, если нет — ничего не
ломается, данные не теряются и не портятся. Единственная цена при нескольких репликах — лишние
походы наружу и трата бюджета лимитера, не некорректный результат. Не реализовано: это добавило бы
новую рантайм-зависимость (Redis) сервису, который по умолчанию однореплична
(`docs/design.md` A6), и не требуется скоупом задания.
