# Currency Quotes Service — дизайн

Асинхронный сервис котировок: клиент просит обновить курс пары и получает идентификатор обновления,
само обновление идёт в фоне, затем клиент забирает результат или последнюю котировку пары.

Документ описывает текущую архитектуру и её контракт — состояние на сейчас, не историю изменений.

---

## 1. Задача и допущения

Из ТЗ: `POST` обновить котировку по коду пары → идентификатор; `GET` получить котировку по
идентификатору; `GET` получить последнюю котировку пары. БД — PostgreSQL, обновление в фоне,
источник цен — внешний API, ограниченный набор валют.

| # | Допущение | Почему |
|---|---|---|
| A1 | «Код валюты» = валютная пара `BASE/QUOTE` (`EUR/MXN`) | котировка бессмысленна без второй стороны; в ТЗ пример именно пары |
| A2 | Allow-list `USD, EUR, MXN` → 6 упорядоченных пар | прямо разрешено ТЗ; заодно валидация входа |
| A3 | Котировка — одно число (mid), без bid/ask | публичные FX API отдают mid и честно пишут «indicative, not for settlement» |
| A4 | «Время обновления» = время котировки у источника (`quoted_at`); отдельно храним `fetched_at` | это разные вещи, и для финансов важна первая |
| A5 | Результат клиент забирает поллингом | ТЗ прямо описывает «через некоторое время — запрос на получение» |
| A6 | Один инстанс — типовой сценарий, но архитектура переживает рестарт и вторую реплику | дёшево заложить сразу, дорого чинить потом |

---

## 2. Источник: `exchangerate.dev`, ручка `/v1/rate/{slug}`

| Провайдер | Ручка | Ключ | Свежесть | Метаданные | Лимит (free) |
|---|---|---|---|---|---|
| **exchangerate.dev** (выбран) | `/v1/rate/{base}-{quote}` — пара за вызов | не нужен (аноним) | `live` для торгуемых пар, иначе дневной фиксинг (`source`) | `data_updated_at` отдельно от `timestamp`, `derived`, `indicative` | 12/мин, 100/час анон |
| exchangerate-api.com v6 | `/v6/{key}/pair/{base}/{quote}` или `.../latest/{base}` | обязателен даже на free-плане | раз в сутки на free-плане | практически нет — курс и дата | ограниченная квота/месяц |
| Frankfurter | `/v1/latest?base=X&symbols=Y` | не нужен | раз в сутки, ЕЦБ, только будни | нет вообще | не документирован |

Exchangerate-api отпадает по ключу — требование «проверяющему ключ не нужен» жёсткое. Frankfurter
отпадает по A3/A4: без `source`/`quality` пришлось бы либо выдумывать признак, либо всегда считать
курс суточным фиксингом — разница между `live` и дневной ценой (важная на выходных) пропала бы.
`exchangerate.dev` даёт `data_updated_at`, `derived` и дисклеймер об индикативности анонимно, без
компромиссов.

**Пара за вызов, не `/v1/latest`.** `/v1/latest` отдал бы все ~30 валют разом — эффективнее по сети,
но: (1) `/rate` уже возвращает готовый кросс-курс с меткой `derived`, не пришлось бы считать его
самим; (2) фоновая обработка — предмет демонстрации задания, и с одним вызовом на все пары пул
воркеров, single-flight и ограничитель остались бы декорацией без реальной нагрузки. Цена — шесть
вызовов вместо одного и реальный лимит (см. «Лимиты» ниже), сознательный trade-off.

При десятках/сотнях пар выбор был бы обратным: `/latest` — один вызов вместо N, лимит перестаёт быть
проблемой, а кросс-курсы для пар вне прямого ответа пришлось бы считать самим. Порт
(`RateProvider.GetCurrencyRate`) и отдельный пакет адаптера держат эту замену заменой одного
адаптера, не переписыванием сервиса.

