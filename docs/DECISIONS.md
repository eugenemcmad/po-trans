# Decisions

The task statement ("Go Code Challenge 3") fixes the contract and five business rules and is silent on much else. This file records every decision made where it is silent or ambiguous. Each entry: context (what the task says, quoted in translation, or that it is silent) → decision → why → rejected alternatives → cost. A part is left out only when there is nothing to say.

Principle: what the task states explicitly is implemented exactly as stated; decisions are made only in the gaps.

## Contract

### D1. Stored rejections
- **Context.** Rule 2: a repeated request with the same key "must return the same response as the original request and must not apply the transfer again". Rejections (`422`) are responses too.
- **Decision.** Every business rejection (insufficient funds, self-transfer, unknown account) is stored as a transfer with `status = rejected` and a `reason`; the key counts as used. A repeat returns the same `422` with the same body, even if the balance has changed since. `GET /transfers/{key}` returns it with `status: rejected` and the extra field `reason`. A `transfer_id` is generated for rejected transfers too, but the `422` body does not carry it (the contract has no such field). Validation errors (`400`) are not stored.
- **Why.** Otherwise a network retry after a refused transfer could *succeed* later (after a top-up) — a response the client never saw. A client can also tell "never arrived" (`404`) from "rejected" (`200 rejected`).
- **Rejected.** Not storing rejections — breaks rule 2 for them.
- **Cost.** Rows for requests that moved no money.

### D2. One error format: `{"status", "reason"}`
- **Context.** The task shows one error body, for `422`: `{"status": "rejected", "reason": "insufficient funds"}`; for `400`, `404` and `409` it gives only the code.
- **Decision.** All errors use that shape. `status` is `rejected` only for `422` (a stored outcome) and `error` for everything else (`400`, `404`, `405`, `409`, `500`, `503`). `reason` strings are lowercase, short and stable; with several validation errors the first one in a fixed order is returned. Routing answers of `ServeMux` (plain text by default) are intercepted: an unknown path is `404 not found`, a wrong method `405 method not allowed` with the `Allow` header that `ServeMux` computes (`GET` routes also serve `HEAD`). `500` is `internal error` without details. Every response is `application/json`, except the documentation pages under `/docs` (HTML and YAML, D27).
- **Why.** One parser for clients; the format the task already defines is extended, not replaced. `status` always means one thing: the outcome of a transfer vs. an error of the request. RFC 9110 requires `Allow` on `405`, and taking it from the router keeps it true.
- **Rejected.** `rejected` for `400`/`409` — `POST` would say `rejected` while `GET` of the same key says `404` (or the original `completed`). RFC 7807 (`application/problem+json`) — two error formats next to the fixed `422`. Separate formats for `POST` and `GET`.

### D3. Two response formats for one transfer
- **Context.** The contract gives different bodies for the `POST` `200` (no `idempotency_key`, no `created_at`) and for `GET /transfers/{key}`.
- **Decision.** One domain entity, two DTOs: `POST` `200` exactly as in the contract (no `reason`, no timestamp), `GET` exactly as in the contract plus `reason` for rejected ones (`omitempty`, safe because a rejected row always has a non-empty reason — D24). Fields that the contract does not have are never sent (not even `null`). A repeated `POST` returns the `POST` format, built from the stored record; a test checks that code and body of a repeat equal the original.
- **Why.** Strict contract match; the e2e tests prove it by validating every response against the OpenAPI file with `additionalProperties: false`. Building the answer from the record is simpler than storing response bytes.
- **Rejected.** Adding `idempotency_key` / `created_at` to the `POST` answer — a deviation from the contract under a strict check. Storing response bytes — more storage, same result.

### D4. Unknown account: `422` in POST, `404` in GET
- **Context.** Rule 4: if an account does not exist, "return HTTP `422` with the reason `account not found`"; the contract of `GET /accounts/{id}` says "`404` — account not found".
- **Decision.** As the task states. One domain error; the HTTP mapping function knows the endpoint.
- **Why.** In `POST` the account is a value in the body (well-formed but unfulfillable → `422`); in `GET` it is the resource itself (`404`). Both are explicit in the task, so neither is changed.
- **Rejected.** One code for both endpoints — contradicts one of the two explicit statements.

### D5. `created_at`
- **Context.** The contract shows `"created_at": "2025-01-01T00:00:00Z"` and nothing more.
- **Decision.** RFC 3339, UTC, second precision, `Z` suffix — as in the example. The database clock (`now()`) sets it, stored as `TIMESTAMPTZ` with microseconds; the fraction is truncated (not rounded) only in the response, and the stored value is returned on replay.
- **Why.** Exact match with the example; one clock for all instances; the stored value keeps a replay identical.
- **Rejected.** Fractions of a second (`RFC3339Nano`) — differs from the example, and clients do not need that precision.

