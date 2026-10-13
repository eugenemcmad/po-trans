# Manual check

A quick walk through the service for a reviewer: about five minutes, only Docker and a browser (and `curl`, if you prefer it to Swagger UI).

## 1. Start

```bash
docker compose up -d --build
docker compose ps                      # wait until api is "healthy" (about 10 s)
curl -s http://localhost:3000/readyz   # {"status":"ok"}
```

The database starts with `acc_1` … `acc_10`, 1000 coins each, and no transfers.

## 2. Open Swagger UI

Open <http://localhost:3000/docs> — use `localhost`, not `127.0.0.1`, or the browser blocks the calls. The page is served by the API itself (switched on by `DOCS_ENABLED: "true"` in `docker-compose.yml`; off by default) and shows `docs/openapi.yaml`: every endpoint, status code and `reason` text. To send a request: open an operation → **Try it out** → fill in the body or the parameter → **Execute**; the response code and body appear below. The page needs internet access for its scripts (jsDelivr CDN).

## 3. Send transfers

Send these bodies to `POST /api/v1/transfers` one after another (in Swagger UI, or with `curl` — see below) and compare the answers:

| # | Body | Expected answer |
|---|---|---|
| 1 | `{"idempotency_key":"check-1","from_account_id":"acc_1","to_account_id":"acc_2","amount":100}` | `200` `{"transfer_id":"…","status":"completed","from_account_id":"acc_1","to_account_id":"acc_2","amount":100}` |
| 2 | the same body again | `200`, exactly the same body (same `transfer_id`); the money is not moved again |
| 3 | key `check-1`, `"amount":101` | `409` `{"status":"error","reason":"idempotency key reused with different parameters"}` |
| 4 | `{"idempotency_key":"check-2","from_account_id":"acc_1","to_account_id":"acc_2","amount":5000}` | `422` `{"status":"rejected","reason":"insufficient funds"}` |
| 5 | `{"idempotency_key":"check-3","from_account_id":"acc_3","to_account_id":"acc_3","amount":10}` | `422` `{"status":"rejected","reason":"self-transfer not allowed"}` |
| 6 | `{"idempotency_key":"check-4","from_account_id":"acc_1","to_account_id":"acc_99","amount":10}` | `422` `{"status":"rejected","reason":"account not found"}` |
| 7 | `{"idempotency_key":"check-5","from_account_id":"acc_1","to_account_id":"acc_2","amount":-1}` | `400` `{"status":"error","reason":"amount must be a positive integer"}` |

With `curl` (the `Content-Type` header is required):

```bash
curl -s -i -X POST http://localhost:3000/api/v1/transfers -H 'Content-Type: application/json' \
  -d '{"idempotency_key":"check-1","from_account_id":"acc_1","to_account_id":"acc_2","amount":100}'
```

## 4. Look at the result

Through the API (Swagger UI or `curl`):

| Request | Expected answer |
|---|---|
| `GET /api/v1/accounts/acc_1` | `200` `{"account_id":"acc_1","balance":900}` — one transfer of 100, the repeat did not count |
| `GET /api/v1/accounts/acc_2` | `200` `{"account_id":"acc_2","balance":1100}` |
| `GET /api/v1/accounts/acc_99` | `404` `{"status":"error","reason":"account not found"}` |
| `GET /api/v1/transfers/check-1` | `200`, `"status":"completed"`, with `idempotency_key` and `created_at` (format `2025-01-01T00:00:00Z`) |
| `GET /api/v1/transfers/check-2` | `200`, `"status":"rejected"`, `"reason":"insufficient funds"` — rejections are stored |
| `GET /api/v1/transfers/check-5` | `404` `{"status":"error","reason":"transfer not found"}` — a `400` is not stored |

In the database:

```bash
docker compose exec db psql -U app -c 'SELECT sum(balance) AS total, min(balance) AS lowest FROM accounts'
#  total | lowest
#  10000 |    900          the total never changes, no balance is negative

docker compose exec db psql -U app -c 'SELECT idempotency_key, from_account_id, to_account_id, amount, status, reason FROM transfers ORDER BY created_at'
#  check-1 | acc_1 | acc_2  |  100 | completed |
#  check-2 | acc_1 | acc_2  | 5000 | rejected  | insufficient funds
#  check-3 | acc_3 | acc_3  |   10 | rejected  | self-transfer not allowed
#  check-4 | acc_1 | acc_99 |   10 | rejected  | account not found
```

In the log — one JSON line per request, with `status`, the transfer fields and the `reason` of a refusal:

```bash
docker compose logs api
```

## 5. Start over

```bash
docker compose down && docker compose up -d --build
```