**Лимиты.** Анонимно — 12/мин, 100/час на IP; с ключом — 10 000/мес. Шесть пар при свежести 60с — 6
вызовов/мин (укладывается) и 360/час (в 3.6 раза больше анонимного часового лимита). Ограничитель
считает оба окна сам (скользящее окно), не по заголовкам ответа — чтение заголовков привязало бы
`RateProvider`'s сигнатуру к одному источнику. Без ключа `stale_after`/`QUOTE_TTL` поднимается так,
чтобы 6 пар не превышали 100/час (≈216с).

**Что берём из ответа.**

```jsonc
{
  "rate": 1.0824,                            // цена → NUMERIC/decimal, никогда не float64
  "data_updated_at": "2026-06-16T14:01:41Z", // → quoted_at, «время обновления» из ТЗ
  "timestamp": "2026-06-16T14:02:11Z",       // время ответа, не время котировки
  "source": "live",                          // live | ecb_daily | fred_daily → quality
  "derived": false,                          // триангуляция на стороне источника
  "market_session": "open",                  // не используется: см. «Свежесть» ниже
  "notice": "Indicative rates, not for settlement."
}
```

**Свежесть — метод адаптера, не общая функция.** `source` сообщает происхождение курса, не гарантию
времени жизни, так что `RateProvider.StaleAfter(quality, quotedAt)` — часть адаптера, у другого
провайдера может быть другой словарь `quality`. У `exchangerate.dev`: 60с для `live`, сутки для
`ecb_daily`/`fred_daily`, консервативный `QUOTE_TTL` из конфига для остального. `market_session` не
используется: живой субботний ответ показал `live`-курсы тикающими и на выходных при
`market_session: weekend` — поле оказалось контекстом, не сигналом свежести.

**Ключ.** Анонимный режим основной. Если задан (`PROVIDER_API_KEY`, только через env), адаптер
передаёт его по правилам своего провайдера — деталь адаптера, не контракта. `PROVIDER=fake` даёт
полностью офлайновый прогон.

**Транспортные ошибки классифицируются, а не пробрасываются сырыми.** Сбой `httpClient.Do` (DNS,
TLS, таймаут) заворачивается в `ProviderUnavailableError` (retryable) — иначе `Worker.fail` не
распознал бы ошибку, строка осталась бы `in_progress` до реапера бесконечно, без `error_code`.
Исключение — уже завершённый `ctx` вызывающего: идёт наверх непронормированным. Тело ответа читается
через `io.LimitReader` (1 МиБ) — иначе сломанный апстрим мог бы вычитывать неограниченный поток.

---

## 3. Архитектура

```
POST /updates ─▶ хендлер: INSERT quote_updates(pending) ─▶ 202 + update_id  (в сеть не ходит)
                     └─ неблокирующий «пинок» воркеру
                            ▼
   Диспетчер: пинок ∪ тикер ∪ reaper ──▶ claim: UPDATE ... FOR UPDATE SKIP LOCKED
                            ▼
   Воркер: now < stale_after ? взять существующую котировку : сходить к провайдеру
                            ▼
   PostgreSQL: quotes (журнал) + quote_updates (очередь и результат)

GET /updates/{id}, GET /latest ──▶ только чтение из БД
```

Слои: `api/http` → `quotes` (домен и юзкейсы) → порты, реализованные в `storage/*` и `provider/*`.
Зависимости направлены внутрь, порты объявляет потребитель.

---

## 4. Контракт API

Базовый путь `/api/v1`, JSON. Ошибки единым конвертом: `{"error":{"code":"...","message":"..."}}`.

**Пара — строка через `/` (`EUR/MXN`), не через `-`.** Дефис у `exchangerate.dev` — формат адаптера,
не контракта. На границе `pair` разбирается на `CurrencyPair`; дальше по системе ходит только
типизированная пара.

**`POST /quotes/updates`** — тело `{"pair":"EUR/MXN"}`, опционально заголовок `Idempotency-Key`.

```http
202 Accepted
{"update_id":"6f1e…","pair":"EUR/MXN","status":"pending","created_at":"2026-09-12T10:00:00Z"}
```

`400` — тело не парсится, `pair` пусто, или строка не имеет формы `BASE/QUOTE`
(`ErrMalformedPair`). `422` — форма верная, но `base`/`quote` вне allow-list или `base == quote`
(`ErrPairNotAllowed`). `409` — тот же ключ с другой парой; `200` + `Idempotency-Replayed: true` —
повтор с тем же ключом.

