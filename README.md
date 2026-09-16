# plata-test-task

[![CI](https://github.com/djsega1/plata-test-task/actions/workflows/ci.yml/badge.svg)](https://github.com/djsega1/plata-test-task/actions/workflows/ci.yml)

Асинхронный сервис котировок валют. Дизайн и его обоснование — в `docs/design.md`, раскладка файлов
и команды сборки/тестов — в `CLAUDE.md`.

## Быстрый старт (Docker)

```bash
cp .env.example .env   # опционально — по умолчанию всё работает офлайн, ключ не нужен
docker compose up -d --build   # или: task docker:up
./scripts/smoke.sh
```

Поднимается PostgreSQL 18, сервис (`PROVIDER=fake` по умолчанию — внешняя сеть и API-ключ не нужны)
и Swagger UI на `http://localhost:8081`. Скрипт дожидается `healthy` и проходит все три бизнес-ручки
насквозь: `POST /api/v1/quotes/updates` → поллинг `GET /api/v1/quotes/updates/{id}` до `succeeded` →
`GET /api/v1/quotes/latest`.

Чтобы обратиться к реальному апстриму: выставить `PROVIDER=exchangeratedev` в `.env` (всё ещё
анонимно — `PROVIDER_API_KEY` опционален, см. `docs/design.md` §2) и пересобрать.

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

## Локальная разработка (без Docker)

Команды `go build`/`go vet`/`go test`/`golangci-lint` и запуск локального кластера PostgreSQL без
Docker — в `CLAUDE.md`.

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
