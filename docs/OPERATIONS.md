# Operations

## Requirements

Docker and Docker Compose ≥ 2.24 (the e2e overlay uses `!reset`). Windows, Linux and macOS are supported; the target platform of the images is `linux/amd64`. Go on the host is needed only for `make test`; `make` is optional — every target has an equivalent command below.

The shell examples are for `bash` (on Windows: Git Bash or WSL).

## Building

The single multi-stage `Dockerfile` (all stages on Alpine 3.24) has these targets:

| Target | Contents | Used by |
|---|---|---|
| `deps` | Go toolchain, downloaded modules, all sources | base of `build` and `test` |
| `build` | static binary (`CGO_ENABLED=0`) | base of `runtime` |
| `test` | `deps` + gcc (for `-race`) + `golangci-lint` v2.14.0; tests, `docs/openapi.yaml` and SQL included | compose services `tests`, `e2e`; `make lint` |
| `runtime` | Alpine + the binary `/usr/local/bin/api`, non-root user (uid 10001), port 3000 | compose service `api`; production |

```bash
docker compose build api                              # the runtime image, as compose uses it
docker build --target runtime -t po-trans-api .       # the same image, standalone
```

On an arm64 host add `--platform linux/amd64`; arm64 itself is not supported or tested.

`go build ./...` on the host only checks that the code compiles; running the binary on the host needs a PostgreSQL of your own (`DATABASE_URL`) and is not a supported path — use compose.

## Running the service

```bash
docker compose up -d --build     # make docker-up
docker compose down              # make docker-down
```

Startup chain: `db` (PostgreSQL 18 in `tmpfs`, health-checked over TCP) → `demo-setup` (one-shot) → `api`.

`demo-setup` runs `psql` with `deploy/local/reset.sql` (drop and recreate the schema) → `migrations/00001_init.sql` → `deploy/local/seed.sql` (`acc_1` … `acc_10`, 1000 each) in one transaction; the API does not start unless it succeeded. The API itself has no migrations, seeding or reset logic. At start the API pings the database and exits with an error if it is unreachable (`restart: unless-stopped` starts it again).

Check: `curl -s http://localhost:3000/readyz` → `{"status":"ok"}`.

A step-by-step check of the running service — Swagger UI, requests to send, expected answers, where to look at balances, stored transfers and logs — is in [MANUAL_CHECK.md](MANUAL_CHECK.md).

### Network and password

The API port is published on the loopback interface only (`127.0.0.1:3000`; `localhost` works); the database port is not published. The database password defaults to `app` for the demo; set `DB_PASSWORD` to change it. The password is inserted into `DATABASE_URL` as is, so it must not contain `@`, `:`, `/`, `%`, `?` or `#`.

### Resetting and restarting

| Action | Effect on data |
|---|---|
| `docker compose down && docker compose up -d --build` | clean state (new containers) — recommended reset |
| `docker compose up -d` on a running environment | **resets the data** to the initial state: Compose runs `demo-setup` again, and recreates the `api` container (observed with Compose 2.38). Running transfers are not cut: `DROP SCHEMA` waits for their locks |
| `docker compose restart api` | data stays |
| `db` restarts by itself | data is lost (`tmpfs`); `db` has `restart: "no"` on purpose, so the environment fails visibly (`/readyz` → `503`) rather than silently working without tables |