### D6. Service endpoints
- **Context.** "The service must be available at `http://localhost:3000/`"; the contract has no health endpoints.
- **Decision.** `GET /` → `200 {"status":"ok"}`, `GET /healthz` (liveness) and `GET /readyz` (checks the database; `503 database unavailable` when it is down) — all three answer `{"status":"ok"}` when healthy. `/` is registered as `GET /{$}` — exactly the root. Swagger UI at `/docs` is a service route too (D27).
- **Why.** A reviewer's first request is likely the root, and a `404` there looks like a broken service. A plain `GET /` pattern in `ServeMux` would answer every unknown path with `200` (and other methods anywhere with `405`). `/readyz` backs the compose health check; `/healthz` costs two lines and is what orchestrators expect. `503` is the standard "not ready" signal.
- **Rejected.** `404` at the root — formally fine, looks broken. Only `/readyz`. `500 internal error` from `/readyz` — mixes "not ready" with a failed request.

### D7. Keys as path segments
- **Context.** `GET /api/v1/transfers/{idempotency_key}`; rule 5 only requires the key to be a non-empty string.
- **Decision.** The key in `GET /transfers/{key}` is one RFC 3986 encoded segment (`/` → `%2F`, space → `%20`); the server matches the decoded value exactly. A key or account id that cannot be stored (over the length limit, control characters, invalid UTF-8) is `404` without a database round trip. Keys `.` and `..` work only encoded (`%2E`, `%2E%2E`): unencoded, clients and `ServeMux` normalise the path.
- **Why.** Any allowed key must be retrievable, and encoding a segment is the standard way to carry an arbitrary string. For `GET` the contract knows only `200` and `404`, and such a key cannot exist, so `404` is the truth.
- **Rejected.** Allowing only URL-safe characters in keys — a rule the task does not have. `400` for `.` / `..` or for impossible path values — `400` is not in the `GET` contract.
- **Cost.** Some proxies decode `%2F` early — noted in the README.

### D8. `X-Content-Type-Options: nosniff`
- **Context.** The task is silent on response headers.
- **Decision.** Every response carries `X-Content-Type-Options: nosniff` next to `Content-Type: application/json`.
- **Why.** A browser never second-guesses the type of a response; a standard hardening header that costs nothing. The contract is unaffected: it fixes JSON fields, and the header is the same on an original and a replay. It is not declared in `docs/openapi.yaml`, which does not affect the response validation in e2e.

## Validation

### D9. `amount` is an integer literal
- **Context.** Rule 5 lists only the three string fields; the task says nothing about `amount` except that all its numbers are integers (`1000`, `100`, `10 000`).
- **Decision.** Coins are indivisible; `amount` and `balance` are `int64`. Only a JSON integer literal `1 … max int64` is valid. `0`, negatives, `100.5`, `100.0`, `1e2`, strings, `true`, `null`, an absent field and overflow are all `400 amount must be a positive integer` (one reason for all).
- **Why.** No rounding bugs; no ambiguity in how an amount is written; a negative amount would in effect debit someone else's account. Parsing uses `json.Number` + `strconv.ParseInt`, which also catches overflow.
- **No upper limit** beyond `int64` — the task has none; an amount above the balance is a normal `422`. Limits are a production topic.
- **Rejected.** Accepting `100.0` / `1e2` as mathematically whole — complexity for a form no sane client sends. A separate `amount is required` — two reasons for one field without benefit. A business limit with `400` — a rule the task does not have.

### D10. Strings
- **Context.** Rule 5: `idempotency_key`, `from_account_id`, `to_account_id` "must be non-empty strings". Nothing about length, characters, case or format.
- **Decision.** No normalisation: no trimming, no case folding, byte-wise comparison. A string of only whitespace is "empty" (`is required`); otherwise the value is kept exactly. Lengths in code points: key 1–255, account id 1–64. Control characters (Unicode Cc) are invalid; format characters (ZWJ, bidi) are allowed. No format check (neither UUID for the key nor `acc_N` for ids). `null` means absent; a number, boolean, object or array is `<field> must be a string`. Names of fields are matched case-sensitively, unknown fields are ignored, duplicates → last wins, trailing data → `invalid json`.
- **Why.** An idempotency key is an opaque token: in base62 the case carries entropy, so folding it would make unrelated requests collide (a wrong replayed answer or a false `409`), and `GET` would return a different key than was sent; Unicode case folding is itself locale-dependent. Account ids: an exact match means `ACC_1` is `account not found`, no money moves. Length limits protect the index; control characters break logs and paths. Ignoring unknown fields is the usual compatibility rule, and a typo in a required field still yields `400`.
- **Rejected.** Trimming or lowercasing — hidden synonyms of keys and ids. Checking a UUID or `acc_N` pattern — an invented rule. `DisallowUnknownFields` — stricter without benefit. `400` on duplicated names — needs a custom streaming parser for an exotic case.
- **Cost.** A client that changes the case of a key on retry creates a new transfer — a violation of the idempotency contract on the client's side, documented in the README. Duplicated field names are accepted.