`Idempotency-Key` действует в пределах этого эндпоинта — уникальность среди обновлений котировок,
не глобально. Появятся другие идемпотентные операции — в индекс добавится `scope`.

**`GET /quotes/updates/{id}`** — `200` с полем `status`, `404` на неизвестный или неразбираемый `id`
(для клиента оба неотличимы). `500`, залогированный, — на сбой репозитория: маскировать его под
«нет такой заявки» значило бы обмануть клиента, вежливо прекращающего поллинг по `404`. Не готово —
`Retry-After: 1`, фиксированный пол, не привязанный к `DISPATCH_TICK_INTERVAL` (5с, §6).

```jsonc
// обязательный минимум из ТЗ — price и quoted_at; остальное дополнение
{"update_id":"6f1e…","pair":"EUR/MXN","status":"succeeded",
 "price":"18.4321000000","quoted_at":"2026-09-12T10:00:02Z",
 "fetched_at":"2026-09-12T10:00:02Z","provider":"exchangerate.dev",
 "quality":"live","derived":false,"indicative":true}

{"update_id":"6f1e…","pair":"EUR/MXN","status":"pending"}

{"update_id":"6f1e…","pair":"EUR/MXN","status":"failed","attempts":3,
 "error":{"code":"provider_unavailable","message":"upstream returned 503"}}
```

`indicative` сохраняется намеренно — терять признак «not for settlement» значило бы отдать цену без
её главного ограничения. `quality` отвечает «живая цена или суточный фиксинг», важно на выходных.

**`GET /quotes/latest?pair=EUR/MXN`** — та же форма без `status`/`update_id`: это запрос к журналу
(`quotes`), не к конкретному обновлению. `400`/`422` — то же разделение, что у `POST`; `404` — пары
ещё нет в журнале. Пара идёт query-параметром: слэш в сегменте пути требует кодирования.

Служебное: `GET /healthz`, `GET /readyz`.

**Конфигурация — из env плюс один флаг.** `--provider` имеет приоритет над `PROVIDER` из окружения —
чтобы менять провайдера в `command:` докер-compose одной строкой.

---

## 5. Схема данных

```sql
-- Котировки: append-only журнал, строка = цена пары на её время у источника
CREATE TABLE quotes (
    id          BIGSERIAL      PRIMARY KEY,
    base        CHAR(3)        NOT NULL,
    quote       CHAR(3)        NOT NULL,
    rate        NUMERIC(24,10) NOT NULL,
    provider    TEXT           NOT NULL,
    quality     TEXT           NOT NULL,
    derived     BOOLEAN        NOT NULL DEFAULT false,
    indicative  BOOLEAN        NOT NULL DEFAULT true,
    quoted_at   TIMESTAMPTZ    NOT NULL,                -- время котировки у источника
    fetched_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),  -- время получения записи
    stale_after TIMESTAMPTZ    NOT NULL                 -- до этого момента ходить бессмысленно
);
CREATE UNIQUE INDEX quotes_dedup_idx  ON quotes (provider, base, quote, quoted_at);
-- «Последняя» — по времени котировки у источника, не по времени запроса; id как tie-break
CREATE INDEX        quotes_latest_idx ON quotes (base, quote, quoted_at DESC, id DESC);

-- Обновления: они же очередь задач
CREATE TABLE quote_updates (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    base            CHAR(3)     NOT NULL,
    quote           CHAR(3)     NOT NULL,
    status          TEXT        NOT NULL,
    idempotency_key TEXT,
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at       TIMESTAMPTZ,
    quote_id        BIGINT      REFERENCES quotes (id),
    error_code      TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX quote_updates_idem_idx  ON quote_updates (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX        quote_updates_queue_idx ON quote_updates (next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX        quote_updates_stuck_idx ON quote_updates (locked_at)
    WHERE status = 'in_progress';
```

- **Очередь живёт в БД, а не в канале.** Клиенту уже отдан `update_id` — обещание должно переживать
  рестарт. `FOR UPDATE SKIP LOCKED` заодно делает корректной работу нескольких реплик.