Never run `demo-setup` or `deploy/local/*` against a real database. In production only `migrations/` is applied, as a separate deploy step (see [Production deployment](#production-deployment)).

## Second instance

The service keeps no state of its own, so a second instance on the same database needs no setup of its own. It must **not** run `demo-setup`: that would reset the data under the first instance.

**On another server**: run the `runtime` image (see [Building](#building)) with `DATABASE_URL` pointing to the same database:

```bash
docker run -d --name po-trans-api -p 3000:3000 \
  -e DATABASE_URL='postgres://<user>:<password>@<db-host>:5432/<db>?sslmode=require' \
  <registry>/po-trans-api:<tag>
```

The database must already have the schema and must be reachable from that server. The demo database is not: its port is not published outside the compose network, so this case is for a real database (see [Production deployment](#production-deployment)).

**On this host**, next to the running environment (`docker compose up -d --build` — the first instance, with the database initialised). Host port 3000 is taken by the first instance, so publish any free port; 3001 in the example:

```bash
docker compose run -d --no-deps --name po-trans-api-2 -p 127.0.0.1:3001:3000 api   # second instance, no init
curl -s http://localhost:3001/readyz                                                 # {"status":"ok"}
docker rm -f po-trans-api-2                                                          # stop and remove it
```

`run` takes the image, environment and network of the `api` service; `--no-deps` keeps `db` and `demo-setup` untouched. The container name is an example too.

## Production deployment

How to deploy the current image. What would change in the service itself for production — security, limits, observability, events — is in [PRODUCTION.md](PRODUCTION.md).

The demo compose file is not a production setup: production has no `tmpfs`, no `demo-setup` and no seed data.

1. Build the `runtime` image (see [Building](#building)) and push it to your registry.
2. Apply the schema — only `migrations/`, never `deploy/local/`:

   ```bash
   psql "$DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f migrations/00001_init.sql
   ```

   The schema has no version tracking; once it starts to change, switch to `golang-migrate` or `goose` (the `00001_` naming fits both).
3. Run the image with `DATABASE_URL` (required) and, if needed, the variables from [Configuration](#configuration). It listens on `:3000` and writes JSON logs to stdout.
4. Probes: liveness `GET /healthz`, readiness `GET /readyz` (`503` while the database is unreachable).
5. Stop with `SIGTERM` and a grace period longer than `SHUTDOWN_TIMEOUT` (compose uses `20s` for `15s`).
6. Scale by adding replicas behind a load balancer — the same image with the same `DATABASE_URL` (see [Second instance](#second-instance)). Keep `DB_MAX_CONNS` × replicas (plus other clients) within the database's `max_connections` (100 by default), or put PgBouncer in front.

## Configuration

Environment variables only; durations use Go syntax (`10s`).

| Variable | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | — (required) | PostgreSQL connection string |
| `HTTP_ADDR` | `:3000` | listen address |
| `DB_MAX_CONNS` | `20` | pool size |
| `REQUEST_TIMEOUT` | `10s` | per-request limit, from entering the handler (bounds database work, including waiting on an idempotency key; reading the body is bounded by `HTTP_READ_TIMEOUT`) |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | |
| `HTTP_READ_TIMEOUT` | `5s` | |
| `HTTP_WRITE_TIMEOUT` | `15s` | counted from the end of the headers; must be larger than `REQUEST_TIMEOUT` so an error response can still be written — the service refuses to start otherwise |
| `HTTP_IDLE_TIMEOUT` | `60s` | |
| `SHUTDOWN_TIMEOUT` | `15s` | graceful shutdown limit; compose gives the containers `stop_grace_period: 20s` |
| `E2E_BASE_URL` | — | tests only: address of the API for the e2e tests |
| `DOCS_ENABLED` | `false` | `true` serves Swagger UI at `/docs` (see [MANUAL_CHECK.md](MANUAL_CHECK.md)); the demo `docker-compose.yml` sets it to `true`. Keep it off in production |
| `DB_PASSWORD` | `app` | compose only: database password of the demo and test environments (see [Network and password](#network-and-password)) |

Unless `DATABASE_URL` sets them, the pool uses `application_name=po-trans` (visible in `pg_stat_activity`) and `idle_in_transaction_session_timeout=30s` (the database ends a transaction left open by a stuck client and releases its row locks).

## Shutdown

On `SIGINT`/`SIGTERM` the service stops accepting connections, waits for started requests (up to `SHUTDOWN_TIMEOUT`, then closes them) and only then closes the database pool, so a transfer in progress is never cut off.

## Logs and request ids

One JSON line per request (liveness and readiness probes are logged at `debug` level, so they do not show at the default level unless they fail): `request_id`, `method`, `path`, `status`, `duration_ms`; for `POST /api/v1/transfers` also `idempotency_key`, `from_account_id`, `to_account_id`, `amount` (when parsed). Every `4xx` answer of the API endpoints (`POST /api/v1/transfers`, `GET /api/v1/transfers/{key}`, `GET /api/v1/accounts/{id}`) — `400`, `404`, `409`, `422` — carries `reason`, the same text as in the response body; routing answers (`404`/`405` for an unknown path or method) have none. `5xx` are logged at `error` level with the full error chain (it never reaches the client); `2xx` and `4xx`, including `422`, at `info`. The id is returned in `X-Request-ID`; a sane client-supplied `X-Request-ID` (≤ 128 characters, no control characters) is used instead of a generated one.

## Tests

What is tested and why — the risks and the tests that cover them — is in [TESTING.md](TESTING.md). This section is about running them.

| Make target | Equivalent command | What it does |
|---|---|---|
| `make test` | `go test ./internal/...` | unit tests on the host, no Docker, no `-race` (that needs gcc) |
| `make test-ci` | `docker compose -p po-trans-test --profile test run --rm --build tests`, then `docker compose -p po-trans-test --profile test down` (also run `down` if the first command fails) | unit + integration tests with `-race` against PostgreSQL in `db-test` |
| `make e2e` | see below | full black-box run on a fresh environment |
| `make lint` | `docker compose --profile test run --rm --build --no-deps tests golangci-lint run` | `golangci-lint` v2.14.0 |
| `make docker-up` / `docker-down` | `docker compose up -d --build` / `docker compose down` | |

`make e2e` is, with `E2E=docker compose -p po-trans-e2e -f docker-compose.yml -f docker-compose.e2e.yml`:

```bash
$E2E --profile test down
$E2E up -d --build
$E2E --profile test run --rm --build --no-deps e2e
$E2E --profile test down          # also after a failure of the previous steps
```

Every e2e run builds its own environment from scratch (the tests assert exact values and check at start that all accounts hold 1000) and removes it afterwards; a separate project name and no published port mean it does not touch a working environment. Do **not** run the `e2e` service by hand on a running environment — `--no-deps` matters, because restarting `demo-setup` would reset the data in the middle of a run.

Both the `tests` and `e2e` services fail if no test is found (`go test` exits 0 with no test files); `-race` is used for unit and integration tests only, e2e tests an ordinary build.

Integration tests need `DATABASE_URL`; without it they fail instead of skipping. They are in one package and run one after another because they share `db-test`.

### CI

`.github/workflows/ci.yml` runs `make lint`, `make test-ci` and `make e2e` on Ubuntu, and `go test ./internal/...` on Ubuntu, Windows and macOS.

## Troubleshooting

- `curl -d '…'` answers `400 unsupported content type` — add `-H 'Content-Type: application/json'`.
- `docker compose up` fails with `!reset` or unknown tag — Docker Compose is older than 2.24.
- `e2e` fails at start with "initial state … want 1000" — the environment was used before; use `make e2e`, which builds a fresh one.
- Port 3000 is busy — stop what uses it; `make e2e` does not need it.
- `GET /api/v1/transfers/..` returns a redirect or `404` — clients and the router normalise dot segments; encode them as `%2E%2E`.
- `/docs` answers `404` — `DOCS_ENABLED` is not `true` (it is off by default; the demo `docker-compose.yml` turns it on).
- Swagger UI at `/docs` is a blank page — the browser cannot reach the jsDelivr CDN for its scripts.
- "Try it out" fails with "Failed to fetch" — the page was opened as `127.0.0.1:3000`; open it as `localhost:3000`.
- `/readyz` returns `503` — the database is unreachable or was restarted (its data is in `tmpfs`); `docker compose down && docker compose up -d --build`.
- `api` keeps restarting and logs `connect to database` — the database is unreachable or `DATABASE_URL` is wrong (check `DB_PASSWORD` for forbidden characters).