### D11. Order of checks, and a used key with invalid data → `409`
- **Context.** Rule 2: a request with an existing key but other `from_account_id`, `to_account_id` or `amount` "must be rejected with HTTP `409 Conflict`"; rule 5: validation errors return `400`. The task does not say which wins.
- **Decision.** (1) content type, size, valid UTF-8, JSON object, and the key itself; (2) lookup by key: any difference in `from`, `to` or `amount` → `409`, even if the new value is invalid or of another JSON type, and whatever the stored status (`completed` or `rejected`); (3) otherwise validate the fields in contract order (first error wins) → `400`; (4) self-transfer; (5) accounts exist; (6) funds. The same key with other data that would now be `422` is also `409`. This is the priority of answers, not the order of database calls.
- **Why.** Rule 2 says a used key with different data is `409` without exceptions; rule 5 applies where rule 2 cannot (no key yet, or nothing to compare). To make this work, the body is parsed into `map[string]json.RawMessage` first, so a wrong *type* (`"amount": "100"`) does not become `400` before the key is looked at (and `encoding/json` would also match field names case-insensitively). Business checks go from cheap to expensive.
- **Implementation.** A valid body goes straight to the transaction (key insert first); only an invalid one costs a `SELECT` by key.
- **Self-transfer before account existence.** `acc_99 → acc_99` is `self-transfer not allowed`: it is unfulfillable whatever the database holds, it needs no row reads, and rule 3 comes before rule 4.
- **Rejected.** `400` for any invalid body before the key lookup — wrong for errors in single fields, against the letter of rule 2. Business checks before the key — `422` instead of the unconditional `409`, plus useless locks. Account existence before self-transfer — a database read for a request that can never succeed.
- **Cost.** If the first request with the key is still running, a concurrent invalid duplicate sees no key yet and gets `400` instead of `409`.

### D12. Content type, body size and read errors
- **Context.** The contract states `Content-Type: application/json`; rule 5 puts validation errors under `400`.
- **Decision.** `application/json` (parameters such as `; charset=utf-8` allowed) is required, otherwise `400 unsupported content type`; body ≤ 64 KiB (`http.MaxBytesReader`) → otherwise `400 request body too large`. UTF-8 is verified on the whole body before parsing; invalid → `invalid json`, as is an empty body. Any other failure to read the body (the client dropped the connection, `HTTP_READ_TIMEOUT` expired) is `400 invalid json`.
- **Why.** The header is part of the contract. A normal request is about 200 bytes. `encoding/json` silently replaces invalid bytes with U+FFFD, which would change client data and merge different keys; by RFC 8259 such a body is not JSON. A body that did not arrive whole has nothing to parse — in effect broken JSON — and a client that dropped the connection never sees the answer anyway.
- **Rejected.** `415` — not in the contract. Not checking the header — contradicts the contract. Checking UTF-8 per field after parsing — the bytes are already replaced by then. `408` for read errors — not in the contract; `500` — not a failure of the service, and it would clutter the `error` log level.
- **Cost.** `curl -d` without `-H 'Content-Type: application/json'` gets `400` (README examples set it). An escaped lone surrogate (`"\ud800"`) is still replaced; catching it needs a custom JSON parser.

## Concurrency and consistency

### D13. One transfer — one `READ COMMITTED` transaction with row locks
- **Context.** Rule 1: "an account balance can never become negative"; the task is silent on concurrency, but parallel requests are the norm for such a service.
- **Decision.** (1) `INSERT … ON CONFLICT (idempotency_key) DO NOTHING` with status `completed`; (2) self-transfer → mark `rejected`; (3) lock both accounts with `SELECT … FOR UPDATE`, one query each, in byte-wise order of ids chosen **in Go**; (4) missing row → `account not found`, `balance < amount` → `insufficient funds` (a transfer of the whole balance is fine — zero is not negative); (5) two `UPDATE`s, commit. Rejections `UPDATE` the already-inserted row inside the same transaction.
- **Why.** Distinguishing "no account" from "no funds" and locking in one order cannot be done by a single conditional `UPDATE`. The order is set in Go because the order in `WHERE id IN (…)` or `ORDER BY id` depends on the query plan and collation; one fixed order in every transaction makes a wait cycle impossible.
- **Why the row is inserted as `completed`.** The `CHECK` constraints must hold at insert time and the outcome is not known yet. The row becomes visible to others only at commit, with its final status; a rollback removes it. A `pending` status would add a value that must never be visible.
- **Concurrent requests with one key.** The second insert waits on the unique index until the first transaction ends, then reads the stored result: same data → the same answer, other data → `409`; if the first rolled back, the second proceeds as new. The wait is bounded by the request timeout (`500`; safe to retry).
- **Rejected.** `UPDATE … WHERE balance >= $1` (see above); in-process mutexes (break with several instances); optimistic row versions (many retries on hot accounts); `SERIALIZABLE` (same correctness, more retries); an advisory lock on the key hash (a second locking mechanism next to the unique index); one `IN … ORDER BY … FOR UPDATE` query; an immediate `409` "in progress" for a concurrent duplicate (mixes two meanings of `409` and breaks "the same response").
- **Cost.** One extra round trip for the second lock; transfers on a hot account are strictly serial.

