# Coin transfer service

An HTTP API in Go that moves in-game coins between player accounts. It guarantees that a balance never goes negative, that the total amount of coins never changes, and that a repeated request (same idempotency key) is applied once.

Stack: Go 1.26, `net/http`, PostgreSQL 18 (`pgx`), Docker Compose.

## Quick start

Requires only Docker with Docker Compose ≥ 2.24.

```bash
docker compose up -d --build
```

This starts PostgreSQL, resets and fills the database (`acc_1` … `acc_10`, 1000 coins each — 10 000 in total) and starts the API on <http://localhost:3000/>. Data lives in `tmpfs` and every fresh start begins from this state. Running `docker compose up` again on a live environment resets the data (see [docs/OPERATIONS.md](docs/OPERATIONS.md)).

**See it work:** open Swagger UI at <http://localhost:3000/docs> (served by the API) and follow the five-step [manual check](docs/MANUAL_CHECK.md) — requests to send, expected answers, and where to look at balances, stored transfers and logs.

The examples use a POSIX shell (on Windows: Git Bash). The `Content-Type` header is required.

```bash
# transfer 100 coins
curl -s -X POST localhost:3000/api/v1/transfers -H 'Content-Type: application/json' -d '{
  "idempotency_key": "550e8400-e29b-41d4-a716-446655440000",
  "from_account_id": "acc_1", "to_account_id": "acc_2", "amount": 100 }'
# {"transfer_id":"…","status":"completed","from_account_id":"acc_1","to_account_id":"acc_2","amount":100}

# the same request again: the same answer, the money is not moved twice
# the same key with another amount: 409
curl -s -i -X POST localhost:3000/api/v1/transfers -H 'Content-Type: application/json' -d '{
  "idempotency_key": "550e8400-e29b-41d4-a716-446655440000",
  "from_account_id": "acc_1", "to_account_id": "acc_2", "amount": 101 }'
# HTTP/1.1 409 …  {"status":"error","reason":"idempotency key reused with different parameters"}

# more than the balance: 422, stored under the key
curl -s -i -X POST localhost:3000/api/v1/transfers -H 'Content-Type: application/json' -d '{
  "idempotency_key": "too-much", "from_account_id": "acc_1", "to_account_id": "acc_2", "amount": 5000 }'
# HTTP/1.1 422 …  {"status":"rejected","reason":"insufficient funds"}

curl -s localhost:3000/api/v1/accounts/acc_1                  # {"account_id":"acc_1","balance":900}
```

More requests and where to look at the result — in the [manual check](docs/MANUAL_CHECK.md).

Everything about the contract — every status code, every `reason` text, the error format — is in [docs/openapi.yaml](docs/openapi.yaml) (OpenAPI 3.0.3), shown by Swagger UI at <http://localhost:3000/docs>. The e2e tests validate every response against that file.

A few points of the contract that are easy to miss:

- A rejected transfer (`422`: insufficient funds, self-transfer, unknown account) is **stored** under its key. Repeating the request returns the same `422`, even if the balance has changed. `GET /api/v1/transfers/{key}` shows it with `"status":"rejected"` and a `reason`.
- An unknown account is `422` in `POST /transfers` (the account is a value in the body) but `404` in `GET /accounts/{id}` (the account is the resource).
- Under a used key, *any* other data is `409` — also data that would be invalid (`amount: -1`) — the key is checked before the fields.
- Keys and account ids are compared byte for byte: `ACC_1` ≠ `acc_1`, and a client that retries must send the key unchanged.
- In `GET /api/v1/transfers/{key}` the key is one path segment, encoded per RFC 3986 (`/` → `%2F`). Keys `.` and `..` can only be fetched as `%2E` / `%2E%2E`, because clients and the router normalise plain dot segments.

## Tests

```bash
make test       # unit tests on the host, no Docker (go test ./internal/...)
make test-ci    # unit + integration tests with -race, on a real PostgreSQL, in containers
make e2e        # black-box tests against a freshly built environment
make lint       # golangci-lint in a container
```

`make` is optional; [docs/OPERATIONS.md](docs/OPERATIONS.md) lists the equivalent `docker compose` commands.

For a service that moves money, the expensive bugs are money created, lost or moved twice — not a wrong JSON field. So those risks are tested on a real PostgreSQL, concurrently and with the race detector: an overdraft under parallel debits (exactly 10 of 20 debits of 100 from 1000 succeed), the 10 000 total under 200 random parallel transfers, a retried request applied once, one key with conflicting bodies at the same time, opposite transfers without deadlock, a transfer finishing during shutdown, and the database's own `CHECK` constraints. The contract and validation are covered by fast table-driven unit tests; e2e tests check the built image over HTTP and validate every response against `docs/openapi.yaml`.

[docs/TESTING.md](docs/TESTING.md) lists the risks in order of importance and the tests that cover each one.

## Documentation