- **`quote_id` ссылкой, а не копией цены.** Два обновления одной пары в пределах свежести указывают
  на одну котировку. Путь переиспользования (`CompleteSuccessReuse`) ничего не пишет в `quotes` —
  только `quote_id` у заявки: иначе каждое переиспользование (обычный случай для активной пары) было
  бы `INSERT ... ON CONFLICT DO UPDATE`, то есть запись/блокировка строки на каждый запрос, а не
  только на реальный поход к провайдеру.
- **Сортировка по `quoted_at`, не по `fetched_at`.** Провайдер может ответить курсом старше уже
  сохранённого (отстающая реплика, откат на суточный источник); «последняя» по `fetched_at` тогда
  была бы неверной. `fetched_at` остаётся в данных — виден возраст чтения и лаг источника.
- **Частичные индексы** держат очередь маленькой при миллионах строк в журнале обновлений.
  `ClaimBatch` клеймит `pending`- и `in_progress`-кандидатов двумя независимыми CTE (каждый — свой
  индекс, `FOR UPDATE SKIP LOCKED`, `LIMIT $1`), а не одним `WHERE ... OR ...`: с общим условием
  `SKIP LOCKED` ставит `LockRows` между `Sort` и `Limit`, блокируя top-N heapsort — Postgres
  сортирует весь `Bitmap Heap Scan`. Измерено: 2мс при 5к `pending`-строк, 117мс при 305к (сортировка
  на диск), оба числа растут с бэклогом, а не с `LIMIT`. Раздельные CTE ограничивают внешний
  `ORDER BY`/`LIMIT` максимум `2 * limit` строками — на том же сценарии 1.8мс, план `Index Scan` без
  полного сорта. `stuck_candidates` сортируется по `locked_at`: набор `in_progress`-строк ограничен
  ёмкостью воркеров, а не глубиной очереди, так что выбор конкретных кандидатов внутри лимита не
  влияет на итоговый приоритет — тот всё равно решает `next_attempt_at`.
- **Легальность перехода статуса решает домен, а не `WHERE`.** Оба `Repository` перед записью
  валидируют переход через `CurrencyRateUpdateStatus.CanTransitionTo`/`TransitionTo`, не полагаясь на
  то, что `UPDATE ... WHERE status = 'in_progress'` случайно кодирует то же правило второй раз. В
  `storage/postgres` — отдельный `SELECT` перед `UPDATE` в той же транзакции (`transitionRequest`);
  сам `WHERE` в `UPDATE` не убран — без блокировки `SELECT` не защищает от гонки с конкурентной
  транзакцией, только даёт содержательную ошибку вместо голого «0 строк». `ClaimBatch` —
  исключение: реклейм реапером не переход в терминах этой машины состояний, а построчная проверка на
  батч в 100 строк свела бы один `UPDATE ... FROM` к сотне запросов.
- **`id` — UUIDv7, не v4.** Генерируется в Go (`uuid.NewV7()`, до `INSERT`) — `202` должен вернуть
  `update_id` немедленно. v7 хранит таймстамп, вставки по первичному ключу остаются упорядоченными,
  не разбросаны по B-tree, как со случайным v4. Требует PostgreSQL 18+.
- **`NUMERIC`, не `float8`;** в Go `shopspring/decimal`, в JSON — строка, чтобы JS-клиент не потерял
  точность.
- **В схеме нет бизнес-логики.** `PRIMARY KEY`/`FOREIGN KEY`/`UNIQUE`/`NOT NULL` — гарантии,
  недоступные атомарно на уровне приложения (без `quote_updates_idem_idx` — гонка
  «прочитал-затем-вставил»). Никаких `CHECK` на словарь значений или диапазон — это проверяют
  Go-типы и конструкторы в `internal/domain/quotes` до записи.