### D14. Safety net and retries
- **Context.** The task is silent; rule 1 is the invariant that must survive a bug in the code.
- **Decision.** `CHECK (balance >= 0)` in the database. A transaction ending in `40P01`/`40001` is retried as a whole (up to 3 retries, backoff with jitter 5–25, 10–50, 20–100 ms; the request context is checked before each); after that `500` and the log carries the number of attempts and the last code. Any other database error (`23505` included) is a plain `500`. `MaxRetries` is a constructor parameter of the repository, so integration test C2 can run with `0`.
- **Why.** Even a bug cannot write a negative balance — the transaction rolls back. With a fixed lock order a deadlock is not expected, so retries are insurance; random, growing delays keep retried transactions from colliding again; with retries on, C2 could not notice a broken lock order. After the last attempt the transaction has rolled back completely, so the client can safely retry with the same key.
- **Rejected.** Retrying without a pause or with a fixed pause — the transactions stay in step. Detecting deadlocks in C2 through `pg_stat_database.deadlocks` — statistics are updated asynchronously, the test would be slow and flaky.

### D15. Global, permanent idempotency keys
- **Context.** `GET /api/v1/transfers/{idempotency_key}` finds a transfer by the key alone; the task has no clients or authentication.
- **Decision.** One key space for the whole system, never deleted.
- **Why.** Uniqueness per sender would make `GET` ambiguous, and there is no client to bind a key to. Deleting a key would turn `GET` into `404` and let a replay move money again; `transfers` also serves as the audit trail.
- **Rejected.** Uniqueness of `(sender, key)` — breaks `GET`. A TTL (e.g. 24 h) — breaks `GET` without a gain for this task.
- **Cost / production.** Unbounded growth: a separate key store with a TTL, partitioning of `transfers`, keys bound to the client (README).

### D16. Stateless service, several instances
- **Context.** The task is silent on the number of instances.
- **Decision.** No caches, locks or idempotency state in the process; correctness is the database's job (transactions, row locks, the unique key index). More instances are replicas of the same image on the same database (see `docs/OPERATIONS.md`); the demo environment runs one.
- **Why.** If correctness rests on the database, the number of instances cannot change it, and scaling needs no code change. The concurrency tests (C1–C5) already exercise many concurrent database sessions.
- **Rejected.** One instance with in-process locks — breaks at the first scale-out or a restart under load.

### D17. Timeouts and configuration
- **Context.** The task is silent.
- **Decision.** Configuration comes only from environment variables, with defaults (`DATABASE_URL` is required). Request limit 10 s from entering the handler — it bounds the database work, including waiting on a key; reading the body is bounded by `HTTP_READ_TIMEOUT`. Server timeouts `ReadHeader` 5 s, `Read` 5 s, `Write` 15 s, `Idle` 60 s. The service refuses to start if `HTTP_WRITE_TIMEOUT ≤ REQUEST_TIMEOUT`, naming both variables.
- **Why.** Environment variables are the 12-factor way and fit compose. In `net/http` the write deadline runs from the end of the headers — the same point the handler starts from — so with `Write` > request limit an error response always has at least 5 s to be written; the check keeps a bad setting from silently breaking that.
- **Rejected.** Configuration files or flags — a second mechanism. The earlier `Read` 10 s + 10 s handling against `Write` 15 s — did not guarantee time for the response.

### D18. Graceful shutdown and startup
- **Context.** The task is silent.
- **Decision.** The request context is rooted in `context.Background()` (`BaseContext`), **not** in the application context. On `SIGINT`/`SIGTERM` (a cancelled context): `Shutdown` with its own 15 s context → `Close` if that expires → the pool closes last (`Serve(ctx, logger, srv, ln, timeout, closers…)`). `Run` builds the dependencies, `Serve` runs and stops the server; `main` only turns signals into a context.
- **Why.** If requests inherited the application context, shutdown would abort the very transaction it is waiting for and the client would get `500` instead of `200`. The split lets a unit test drive `Serve` with a fake store and an integration test drive the real `Run`.
- **Startup.** `Run` pings the database (10 s limit) before it starts listening; if the database is unreachable or `DATABASE_URL` is wrong, the process exits with an error and `restart: unless-stopped` starts it again. A stop requested while connecting is a clean exit.
  - **Why.** A configuration error shows at once, not on the first request (fail fast, let the orchestrator restart); in compose the database is already up, so the check costs nothing; the shutdown test sees a real connection.
  - **Rejected.** Starting without the database and reporting it only through `/readyz` (`503`) — correct, but a wrong `DATABASE_URL` goes unnoticed until someone looks.
