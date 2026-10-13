# From MVP to production

This service is a test assignment: an MVP that is correct where money is concerned and deliberately simple everywhere else. Below is what would change for production, **most important first**. None of it is implemented; the design decisions behind the current state are in [DECISIONS.md](DECISIONS.md), and how to deploy the current image is in [OPERATIONS.md](OPERATIONS.md#production-deployment).

## Priorities

| # | Area | Now (MVP) | Production |
|---|---|---|---|
| 1 | **Security** | No authentication (the contract has none); Swagger UI on in the demo; demo database password; ports on loopback only | Client authentication (JWT or mTLS); only the owner of `from_account_id` may debit it; idempotency keys bound to the client; TLS; secrets from a vault; `/docs` off (`DOCS_ENABLED=false`), the OpenAPI file on a developer portal or the API gateway |
| 2 | **Limits** | None beyond `int64` | Rate limiting per client (at the gateway); per-transfer and per-day limits; an upper bound on the amount |
| 3 | **Data safety** | PostgreSQL in `tmpfs`, schema dropped and recreated on every start, no migration tool | Managed PostgreSQL with replication, backups and point-in-time recovery; versioned migrations (`golang-migrate` / `goose`) as a separate deploy step, only `migrations/` |
| 4 | **Observability** | One JSON log line per request with a request id | Metrics, traces, central logs, alerts and a balance invariant check — [below](#observability) |
| 5 | **Events** | None | `transfer.completed` / `transfer.rejected` through a transactional outbox to a message bus, for analytics and other services — [below](#events-for-analytics) |
| 6 | **Idempotency keys and history** | Global keys kept forever | Keys in a separate store with a TTL; `transfers` partitioned by `created_at` with a retention policy |
| 7 | **Scaling** | One instance in the demo; the service is stateless | Replicas behind a load balancer; PgBouncer; a queue or batching for hot accounts; sharding by account with a saga for cross-shard transfers; reads stay on the primary (a `GET` right after a `POST` must see it) |
| 8 | **Ledger** | `transfers` is the journal | Double-entry ledger (debit/credit rows) once there are operations other than transfers |
| 9 | **Image and delivery** | Alpine image, Docker Compose | Distroless image with a built-in health command; an orchestrator with liveness/readiness probes and a stop grace period longer than `SHUTDOWN_TIMEOUT`; image scanning and signing; proxies must not decode `%2F` in paths (keys may contain `/`) |

Money and data first (1–3), then the ability to see and react (4–5), then growth (6–9).

## Observability

- **Metrics** (OpenTelemetry / Prometheus):
  - per endpoint — request rate, errors, latency;
  - business — transfers by outcome and `reason`, amount moved, idempotent replays, `409`s;
  - database — deadlock retries by code, exhausted retries, transaction time, pool connections in use and waiting.
- **Traces** (OpenTelemetry): a span per request with child spans for the database; `trace_id` in the log next to `request_id`; the incoming `traceparent` is honoured.
- **Logs:** the existing JSON lines shipped to a central store, with retention; ids only, no personal data.
- **Alerts:** `5xx` rate, latency of `POST /transfers`, any exhausted retries, pool saturation, failing `/readyz`.
- **Invariant check** — the one specific to money: a periodic job checks that the sum of balances equals the issued total and that no balance is negative, and alerts on any drift.

## Events for analytics

- **Events:** `transfer.completed` and `transfer.rejected` with `transfer_id`, `from_account_id`, `to_account_id`, `amount`, `reason`, `created_at`. A versioned schema (Protobuf or Avro in a schema registry).
- **Transactional outbox:** the transfer transaction also inserts the event into an `outbox` table; a relay publishes it to the bus (Kafka) and marks it sent.
  - Why: publishing after the commit loses the event if the process dies in between; publishing before the commit can announce a transfer that then rolls back. With the outbox, the event is exactly as durable as the transfer.
- **Delivery:** at least once. Consumers deduplicate by `transfer_id`. The partition key is the account id, so the events of one account stay in order.
- **Analytics:** a consumer loads the events into the warehouse (ClickHouse, BigQuery); analytics never queries the service database.
- **Alternative:** change data capture (Debezium) on `transfers` — no code in the service, but consumers become tied to the table schema.

## Kept as is

These parts are already built the production way: one transaction per transfer with a fixed lock order; idempotency on the unique key index; `CHECK` constraints in the database; retries with backoff and jitter; timeouts; graceful shutdown; a stateless service configured by environment variables; structured logs; tests on a real PostgreSQL.