- **`NewCurrencyRate` отвергает и `quoted_at`, не только `rate <= 0`.** Ноль (не разобрался
  `data_updated_at`) иначе прошёл бы тихо: `stale_after` оказался бы всегда в прошлом, строка никогда
  не стала бы «latest». Дата больше чем на `maxQuotedAtSkew` (5 минут) впереди `fetched_at` —
  зеркальный случай: `stale_after` уедет в будущее, котировка будет вытеснять настоящие свежие.

---

## 6. Компоненты и конфигурация

**Дальнейшее развитие** — не реализовано, при наличии времени:

| Что | Зачем |
|---|---|
| Метрики Prometheus | сейчас только структурные логи |
| Circuit breaker | декоратор над `RateProvider`, подключается на wiring в `cmd/server/main.go`. Пока не нужен: при шести парах пул из восьми воркеров не исчерпывается даже при полной недоступности источника — ограничителя и разделения retryable/permanent-ошибок достаточно |
| Второй адаптер | `exchangerate.dev` сам считает и помечает кросс-курс (`derived`) через `/rate` (§2); адаптер без такой ручки делал бы деривацию сам |

**Домен и хранилище.** `CurrencyRateUpdateRequest.Attempts/ErrorCode/ErrorMessage` — строки, тем же
типом, что принимает `Repository.CompleteFailure`. `CompleteSuccess` чистит `error_code`/
`error_message` при успехе, но не `Attempts` — это счётчик за всё время жизни заявки.

**Диспетчер.** `DISPATCH_TICK_INTERVAL` (5с), `DISPATCH_BATCH_SIZE` (100), `DISPATCH_POOL_SIZE` (8),
`DISPATCH_VISIBILITY_TIMEOUT` (30с), `DISPATCH_BASE_BACKOFF` (5с). `Dispatcher.Run` перезапускает
клейм сам, пока батч приходит полным, не дожидаясь следующего `nudge`/тика — иначе дренирование
бэклога было бы ограничено `DISPATCH_BATCH_SIZE/DISPATCH_TICK_INTERVAL` независимо от размера
очереди. Паника внутри `ClaimBatch` логируется на уровне `Run`, цикл продолжает со следующего
`nudge`/тика.

Каждый пасс `Run` — на `context.WithTimeout(context.WithoutCancel(ctx), DISPATCH_PASS_TIMEOUT)` (90с
по умолчанию, с запасом над `⌈100/8⌉ * 5с ≈ 65с`). `WithoutCancel` снимает только отмену через
shutdown — без своего таймаута зависший вызов к БД повесил бы горутину диспетчера навсегда.
`DISPATCH_MAX_ATTEMPTS` (10) — после скольких retryable-неудач `Worker.fail` помечает заявку
`failed` сама (`error_code = attempts_exhausted`), иначе постоянно недоступный апстрим держал бы
заявку в вечном цикле реклейма.

`DISPATCH_BASE_BACKOFF` растёт экспоненциально с числом попыток (5с, 10с, 20с...) до потолка
`DISPATCH_MAX_BACKOFF` (5м), затем поднимается выше только если у ошибки свой `Retry-After` длиннее.
С дефолтным потолком 10 попыток дают ~20 минут (5+10+20+40+80+160+300×3) — осмысленная граница
вместо фиксированного ритма по недоступному источнику.

**Наблюдаемость.** `Worker`/`RequestUpdate`/`Dispatcher` логируют через явный `*slog.Logger`: поход к
провайдеру и успех — `Info`, переиспользование котировки — `Debug`, исчерпание лимитера и отложенный
retryable-отказ — `Warn`, permanent-отказ — `Error`. `GetByUpdateID`/`GetLatest` не логируют — чтения
без побочного эффекта.

`requestIDMiddleware` даёт каждому HTTP-запросу `request_id` (доверяет входящему `X-Request-Id`,
иначе генерирует `uuid.NewV7()`), эхом отдаёт в ответе. Это id одного вызова, не `update_id`:
`postQuotesUpdatesHandler` логирует оба вместе одной строкой при создании заявки — это единственная
точка, через которую `request_id` из access-лога трассируется в `update_id`-логи `Worker`/
`Dispatcher`.