- **Rejected.** Request contexts derived from the application context — see Why.

### D19. Database session settings
- **Context.** The task is silent.
- **Decision.** Unless `DATABASE_URL` sets them, the pool opens sessions with `idle_in_transaction_session_timeout = 30s` and `application_name = po-trans`.
- **Why.** If the process hangs or loses its connection in the middle of a transfer, PostgreSQL ends the transaction itself and releases the `FOR UPDATE` locks — otherwise the accounts would stay locked until TCP gives up. A normal transfer is never affected: 30 s is longer than the 10 s request limit (the request context cancels and rolls back first), and waiting on the idempotency key index is an active query, not idle time. `application_name` makes the service's connections visible in `pg_stat_activity`; a value in the URL wins, which is how the shutdown test marks its own connections.
- **Rejected.** No idle timeout — a stuck transaction holds the locks of hot accounts indefinitely. A separate environment variable — the connection string already carries session parameters.

### D20. Resilience mechanisms
- **Context.** The task is silent.
- **Decision.** Required and implemented: idempotency (D1, D11, D13), retries with backoff and jitter (D14), timeouts (D17). Not required: a rate limiter — the contract has no limits (a production topic, README); a circuit breaker — the only dependency is the service's own database, timeouts already fail fast and `/readyz` reports `503`.
- **Why.** Each mechanism was considered explicitly, so an absent one is a decision, not an omission.

## Architecture and operations

### D21. Layers, light CQRS, fake store
- **Context.** The task is silent on structure.
- **Decision.** `domain` (entities, rules, validation, errors) → `app` (command `CreateTransfer`, queries `GetTransfer`, `GetBalance`, ports) → `repository/postgres` (PostgreSQL write and read repositories), `config`, `httpapi` (transport); `server` wires them. The in-memory fake used by the unit tests is a test utility and lives in `tests/memstore`, not in `internal/`. Packages are named for what they provide, not for their layer: the dependency rule is visible in the import graph. `domain.Decide` owns the order of the transfer rules (and the order in which accounts are loaded, hence locked); both storage adapters call it instead of repeating it. The request-shaped input of the command (`TransferRequest`, the JSON kind of each field) belongs to `app`, not to the domain. One database and one model: reads are never behind writes. The command may read: the key lookup for invalid fields goes through the reader port. The application assigns the transfer id (UUIDv7).
- **Errors.** Domain sentinels and one `*ValidationError{Field, Err}`; one function in the transport maps errors to HTTP (it knows the endpoint). Texts of `reason` are constants of the transport / `RejectReason` values of the domain, never `err.Error()`, so editing an error message cannot change the API. Technical details of a `500` stay in the log.
- **Fake instead of generated mocks.** The in-memory store is a small real implementation of every port; tests assert behaviour, not call sequences. It does not model locks, so concurrency is tested only on PostgreSQL.
- **Why.** Business rules are tested without a database; database work is isolated in one layer; the command/query split costs nothing and keeps the transactional path apart from reads.
- **Rejected.** One flat package — handler tests would need a database. Full hexagonal / DDD layout or `infrastructure/`, `transport/` directories — too much for three endpoints. A separate read model or projections — a `GET` right after a `POST` could see stale data. A fake inside one package's `_test.go` — not reusable by `app` and `httpapi` tests. gomock / mockery — tests bound to the implementation.

### D22. Technology stack
- **Context.** "Language: Go"; "Database: of your choice (PostgreSQL, MongoDB, etc.)"; the write-up must explain "why this database, framework and libraries".
- **Decision.** Go 1.26 (`go 1.26`, image `golang:1.26.9-alpine3.24`); HTTP on the standard `net/http` with `ServeMux` (methods and path parameters since Go 1.22), no framework; PostgreSQL 18 with `jackc/pgx/v5` and `pgxpool`; logs with `log/slog`; tests with `testing`, `testify/require`, `go.uber.org/goleak`, `getkin/kin-openapi` (response validation in e2e only).
- **Why.** Go 1.26 is a supported release with a mature patch level and matches common local toolchains. Three endpoints need no framework, the strict validation rules are hand-written anyway, and `ServeMux` matches `%2F` keys and answers `405` out of the box. PostgreSQL gives ACID transactions, `FOR UPDATE`, waiting on a unique index and `CHECK` constraints — exactly what money needs. A pool is mandatory because one `pgx.Conn` cannot be used concurrently; `pgxpool` gives `pgx.Tx` and PostgreSQL error codes directly. `require` stops at the first failure without follow-up noise.
- **Rejected.** Go 1.27 — newest, no gain, needs a toolchain update. Gin (breaks keys with `/` by default, `404` instead of `405`, a dependency tree), chi, echo. MongoDB (clumsier locking, no `CHECK`), Redis (no durability by default). `database/sql` + `pgx/stdlib`, `lib/pq` (maintenance mode), gorm (hides SQL and locks, which matter here), sqlc (an extra generation step). zap / zerolog (faster, but a dependency). `testify/assert` (continues after a failure), plain `testing` only (verbose).
- **Cost.** `kin-openapi` adds transitive modules to `go.mod` (not to the service binary).

