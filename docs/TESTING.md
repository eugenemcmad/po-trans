# Testing approach

The task asks to "show your approach to testing with meaningful examples" and to "focus on the cases that matter most for a service of this kind" (full coverage is not required).

For a service that moves money, the expensive bugs are not a wrong JSON field but money created, lost or moved twice: an overdraft under parallel debits, a broken total, a retried request applied again, a deadlock, a transfer cut off by a restart. Those risks depend on how PostgreSQL behaves — locks, the unique index, transactions, constraints — so they are tested **on a real PostgreSQL, concurrently, with the race detector**. The contract and validation are covered by fast table-driven unit tests without Docker. End-to-end tests check the built image over HTTP, the way a client sees it, and validate every response against `docs/openapi.yaml`.

## The risks, most important first

Test ids: `I`/`C`/`S` — integration tests on PostgreSQL (`tests/integration`), `E` — end-to-end tests (`tests/e2e`); other names are unit tests in `internal/...`. Every test function carries a one-line comment naming the rule or risk it checks.

| # | Risk | Tests |
|---|---|---|
| 1 | **Overdraft** — a balance goes negative (rule 1), above all when several debits of one account run at once | C5: 20 parallel debits of 100 from 1000 → exactly 10 succeed, 10 are `insufficient funds`, the balance is 0 · I10: the whole balance can be sent, one coin more cannot · I11: the database itself refuses a negative balance (`CHECK`), even if the code were wrong · `TestCovers`, `TestDecide` |
| 2 | **The total changes** — coins appear or vanish | C1: 200 random transfers, up to 50 at once → the total stays 10 000 and every balance equals 1000 plus its completed transfers · E9: the same over HTTP against the built service |
| 3 | **A retried request is applied twice** (rule 2) | C3: 20 identical requests at once → one transfer, one `transfer_id`, identical answers · I6, E4, `TestPostTransfer_RepeatGivesIdenticalAnswer` |
| 4 | **One key, different data** (rule 2) — also at the same time, also with invalid data | C4: 20 copies each of two bodies at once → one winner, the rest `409`, money moved once · I3, I4, E6, `TestCreateTransfer_InvalidInput` |
| 5 | **A rejected transfer goes through later** on a retry | I7: rejected, the sender is topped up, the repeat is still the same `422` and no money moves · I2, I8 |
| 6 | **Deadlock** between opposite transfers | C2: `acc_1 ↔ acc_2` in parallel with retries switched off, so a deadlock would show as a `500` · `TestDecide`: accounts are always locked in one order |
| 7 | **A restart cuts a transfer** or leaks resources | `TestServe_GracefulShutdown`: a transfer in flight finishes with `200` while new connections are refused · S1: the real `server.Run` stops and its pool is closed (checked in `pg_stat_activity`) · `goleak` in every test package |
| 8 | **The contract** — status codes, bodies, `reason` texts, order of checks | Table tests: `TestPostTransfer_Responses` (one row per case), `TestGetTransfer`, `TestGetAccount`, `TestRouting`, `TestTransferRequest_Command` · E1–E8, E10 · every e2e response is validated against `docs/openapi.yaml` |
| 9 | **Internal details leak**, or a failure is not a clean `500` | `TestInternalErrorsAreHidden`, `TestPanicIsRecovered`, `TestReadyz_DatabaseDown`, `TestCreateTransfer_StorageErrors` |

## Three levels

| Level | Where | Database | What it is for |
|---|---|---|---|
| Unit | `internal/...` | none — an in-memory store (`tests/memstore`) | rules, validation, every status code and text, error mapping, logs, graceful shutdown; fast, no Docker |
| Integration | `tests/integration` | PostgreSQL (`db-test`) | everything that depends on the database: transactions, row locks, waiting on the unique key index, `CHECK`, races |
| End-to-end | `tests/e2e` | a fresh environment from `docker-compose.yml` | the built image as a black box over HTTP: contract, exact balance changes, the total under parallel load |

A case is tested once, on the cheapest level that can catch it: formats and codes on the unit level, database behaviour on the integration level.

## Rules the tests follow

- **Table-driven:** one row per case, so an edge case (`0`, `-1`, `100.0`, `"100"`, 255/256 characters, `"\t\t"`) is one more line.
- **An in-memory store, not generated mocks:** `tests/memstore` is a small real implementation of the ports (a fake); tests check behaviour, not call sequences. It has no locks, so concurrency is tested only on PostgreSQL.
- **Race detector** on unit and integration tests (in Docker and CI).
- **No leaked goroutines:** `goleak` in every test package.
- **Reproducible:** random tests use a fixed seed, printed on failure; the e2e run starts from a known state (ten accounts with 1000) and checks exact differences, not absolute values.
- **The same on any machine:** no sleeps to order events, only `127.0.0.1:0` ports, shutdown through a context instead of OS signals; CI runs the unit tests on Linux, Windows and macOS.

## Deliberately not done

- **A coverage percentage.** The task does not ask for it; completeness comes from the risk list above and the table tests.
- **Mocking the database for the money logic.** A stub behaves as it was written, not as PostgreSQL does — exactly where a mistake would cost money.
- **Two service instances in the tests.** Correctness lives in the database; C1–C5 already run many concurrent database sessions.
- **Load and performance tests** — a production topic.

## Running

`make test`, `make test-ci`, `make e2e` — with the equivalent `docker compose` commands in [OPERATIONS.md](OPERATIONS.md#tests).