**Упаковка.** Dockerfile — `alpine`, не `distroless`: нужен шелл для смоука и `ca-certificates` для
`exchangeratedev`. Том Postgres монтируется в `/var/lib/postgresql`, не `.../data` (Postgres 18+
хранит данные в подкаталоге по версии). `DATABASE_URL`/`HTTP_ADDR` зафиксированы; остальное — через
`.env`.

**HTTP-клиент для `exchangeratedev`** собирается в `cmd/server`, не в адаптере — `pkg/httpclient`
даёт голый конструктор без дефолтов, числа даёт вызывающий код из конфига.

**Пул соединений к Postgres** — через `pgxpool.ParseConfig`: `POSTGRES_MAX_CONNS`,
`POSTGRES_MAX_CONN_LIFETIME`, `POSTGRES_HEALTH_CHECK_PERIOD` (0 — дефолт `pgxpool`). Та же забота о
транспорте, что и у `pkg/httpclient`, не то, что должен угадывать `storage/postgres`.

**Тело `POST /quotes/updates` ограничено `http.MaxBytesReader` (4 КиБ)** — оно одно строковое поле, и
`ReadTimeout` сам по себе объём не ограничивает.

**OpenAPI UI** — `swaggerapi/swagger-ui` в `docker-compose.yml`, версия зафиксирована тегом.
`corsMiddleware` отражает `Origin` (не `*` — не работает с credentialed-запросами) и отвечает на
preflight `OPTIONS` напрямую — оба нужны, чтобы «Try it out» доходил до бэкенда.

**Сопутствующие скрипты.** `cmd/throughput` + `scripts/throughput.sh` — измерение пропускной
способности конвейера, отдельная программа на stdlib, не часть сервиса. `scripts/smoke.sh` разбирает
JSON `grep`/`sed`, не `jq` — требование «ключ не нужен» распространяется и на утилиты.

**CI** — три джобы: `build-and-test` (gofmt, vet, lint, build, `test -race -cover`, без базы),
`integration-tests` (сервис-контейнер `postgres:18`), `docker-smoke` (`docker compose up` + тот же
`smoke.sh`, что и для ручной проверки).

---

## 7. Осознанно вне скоупа

Аутентификация и rate limiting на входе, retention журнала, трассировка, брокер сообщений, кэш
поверх БД, автоматический fallback между провайдерами (два источника разойдутся в цене, «молча взять
другой» отдало бы клиенту цену без объяснения происхождения), bid/ask и расчётные курсы Banxico/DOF
— понадобятся, если по цене начнут проводить операции, а тогда меняется не адаптер, а модель
котировки.

---

## 8. Ограничения проверки

`gofmt -l`, `go vet`, `golangci-lint`, `go test ./...` (включая `-race`) — зелёные без базы и в CI на
GitHub Actions; интеграционные тесты — под `//go:build integration`, пропускаются без
`TEST_DATABASE_URL`.

**Против настоящего PostgreSQL 18:** конкурентный `ClaimBatch` не выдаёт задачу дважды ни внутри
процесса, ни двум репликам; уникальность `idempotency_key`; reaper реклеймит зависшее; дедуп `quotes`
по `(provider, base, quote, quoted_at)`; миграции `Up`/`Down`/`Up`, конкурентные — под advisory lock.
Живым бинарником: `POST` → `Dispatcher.Run` → `succeeded` без ручного вмешательства; рестарт без
`-v` переживает том и котировки; graceful shutdown по `SIGTERM` укладывается в секунду.

**На подставных часах (`go test`):** single-flight сводит сто конкурентных запросов одной пары к
одному вызову провайдера; шесть пар обрабатываются параллельно; исчерпанный лимит переносит задачу,
не роняя её.

**Против настоящего `api.exchangerate.dev`, анонимно:** `POST` доходит до ответа и `succeeded`,
`GET /latest` отдаёт ту же цену; сетевой сбой оставляет строку `in_progress` до реклейма реапером.

**Нагрузочно** (`scripts/throughput.sh`, `PROVIDER=fake`, 50 конкурентных воркеров 20с): очередь
дренируется полностью без потерь, узкое место — сама БД-очередь, не пул провайдера. Актуальные
цифры — README, раздел «Пропускная способность».