### D23. Logging and request ids
- **Context.** The task is silent.
- **Decision.** `log/slog`, JSON, one record per request: `request_id`, `method`, `path`, `status`, `duration_ms`; for `POST /transfers` also `idempotency_key`, `from_account_id`, `to_account_id`, `amount` (when parsed). `reason` is logged for every `4xx` answer of the API endpoints (`POST /transfers`, `GET /transfers/{key}`, `GET /accounts/{id}`) — `400`, `404`, `409`, `422` — with the same text as the response body; routing answers (`404`/`405` for an unknown path or method) have none; `5xx` carry `error` (the full error chain) instead. Levels: `info` for `2xx` and `4xx` (a `422` is an expected outcome, not a failure), `error` for every `5xx`; `/healthz` and `/readyz` at `debug` unless they fail. Never logged: the raw body, stack traces (except for a recovered panic). The request id comes back in `X-Request-ID`; a client's own `X-Request-ID` is used when it is at most 128 characters without control characters.
- **Why.** One record tells what happened to a transfer and why; business rejections do not flood the `error` level people watch; `500` details stay in the log and never reach the client; probes polled every few seconds would bury real requests at `info`. A client-supplied id allows tracing across services, and the limits keep junk out of the log.
- **Rejected.** zap / zerolog — a dependency (D22). `reason` only for `422` — for `400` and `409` the cause would have to be reconstructed from the request fields.

### D24. Schema and the demo environment
- **Context.** "Provide a `docker-compose.yml` that starts the service, the database and performs the initial data seeding"; "on start the following accounts must be created": `acc_1` … `acc_10` with 1000 each, 10 000 in total.
- **Decision.** `accounts(id, balance CHECK >= 0)`; `transfers` with `UNIQUE (idempotency_key)`, `CHECK`s for amount, status and reason consistency (a rejected transfer always has a non-empty reason, so `omitempty` in the `GET` DTO is safe), `UUID` primary key generated by the application as **UUIDv7**. No foreign keys — rejected transfers to unknown accounts are stored. No `created_at` on accounts, no ledger table (`transfers` already records every movement).
- **Setup.** A one-shot `demo-setup` service (the `postgres` image as is, SQL mounted read-only) runs `psql -v ON_ERROR_STOP=1 --single-transaction` over `reset.sql → 00001_init.sql → seed.sql`; `api` starts only after it succeeded. The API has no migration or reset code, so a crash or restart of the API can never wipe data. Demo and test SQL lives in `deploy/local/`, production gets only `migrations/`. No migration tool: the demo schema is recreated on every start. Every start of the environment is a clean state: 10 × 1000, no transfers.
- **`tmpfs`** at `/var/lib/postgresql` (the layout of the PostgreSQL 18 image) is for speed and no leftover disk data; clean state comes from `demo-setup`.
- **Compose details.** `db` is healthy only when `pg_isready` succeeds over TCP (`-h 127.0.0.1`; during init the image runs a socket-only server). `api` is checked with `wget --spider http://127.0.0.1:3000/readyz`. `api` gets `stop_grace_period: 20s` (more than the 15 s shutdown limit; the default 10 s would cut it). `db` and `demo-setup` have `restart: "no"`, `api` has `restart: unless-stopped`: a `db` that restarted with an empty `tmpfs` must fail visibly rather than run without tables.
- **Observed.** A second `docker compose up -d` on a live environment reruns `demo-setup` (data reset) and recreates the `api` container.
- **Network and password.** The API port is published on the loopback interface only (`127.0.0.1:3000`); the database port is not published at all. The database password is `DB_PASSWORD` (compose only, default `app`); it is inserted into `DATABASE_URL` as is, so `@ : / % ? #` are not allowed in it.
  - **Why.** A demo with a default password is not reachable from the local network, and `http://localhost:3000/` still works; the password can be changed without editing files, and `docker compose up` needs no setup.
  - **Cost.** From another machine the service is unreachable without changing the port binding; a password with special characters would need URL encoding, which compose does not do.