- [docs/MANUAL_CHECK.md](docs/MANUAL_CHECK.md) — a five-minute manual check of the running service: Swagger UI, requests and expected answers, where to look at the result.
- [docs/PRODUCTION.md](docs/PRODUCTION.md) — what changes from this MVP to production, most important first, including observability and events for analytics.
- [docs/TESTING.md](docs/TESTING.md) — the testing approach: the risks of a money-transfer service in order of importance and the tests that cover each one.
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — building, running and resetting, a second instance (on another server or next to the first one), production deployment, configuration, tests, troubleshooting.
- [docs/DECISIONS.md](docs/DECISIONS.md) — every non-obvious decision: context, choice, why, rejected alternatives, cost.
- [docs/openapi.yaml](docs/openapi.yaml) — the API; Swagger UI shows it at <http://localhost:3000/docs>.

## Notes for the review

### What I would add or change for production

In order of importance (details, including observability and events, in [docs/PRODUCTION.md](docs/PRODUCTION.md)):

1. **Security.** Client authentication (JWT or mTLS); only the owner of `from_account_id` may debit it; idempotency keys bound to the client; TLS and secrets from a vault. Swagger UI at `/docs` is only for a quick check of the demo: `DOCS_ENABLED` is `false` by default, and in production the OpenAPI file lives on a developer portal or the API gateway.
2. **Limits.** Rate limiting per client, per-transfer and per-day limits, an upper bound on the amount.
3. **Data safety.** Managed PostgreSQL with replication and backups instead of `tmpfs`; versioned migrations (`golang-migrate` / `goose`) as a separate deploy step, only `migrations/` — never `deploy/local/`.
4. **Observability.** Metrics (request rate, errors, latency, transfer outcomes, deadlock retries, pool saturation), OpenTelemetry tracing, central logs, alerts, and a periodic check that the sum of balances never changes. Today: one JSON log line per request with a request id.
5. **Events.** `transfer.completed` / `transfer.rejected` through a transactional outbox to Kafka, for analytics and other services.
6. **Idempotency keys and history.** Keys in a separate store with a TTL; `transfers` partitioned by `created_at` with retention. Today keys live forever, which keeps `GET /transfers/{key}` and replay correct.
7. **Scaling, ledger, delivery.** See below; a double-entry ledger once there are operations other than transfers; a distroless image with a built-in health command. Proxies must not decode `%2F` in paths, which breaks keys containing `/`.

### Why this database, framework and libraries

- **PostgreSQL.** Money needs ACID transactions, row locks (`SELECT … FOR UPDATE`), a unique index that makes concurrent requests with one key wait for each other, and `CHECK` constraints as a last line of defence (`balance >= 0`). MongoDB has multi-document transactions but no equivalent of the constraint and clumsier locking; Redis gives no durability guarantees by default.
- **`net/http` and `ServeMux`.** Three endpoints; method and path-parameter routing is built in (Go 1.22+), keys with `%2F` work without configuration, and the strict validation rules have to be hand-written anyway. A framework would add dependencies and, in Gin's case, break encoded slashes by default.
- **`pgx/v5` + `pgxpool`.** Requests are concurrent, so a pool is mandatory; pgx gives native transactions and PostgreSQL error codes (deadlock `40P01`). No ORM: the SQL and the locking *are* the logic here.
- **Tests:** `testing`, `testify/require`, `goleak` (goroutine leaks), `kin-openapi` (responses are validated against the OpenAPI file). No mock generators (see Trade-offs).
- **Structure:** `domain` → `app` (one command, two queries, ports) → `repository/postgres` (PostgreSQL), `config`, `httpapi` (transport); `server` wires them. The in-memory fake store used by the unit tests lives with the tests, in `tests/memstore`. Business rules never see HTTP or SQL.

### Infrastructure, deployment and scaling

The service is stateless: all correctness rests on the database (transactions, row locks, the unique key index). Any number of replicas can run behind a load balancer without coordination — [docs/OPERATIONS.md](docs/OPERATIONS.md#production-deployment) describes the deployment. Configuration is environment variables only (12-factor); shutdown is graceful (in-flight transfers finish, the pool closes last); `/healthz` and `/readyz` are for liveness and readiness.

Scaling the database is the real limit: transfers from one hot account are serialised by its row lock — correct, but with a ceiling. The path forward (PgBouncer, a queue or batching per hot account, sharding by account with a saga) is in [docs/PRODUCTION.md](docs/PRODUCTION.md).

### Trade-offs

- **Stored rejections.** A rejected transfer is saved so that a retry returns the same answer even after the balance changes. Cost: rows for requests that moved no money.
- **Global, permanent keys.** Required by the contract (lookup by key alone), costs unbounded table growth — see production notes.
- **`READ COMMITTED` with explicit row locks, accounts locked in a fixed order from Go.** Simple and fast, no serialization failures; the cost is that the order must never change. Deadlock retries (up to 3, with backoff and jitter) are only a safety net. Integration test C2 runs with retries off to prove the order works.
- **Fake store instead of mocks; real PostgreSQL for concurrency.** Fast unit tests without Docker, but the database behaviour is tested only in the Docker-based tests.
- **No circuit breaker.** The only dependency is the service's own database; timeouts give fast failure and `/readyz` answers `503`. Authentication and limits are not in the contract — see production notes.
- **Strict, literal validation.** `amount` must be an integer literal (`100.0` is rejected), field names are case sensitive, strings are not trimmed or case-folded. This avoids hidden synonyms of keys and ids; the price is that a sloppy client gets `400`/`422` instead of being forgiven.