- **Why.** UUIDv7 does not reveal the number of transfers, cannot be enumerated, is ordered for the index and does not depend on the PostgreSQL version. The database checks integrity even if the code is wrong. Demo data is demo data: every run starts from a known state, as the task's "on start" says, and balances and keys are reset together. One rule — "only `migrations/` goes to production" — is visible in the directory layout.
- **Rejected.** `BIGSERIAL` ids (reveal volume, enumerable), UUIDv4 (worse for the index). A PostgreSQL `ENUM` for status (harder to change). Seeding or resetting in the API (a restart or a second instance would wipe data). Scripts in `/docker-entrypoint-initdb.d` (run only on first init, no explicit reset). `golang-migrate` / `goose` (versioning is pointless when the schema is recreated on every start). Keeping data between runs (runs stop being reproducible). Relying on `tmpfs` alone for a clean state (implicit, depends on Docker).
- **Cost.** No schema versioning — the README says to switch to a migration tool once the production schema starts to change.

### D25. Tests
- **Context.** "Write tests. Full coverage is not required, but show your approach to testing with meaningful examples. Focus on the cases you consider most important for a service of this kind."
- **Decision.** Three levels — unit tests on the in-memory store (`tests/memstore`), integration tests on PostgreSQL (`tests/integration`, one package, sequential), e2e tests on the built image (`tests/e2e`, `e2e` build tag). The risks and the tests that cover each, and the rules every test follows, are in [TESTING.md](TESTING.md). Decided here: scenarios without mocks are a fixed, approved list (I1–I11, C1–C5, S1; E1–E10); `-race` runs only in Docker; graceful shutdown is tested twice (in-memory store; the real `server.Run`, with `pg_stat_activity` proving the pool is closed); the `CHECK` constraints are tested directly and must fail with a check violation (`23514`), not with just any error — the other tests go through correct code, which never trips a constraint, so a constraint dropped from the migration would otherwise go unnoticed.
- **How they run.** `make test-ci` and `make e2e` use their own compose projects (`po-trans-test`, `po-trans-e2e`), pass `--build` to every command that runs tests, and remove everything afterwards, also after a failure. e2e brings up a fresh environment each time, does not publish the API port (an overlay with `ports: !reset []`), and waits for `/readyz` and checks the initial balances in `TestMain` instead of `up --wait`. The e2e command runs with `-count=1`: it tests an external service whose state the `go test` result cache cannot see, so every run must hit the live API. Both test services fail if no test is found.
- **Why.** The main risks of this service are in PostgreSQL behaviour (lock order, the `ON CONFLICT` wait, rollback, `CHECK`, races) — a stub behaves as written, not as the database does. The fast part stays without Docker. One package for all database tests runs them one after another without `-p 1` or build tags. Separate projects keep test runs away from a developer's environment and give each run a fresh database. `up --wait` behaves differently across Compose versions.
- **Rejected.** Everything on stubs (misses the main risks); everything on a real database (slow, Docker even for validation); testcontainers inside compose (Docker-in-Docker or a mounted socket); integration tests next to the code with a build tag and `-p 1`; absolute balances in e2e (depend on test order); `-race` on the host (needs gcc, not on Windows by default); an extra `-race` API image for e2e (catches what the integration tests already catch).
- **Not done:** coverage measurement and thresholds, and the rest listed in [TESTING.md](TESTING.md#deliberately-not-done).

### D26. Build, tooling and CI
- **Context.** The task asks only for a `docker-compose.yml`.
- **Decision.** One multi-stage `Dockerfile`, all stages on Alpine 3.24: `deps` → `build` (static binary, `CGO_ENABLED=0`) / `test` (gcc for `-race`, `golangci-lint` v2.14.0) → `runtime` (`alpine:3.24`, only the binary, non-root user). Target platform `linux/amd64`. `.dockerignore` keeps tests, docs and SQL in the build context (otherwise `go test` finds no files and exits 0); `.gitattributes` forces `eol=lf`; `.gitignore` lists exact patterns instead of broad `*tmp*`. Makefile targets `test`, `test-ci`, `e2e`, `lint`, `docker-up`, `docker-down`; recipes avoid shell-specific syntax so they work in `sh` and `cmd.exe`, and every target has an equivalent `docker compose` command in `docs/OPERATIONS.md`. GitHub Actions on push and pull request: `make lint`, `make test-ci`, `make e2e` on Ubuntu, plus `go test ./internal/...` on Ubuntu, Windows and macOS.
- **Why.** One base line for all stages keeps the shared `deps` stage truly shared; Alpine has `wget` and a shell for the health check; a pinned linter gives the same result locally and in CI; only Docker is required on any OS; the OS matrix proves the portability of the unit tests in practice; CRLF in `.sql` would break `psql` in a Linux container.
- **Rejected.** distroless with a built-in health command (more code for a smaller image); a Debian image for the `test` stage (two base lines); a separate `Dockerfile.test` (duplicates the build); shell scripts or `sh`-only recipes (break on Windows); excluding `**/*_test.go` from the build context (tests vanish from the test image); no CI ("the same locally and in CI" would be unproven); arm64 support (work without benefit for the task).
- **Cost.** A slightly larger attack surface than distroless; gcc is installed by hand; any change to a test file invalidates the build cache of the runtime image; Windows and macOS CI minutes cost more.

### D27. Documentation
- **Context.** The task asks for "a short description" answering four questions; the additional requirements ask for attention to detail and a careful check before submission.
- **Decision.** `README.md` (what it is, quick start, `curl` examples, short answers to the four questions, links), `docs/DECISIONS.md` (this file), `docs/PRODUCTION.md` (what changes from the MVP to production, most important first, with observability and events for analytics), `docs/TESTING.md` (the testing approach: the risks in order of importance and the tests that cover each), `docs/MANUAL_CHECK.md` (a five-minute step-by-step check of the running service: Swagger UI, requests and expected answers, where to look at the result), `docs/OPERATIONS.md` (requirements, build, run and reset, second instance, production deployment, configuration, tests, troubleshooting), `docs/openapi.yaml` (OpenAPI 3.0.3, written by hand, every response schema with `additionalProperties: false`). All in English. The OpenAPI file is validated automatically: every e2e response is checked against it.
- **Why.** The reviewer finds the four answers without following links; the details live in their own files so the README stays short; a machine-readable contract that the tests check cannot silently drift from the code.
- **Rejected.** Generating OpenAPI from annotations (`swag`) — a dependency and noise in the handlers. OpenAPI 3.1 — nothing in it is needed, and 3.0 has the longest support in `kin-openapi`.
- **Cost.** Only the responses the e2e tests trigger are validated (E1–E10 cover every status code).

#### Swagger UI is served by the API, not by a separate container
- **Context.** The reviewer must be able to see the API and try it with as little setup as possible; the task names no tool.
- **Decision.** The API itself serves Swagger UI at `GET /docs` and the OpenAPI file at `GET /docs/openapi.yaml`. The file is embedded in the binary (`go:embed`, package `docs`); the page loads `swagger-ui-dist` from the jsDelivr CDN at a pinned version. The routes are service routes, outside the contract and outside `openapi.yaml`; their errors (`404`, `405`) use the common format. The reviewer opens <http://localhost:3000/docs> and runs requests with "Try it out".
- **Why.** The page and the API share one origin (`localhost:3000`), so the browser lets "Try it out" call the API — no CORS, nothing to configure, no extra service. The documentation is wherever the service is and always matches the running version, because the file is built into the same binary. `servers` in `openapi.yaml` (`http://localhost:3000`) is the address of the page.
- **Rejected.**
  - A `swagger-ui` container in compose on its own port — a different origin: the browser blocks "Try it out" against `localhost:3000`. Making it work needs CORS headers and `OPTIONS` handling (a `POST` with JSON triggers a preflight) in the API — new code and changed responses — or a proxy in front of the API, plus one more service to run.
  - An external editor (editor.swagger.io) — a third-party site, and its "Try it out" against `localhost` hits the same CORS wall.
  - Vendoring `swagger-ui-dist` into the repository — about 1.5 MB of foreign files only to work offline.
- **Only because this is a test assignment.** The choice is made for a quick check: start the service and check that it works right away, with no extra setup. It is not acceptable for production: a money-transfer service should not serve an interactive console to itself (an extra attack surface — a page that executes requests on the API's own origin), third-party CDN scripts on that origin, and a full description of its internal API. In production `/docs` must not be served, so it is behind `DOCS_ENABLED`, off by default: `server.Run` passes the OpenAPI file to the handler only when it is `true`, and without the file the routes are not registered (`404`); the demo `docker-compose.yml` sets it to `true`. A safe default means an image started anywhere without configuration serves no console, while the quick check of the demo still needs no action. (Rejected: on by default — production would depend on someone remembering to switch it off; the name `DOCS_OPENED` — reads as "open without authentication"; a separate build without `/docs` — two binaries for one route.) Instead, `openapi.yaml` is published separately — a developer portal or the API gateway, with its own authentication and with CORS configured there, not in the service — and Swagger UI, if wanted, lives there too.
- **Cost.** The reviewer's browser needs internet access for the CDN assets. "Try it out" works when the page is opened as `localhost:3000`; `127.0.0.1:3000` is a different origin and the browser blocks the calls.

### D28. No authentication
- **Context.** The contract has no headers or fields for authentication.
- **Decision.** No authentication or authorisation in the service; [PRODUCTION.md](PRODUCTION.md) (briefly, the README) describes what production would add: client authentication (JWT or mTLS), only the owner of `from_account_id` may debit it, idempotency keys bound to the client, rate and amount limits.
- **Why.** Adding authentication would change the contract and break the reviewer's checks; the task explicitly asks to describe what production would change.
- **Rejected.** An optional API key switched off by default — code without tests or use.
